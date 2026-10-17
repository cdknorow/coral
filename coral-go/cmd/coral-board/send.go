package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cdknorow/coral/internal/clihelp"
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
//	coral-board send --address <server>/<team> [--reply-to <message id>] [--message-id <id>] "<message>"
//
// The server checks that the sender is an Orchestrator on its board (or the
// Operator), that both boards allow cross-team messaging, and the rate limit. With
// --address the message goes to another Coral through Coral Hub (#3489), to an address
// `coral-board targets` lists; --message-id retries an earlier send without delivering it
// twice. --attach (repeatable, at most 4) sends files the sender uploaded with the message
// (#4379; the usage text says what it may send and what it is not).
func runSend(args []string, id sendIdentity, out io.Writer) error {
	var target, to, address, replyTo, messageID string
	var attach []string
	var msgParts []string
	literal := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--":
			msgParts = append(msgParts, args[i+1:]...)
			literal = true
			i = len(args)
		case "--board", "--to", "--address", "--reply-to", "--message-id", "--attach":
			if i+1 >= len(args) || args[i+1] == "" {
				return fmt.Errorf("%s needs a value", args[i])
			}
			switch args[i] {
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
			default:
				messageID = args[i+1]
			}
			i++
		default:
			if len(msgParts) == 0 && clihelp.IsFlagLike(args[i]) {
				return fmt.Errorf("unknown option %q for send (put -- before message text that starts with '-')", args[i])
			}
			msgParts = append(msgParts, args[i])
		}
	}
	var message string
	if !literal && len(msgParts) == 1 && msgParts[0] == "-" {
		text, err := readStdinText("message")
		if err != nil {
			return err
		}
		message = text
	} else {
		message = strings.TrimSpace(strings.Join(msgParts, " "))
		warnShellSyntax(os.Stderr, message)
	}
	if address != "" {
		if target != "" || to != "" {
			return fmt.Errorf("--address can't be combined with --board or --to")
		}
		if len(attach) > 0 {
			return fmt.Errorf("--attach works only with --board (a board on this Coral) for now")
		}
		return runRelaySend(id, address, replyTo, messageID, message, out)
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
		return fmt.Errorf("send to %q failed (%d): %s", target, status, result.Error)
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
		os.Exit(1)
	}
}

// runRelaySend sends message to another Coral through Coral Hub (#3489).
func runRelaySend(id sendIdentity, address, replyTo, messageID, message string, out io.Writer) error {
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
	data, status, err := apiCallRawTimeout(http.MethodPost, "/"+url.PathEscape(id.Board)+"/relay-messages", body, 35*time.Second)
	if err != nil {
		return err
	}
	var result struct {
		Error           string `json:"error"`
		Message         string `json:"message"`
		MessageID       string `json:"message_id"`
		TargetMessageID int64  `json:"target_message_id"`
		Duplicate       bool   `json:"duplicate"`
	}
	_ = json.Unmarshal(data, &result)
	if status != http.StatusOK {
		if result.Error == "" {
			result.Error = strings.TrimSpace(string(data))
		}
		msg := fmt.Sprintf("send to %q failed (%d): %s", address, status, result.Error)
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
	return nil
}

// cmdTargets lists the other Corals' teams this board may message through Coral Hub.
//
//	coral-board targets
func cmdTargets() {
	st := loadState()
	if st == nil {
		fmt.Fprintln(os.Stderr, "Not subscribed to any board.")
		os.Exit(1)
	}
	if err := runTargets(st.Project, resolveSubscriberID(), resolveSessionName(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
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
			Address   string `json:"address"`
			Reachable bool   `json:"reachable"`
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
		state := "online"
		if !t.Reachable {
			state = "offline"
		}
		fmt.Fprintf(out, "%s (%s)\n", t.Address, state)
	}
	return nil
}
