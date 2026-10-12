package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

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
//	coral-board send --board <target> [--to <orchestrator>] "<message>"
//
// The server checks that the sender is an Orchestrator on its board (or the
// Operator), that both boards allow cross-team messaging, and the rate limit.
func runSend(args []string, id sendIdentity, out io.Writer) error {
	var target, to string
	var msgParts []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--":
			msgParts = append(msgParts, args[i+1:]...)
			i = len(args)
		case "--board", "--to":
			if i+1 >= len(args) || args[i+1] == "" {
				return fmt.Errorf("%s needs a value", args[i])
			}
			if args[i] == "--board" {
				target = args[i+1]
			} else {
				to = args[i+1]
			}
			i++
		default:
			if len(msgParts) == 0 && clihelp.IsFlagLike(args[i]) {
				return fmt.Errorf("unknown option %q for send (put -- before message text that starts with '-')", args[i])
			}
			msgParts = append(msgParts, args[i])
		}
	}
	message := strings.TrimSpace(strings.Join(msgParts, " "))
	if target == "" || message == "" {
		return fmt.Errorf(`usage: coral-board send --board <target> [--to <orchestrator>] "<message>"`)
	}
	if id.Board == "" {
		return fmt.Errorf("not subscribed to a board. Run: coral-board join <project> --as <role>")
	}
	data, status, err := apiCallRaw(http.MethodPost, "/"+url.PathEscape(target)+"/external-messages", map[string]string{
		"from_board":    id.Board,
		"subscriber_id": id.SubscriberID,
		"session_name":  id.SessionName,
		"content":       message,
		"to":            to,
	})
	if err != nil {
		return err
	}
	var result struct {
		Error       string   `json:"error"`
		DeliveredTo []string `json:"delivered_to"`
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
