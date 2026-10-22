package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/subgentic/coral/internal/clierr"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/hooks"
)

const notifyUsageText = `Usage: coral-agent notify --title "Title" [--message "text"] [--level info|success|warning|error] [--alert]

Shows a notification on every open Coral dashboard: a toast, or with --alert a
centered message the user must acknowledge. It carries the agent's identity, so it
also works with strict local auth (use it in hooks instead of curl).`

// cmdNotify posts a dashboard notification (POST /api/notifications, #3418).
func cmdNotify(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	title := fs.String("title", "", "notification title (required)")
	message := fs.String("message", "", "notification text")
	level := fs.String("level", "info", "info, success, warning or error")
	alert := fs.Bool("alert", false, "a centered message the user must acknowledge")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return errors.New(notifyUsageText)
	}
	if strings.TrimSpace(*title) == "" {
		return errors.New("--title is required\n" + notifyUsageText)
	}
	switch *level {
	case "info", "success", "warning", "error":
	default:
		return errors.New("--level must be info, success, warning or error")
	}
	body := map[string]string{"title": *title, "message": *message, "level": *level}
	if *alert {
		body["type"] = "alert"
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, serverURL+"/api/notifications", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	wait := clierr.Timeout(10 * time.Second)
	resp, err := (&http.Client{Timeout: wait}).Do(req)
	if err != nil {
		return clierr.CannotReach(hooks.RedactedURL(serverURL), err, wait)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := readServer(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("Coral answered %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	fmt.Fprintln(out, "Notification sent")
	return nil
}
