package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/clierr"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/hooks"
)

const docsUsageText = `Usage: coral-agent docs [topic|all]

Prints Coral's own documentation for agents. With no topic it lists the topics;
"coral-agent docs <topic>" prints one (for example: coral-agent docs board), and
"coral-agent docs all" prints every topic. It carries the agent's identity, so it works
with strict local auth, where a plain curl of /api/agent-docs is refused.`

// cmdDocs lists or prints the embedded agent docs (GET /api/agent-docs[/{topic}|/all]).
func cmdDocs(args []string, out io.Writer) error {
	if len(args) > 1 {
		return errors.New(docsUsageText)
	}
	if len(args) == 0 {
		b, err := docsGet("/api/agent-docs")
		if err != nil {
			return err
		}
		return printDocTopics(b, out)
	}
	topic := strings.TrimSuffix(args[0], ".md")
	if topic == "all" {
		b, err := docsGet("/api/agent-docs/all")
		if err != nil {
			return err
		}
		_, err = out.Write(b)
		return err
	}
	b, err := docsGet("/api/agent-docs/" + url.PathEscape(topic))
	if err != nil {
		var notFound docNotFoundError
		if errors.As(err, &notFound) {
			if list, lerr := docsGet("/api/agent-docs"); lerr == nil {
				var sb strings.Builder
				printDocTopics(list, &sb)
				return fmt.Errorf("no Coral doc named %q.\n%s", topic, sb.String())
			}
		}
		return err
	}
	var doc struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("Coral sent a doc that could not be read: %w", err)
	}
	_, err = io.WriteString(out, doc.Content)
	return err
}

type docNotFoundError struct{ topic string }

func (e docNotFoundError) Error() string { return "doc not found: " + e.topic }

// docsGet reads one docs route; the body is cleaned of terminal control unless --raw.
func docsGet(path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, serverURL+path, nil)
	if err != nil {
		return nil, err
	}
	wait := clierr.Timeout(15 * time.Second)
	resp, err := (&http.Client{Timeout: wait}).Do(req)
	if err != nil {
		return nil, clierr.CannotReach(hooks.RedactedURL(serverURL), err, wait)
	}
	defer resp.Body.Close()
	b, _ := readServer(resp.Body)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, docNotFoundError{topic: path}
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("Coral answered %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return b, nil
}

// printDocTopics prints the topic names with their titles, one per line, and how to read one.
func printDocTopics(list []byte, out io.Writer) error {
	var docs []struct{ Name, Title string }
	if err := json.Unmarshal(list, &docs); err != nil {
		return fmt.Errorf("Coral sent a docs list that could not be read: %w", err)
	}
	if len(docs) == 0 {
		fmt.Fprintln(out, "This Coral build carries no agent docs.")
		return nil
	}
	fmt.Fprintln(out, "Coral docs for agents. Read one with: coral-agent docs <topic>  (coral-agent docs all prints everything)")
	for _, d := range docs {
		fmt.Fprintf(out, "  %-24s %s\n", d.Name, d.Title)
	}
	return nil
}
