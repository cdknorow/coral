package main

import (
	"encoding/json"
	"fmt"
	"github.com/subgentic/coral/internal/clierr"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/subgentic/coral/internal/clihelp"
)

// sendIdentity is who is sending: always this agent's own subscription and
// session, never a value given on the command line (there is no --as).
type sendIdentity struct {
	Board        string
	SubscriberID string
	SessionName  string
}

// runSend posts a cross-team message to another board's Orchestrator.
//
//	coral-board send --board <target> [--to <orchestrator>] [--attach coral://artifacts/<id> ...] "<message>"
//	coral-board send --address <server>/<team> [--reply-to <message id>] [--message-id <id>] [--attach coral://artifacts/<id> ...] "<message>"
//
// The server checks that the sender is an Orchestrator on its board (or the
// Operator), that both boards allow cross-team messaging, and the rate limit. With
// --address the message goes to another Coral through Coral Hub (#3489), to an address
// `coral-board targets` lists; --message-id retries an earlier send without delivering it
// twice. --attach (repeatable, at most 4) sends files the sender uploaded with the message
// (#4379; the usage text says what it may send and what it is not).
func runSend(args []string, id sendIdentity, out io.Writer) error {
	var target, to, address, replyTo, messageID, file string
	var attach []string
	var msgParts []string
	literal := false
	for i := 0; i < len(args); i++ {
		switch foldOpt(args[i], sendFlags) { // --BOARD is --board
		case "--":
			msgParts = append(msgParts, args[i+1:]...)
			literal = true
			i = len(args)
		case "--board", "--to", "--address", "--reply-to", "--message-id", "--attach", "--file":
			if i+1 >= len(args) || args[i+1] == "" {
				return fmt.Errorf("%s needs a value", args[i])
			}
			switch foldOpt(args[i], sendFlags) {
			case "--board":
				target = args[i+1]
			case "--to":
				to = args[i+1]
			case "--address":
				address = args[i+1]
			case "--reply-to":
				replyTo = args[i+1]
			case "--attach":
				attach = append(attach, args[i+1])
			case "--file":
				file = args[i+1]
			default:
				messageID = args[i+1]
			}
			i++
		default:
			if len(msgParts) == 0 && clihelp.IsFlagLike(args[i]) {
				return fmt.Errorf("unknown option %q for send. To post text that starts with '-', put -- before it.", args[i])
			}
			msgParts = append(msgParts, args[i])
		}
	}
	var message string
	if file != "" {
		if len(msgParts) > 0 {
			return fmt.Errorf("--file can't be combined with message text")
		}
		text, err := readFileText(file, "message")
		if err != nil {
			return err
		}
		message = text
	} else if !literal && len(msgParts) == 1 && msgParts[0] == "-" {
		text, err := readStdinText("message")
		if err != nil {
			return err
		}
		message = text
	} else {
		message = strings.TrimSpace(strings.Join(msgParts, " "))
		if err := checkShellSyntax(os.Stderr, "send", message); err != nil {
			return err
		}
	}
	if address != "" {
		if target != "" || to != "" {
			return fmt.Errorf("--address can't be combined with --board or --to")
		}
		if len(attach) > 4 {
			return fmt.Errorf("at most 4 --attach per send")
		}
		return runRelaySend(id, address, replyTo, messageID, message, attach, out)
	}
	if replyTo != "" || messageID != "" {
		return fmt.Errorf("--reply-to and --message-id go with --address")
	}
	if target == "" || message == "" {
		return fmt.Errorf(`usage: coral-board send --board <target> [--to <orchestrator>] [--attach coral://artifacts/<id> ...] "<message>"`)
	}
	if len(attach) > 4 {
		return fmt.Errorf("at most 4 --attach per send")
	}
	if id.Board == "" {
		return fmt.Errorf("not subscribed to a board. Run: coral-board join <project> --as <role>")
	}
	body := map[string]any{
		"from_board":    id.Board,
		"subscriber_id": id.SubscriberID,
		"session_name":  id.SessionName,
		"content":       message,
		"to":            to,
	}
	if len(attach) > 0 {
		body["attach"] = attach
	}
	data, status, err := apiCallRaw(http.MethodPost, "/"+url.PathEscape(target)+"/external-messages", body)
	if err != nil {
		return err
	}
	var result struct {
		Error       string   `json:"error"`
		Code        string   `json:"code"`
		DeliveredTo []string `json:"delivered_to"`
		Attached    []string `json:"attached"`
		Message     struct {
			ID int64 `json:"id"`
		} `json:"message"`
	}
	_ = json.Unmarshal(data, &result)
	if status != http.StatusOK {
		if result.Error == "" {
			result.Error = strings.TrimSpace(string(data))
		}
		return fmt.Errorf("send to %q failed (%d): %s", target, status, refusalText(result.Code, result.Error))
	}
	fmt.Fprintf(out, "Sent to '%s' (message #%d, delivered to %s). A copy is on '%s'.\n",
		target, result.Message.ID, strings.Join(result.DeliveredTo, ", "), id.Board)
	if len(result.Attached) > 0 {
		fmt.Fprintf(out, "Attached %d file(s): %s\n", len(result.Attached), strings.Join(result.Attached, ", "))
	}
	return nil
}

func cmdSend() {
	id := sendIdentity{SubscriberID: resolveSubscriberID(), SessionName: resolveSessionName()}
	if st := loadState(); st != nil {
		id.Board = st.Project
	}
	if err := runSend(os.Args[2:], id, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		clierr.Exit1()
	}
}

// runRelaySend sends message to another Coral through Coral Hub (#3489).
func runRelaySend(id sendIdentity, address, replyTo, messageID, message string, attach []string, out io.Writer) error {
	if message == "" {
		return fmt.Errorf(`usage: coral-board send --address <server>/<team> [--reply-to <message id>] [--message-id <id>] "<message>"`)
	}
	if id.Board == "" {
		return fmt.Errorf("not subscribed to a board. Run: coral-board join <project> --as <role>")
	}
	body := map[string]any{
		"subscriber_id": id.SubscriberID,
		"session_name":  id.SessionName,
		"content":       message,
		"to_address":    address,
	}
	if replyTo != "" {
		n, err := strconv.ParseInt(replyTo, 10, 64)
		if err != nil || n <= 0 {
			return fmt.Errorf("--reply-to takes a message id (a number)")
		}
		body["reply_to"] = n
	}
	// The message id is made here, so a failed send can print it for a retry that is
	// never delivered twice.
	if messageID == "" {
		messageID = uuid.NewString()
	}
	body["message_id"] = messageID
	timeout := 35 * time.Second
	if len(attach) > 0 {
		// Each file may take up to Coral Hub's 120 s transfer limit, after up to 100 s of secret scanning on
		// this computer (the server's scanFileCap); the files go before the message.
		body["attach"] = attach
		timeout = attachWait(len(attach))
	}
	data, status, err := apiCallRawTimeout(http.MethodPost, "/"+url.PathEscape(id.Board)+"/relay-messages", body, timeout)
	if err != nil {
		return err
	}
	var result struct {
		Error           string   `json:"error"`
		Code            string   `json:"code"`
		Message         string   `json:"message"`
		MessageID       string   `json:"message_id"`
		TargetMessageID int64    `json:"target_message_id"`
		Duplicate       bool     `json:"duplicate"`
		Attached        []string `json:"attached"`
		Unattached      []string `json:"unattached_links"`
		AutoAttached    []string `json:"auto_attached"`
	}
	_ = json.Unmarshal(data, &result)
	if status != http.StatusOK {
		if result.Error == "" {
			result.Error = strings.TrimSpace(string(data))
		}
		msg := fmt.Sprintf("send to %q failed (%d): %s", address, status, refusalText(result.Code, result.Error))
		if result.Message != "" {
			msg += ": " + result.Message
		}
		switch {
		case status == http.StatusNotImplemented:
			// The hub doesn't relay (switched off, or too old): a retry fails the same way
			// until whoever runs the hub turns it on or updates it (#3503).
			msg += " (not delivered; retrying won't help until the hub relays messages)"
		case status == http.StatusTooManyRequests || status >= 500:
			msg += fmt.Sprintf(" (retry later with --message-id %s, so it is never delivered twice)", messageID)
		}
		return fmt.Errorf("%s", msg)
	}
	if result.Duplicate {
		fmt.Fprintf(out, "Already delivered to '%s' (message id %s).\n", address, result.MessageID)
		return nil
	}
	fmt.Fprintf(out, "Sent to '%s' through Coral Hub (their message #%d; message id %s). A copy is on '%s'.\n",
		address, result.TargetMessageID, result.MessageID, id.Board)
	if len(result.Attached) > 0 {
		fmt.Fprintf(out, "Attached %d file(s): %s\n", len(result.Attached), strings.Join(result.Attached, ", "))
	}
	if len(result.AutoAttached) > 0 {
		fmt.Fprintf(out, "%d of them %s attached automatically because the message's text links %s.\n",
			len(result.AutoAttached), plural(len(result.AutoAttached), "was", "were"), plural(len(result.AutoAttached), "it", "them"))
	}
	if len(result.Unattached) > 0 {
		fmt.Fprintf(out, "Warning: the message names %d %s that %s not attached. A coral://artifacts link is a file on this computer only: the other Coral cannot open it. They were not attached automatically (the Operator's cross-team files switch is off, the file is not one this team uploaded, or it is over the limits of 4 files, 8 MiB each and 16 MiB in all). Send again with --attach coral://artifacts/<id> for each you can.\n",
			len(result.Unattached), plural(len(result.Unattached), "link", "links"), plural(len(result.Unattached), "is", "are"))
		for _, l := range result.Unattached {
			fmt.Fprintf(out, "  not attached: %s\n", l)
		}
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// cmdTargets lists the other Corals' teams this board may message through Coral Hub.
//
//	coral-board targets
func cmdTargets() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board.")
		clierr.Exit1()
	}
	if err := runTargets(st.Project, resolveSubscriberID(), resolveSessionName(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		clierr.Exit1()
	}
}

func runTargets(board, subscriberID, sessionName string, out io.Writer) error {
	q := url.Values{"subscriber_id": {subscriberID}, "session_name": {sessionName}}
	data, status, err := apiCallRawTimeout(http.MethodGet, "/"+url.PathEscape(board)+"/relay-targets?"+q.Encode(), nil, 35*time.Second)
	if err != nil {
		return err
	}
	var result struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Targets []struct {
			Address string `json:"address"`
			// A pointer: Coral Hub does not send the field today, and an absent one is not "offline" (#5598).
			Reachable *bool `json:"reachable"`
		} `json:"targets"`
	}
	_ = json.Unmarshal(data, &result)
	if status != http.StatusOK {
		if result.Error == "" {
			result.Error = strings.TrimSpace(string(data))
		}
		// The same shape as send's failure: "<code>: <message>" (#3520, QA's bug bash F4).
		msg := fmt.Sprintf("listing targets failed (%d): %s", status, result.Error)
		if result.Message != "" {
			msg += ": " + result.Message
		}
		return fmt.Errorf("%s", msg)
	}
	if len(result.Targets) == 0 {
		fmt.Fprintln(out, "No targets: the Coral Hub account owner hasn't linked this team to another Coral's team.")
		return nil
	}
	for _, t := range result.Targets {
		switch {
		case t.Reachable == nil:
			fmt.Fprintln(out, t.Address)
		case *t.Reachable:
			fmt.Fprintf(out, "%s (online)\n", t.Address)
		default:
			fmt.Fprintf(out, "%s (offline)\n", t.Address)
		}
	}
	return nil
}

// refusalText is a refusal's words with its code in front when it has one: an attachment's refusal (attach_secret,
// attach_unchecked, not_yours, ...) answers {"code", "error": words}, and the code is what a script or a reader
// tells one refusal from another by; a refusal whose "error" is already the code is left as it is.
func refusalText(code, words string) string {
	if code == "" || code == words || strings.HasPrefix(words, code) {
		return words
	}
	return code + ": " + words
}

// attachWait is how long a send with n attachments waits for the server: per file the 125 s of Coral Hub's
// transfer limit, the 100 s the server may spend scanning it for secrets (scanFileCap) and the 30 s it may
// wait for another send's scan to finish (scanQueueWait, #4474), and 35 s for the message.
func attachWait(n int) time.Duration {
	return time.Duration(n)*(125+100+30)*time.Second + 35*time.Second
}
