package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/subgentic/coral/internal/clierr"
	"github.com/subgentic/coral/internal/clitext"
)

// taskShowArtifact is one artifact of a task as task show prints it: its link, never an inline artifact's content (#285).
type taskShowArtifact struct {
	Name        string `json:"name"`
	URI         string `json:"uri,omitempty"`
	MediaType   string `json:"media_type,omitempty"`
	Digest      string `json:"digest,omitempty"`
	InlineBytes int    `json:"inline_bytes,omitempty"`
	Landed      bool   `json:"landed,omitempty"`
}

// taskShow is task show's view of a task: what a person needs to read a task's result (#285).
type taskShow struct {
	ID          int64              `json:"id"`
	Title       string             `json:"title"`
	Status      string             `json:"status"`
	Priority    string             `json:"priority,omitempty"`
	AssignedTo  string             `json:"assigned_to,omitempty"`
	CreatedBy   string             `json:"created_by,omitempty"`
	CompletedBy string             `json:"completed_by,omitempty"`
	Body        string             `json:"body,omitempty"`
	Outcome     string             `json:"outcome,omitempty"`
	Message     string             `json:"message,omitempty"`
	Artifacts   []taskShowArtifact `json:"artifacts"`
}

// cmdTaskShow is `coral-board task show <id> [--json]`: a task's title, status, assignee, body, outcome, result message and its
// artifacts' links, from the same read as task detail (GET /tasks/<id>), so it shows a worker nothing task detail doesn't.
// Server text is printed without terminal controls.
func cmdTaskShow(st *boardState, args []string) {
	wantJSON := hasFlag(args, "--json")
	args = withoutFlag(args, "--json")
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: coral-board task show <id> [--json]")
		os.Exit(clierr.ExitUsage)
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintln(os.Stderr, "task show needs a task id, a whole number from 1 up")
		os.Exit(clierr.ExitUsage)
	}
	data, status, err := apiCallRaw("GET", fmt.Sprintf("/%s/tasks/%d", url.PathEscape(st.Project), id), nil)
	if err != nil || status != 200 {
		fmt.Fprintln(os.Stderr, "Unable to read task: "+clierr.Describe(err, status, data))
		clierr.Exit1()
	}
	t, err := parseTaskShow(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Unable to read task: "+err.Error())
		clierr.Exit1()
	}
	if wantJSON {
		out, _ := json.MarshalIndent(t, "", "  ")
		fmt.Println(string(out))
		return
	}
	printTaskShow(os.Stdout, t)
}

// parseTaskShow reads the server's task (GET /tasks/<id>) into task show's view, its text without terminal controls.
func parseTaskShow(data []byte) (taskShow, error) {
	var raw struct {
		ID                int64   `json:"id"`
		Title             string  `json:"title"`
		Status            string  `json:"status"`
		Priority          string  `json:"priority"`
		AssignedTo        *string `json:"assigned_to"`
		CreatedBy         string  `json:"created_by"`
		CompletedBy       *string `json:"completed_by"`
		Body              *string `json:"body"`
		CompletionMessage *string `json:"completion_message"`
		Workflow          struct {
			Outcome   string `json:"outcome"`
			Artifacts []struct {
				Name      string `json:"name"`
				URI       string `json:"uri"`
				Content   string `json:"content"`
				MediaType string `json:"media_type"`
				Digest    string `json:"digest"`
				Landed    bool   `json:"landed"`
			} `json:"artifacts"`
		} `json:"workflow"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return taskShow{}, err
	}
	val := func(p *string) string {
		if p == nil {
			return ""
		}
		return clitext.Clean(*p)
	}
	t := taskShow{ID: raw.ID, Title: clitext.CleanName(raw.Title), Status: clitext.CleanName(raw.Status), Priority: clitext.CleanName(raw.Priority),
		AssignedTo: clitext.CleanName(val(raw.AssignedTo)), CreatedBy: clitext.CleanName(raw.CreatedBy), CompletedBy: clitext.CleanName(val(raw.CompletedBy)),
		Body: val(raw.Body), Outcome: clitext.CleanName(raw.Workflow.Outcome), Message: val(raw.CompletionMessage), Artifacts: []taskShowArtifact{}}
	for _, a := range raw.Workflow.Artifacts {
		t.Artifacts = append(t.Artifacts, taskShowArtifact{Name: clitext.CleanName(a.Name), URI: clitext.CleanName(a.URI), MediaType: clitext.CleanName(a.MediaType),
			Digest: clitext.CleanName(a.Digest), InlineBytes: len(a.Content), Landed: a.Landed})
	}
	return t, nil
}

// printTaskShow writes the task for a person.
func printTaskShow(w io.Writer, t taskShow) {
	fmt.Fprintf(w, "Task #%d: %s\n", t.ID, t.Title)
	line := "Status: " + t.Status
	if t.Outcome != "" {
		line += " (" + t.Outcome + ")"
	}
	if t.Priority != "" {
		line += "  Priority: " + t.Priority
	}
	fmt.Fprintln(w, line)
	var who []string
	for _, p := range [][2]string{{"Assigned to", t.AssignedTo}, {"Created by", t.CreatedBy}, {"Completed by", t.CompletedBy}} {
		if p[1] != "" {
			who = append(who, p[0]+": "+p[1])
		}
	}
	if len(who) > 0 {
		fmt.Fprintln(w, strings.Join(who, "  "))
	}
	if t.Body != "" {
		fmt.Fprintln(w, "\n"+t.Body)
	}
	if t.Message != "" {
		fmt.Fprintln(w, "\nResult: "+t.Message)
	}
	if len(t.Artifacts) == 0 {
		fmt.Fprintln(w, "\nArtifacts: none")
		return
	}
	fmt.Fprintf(w, "\nArtifacts (%d):\n", len(t.Artifacts))
	for _, a := range t.Artifacts {
		where := a.URI
		if where == "" && a.InlineBytes > 0 {
			where = fmt.Sprintf("(inline, %d bytes; task detail %d shows it)", a.InlineBytes, t.ID)
		}
		extra := ""
		if a.MediaType != "" {
			extra += " " + a.MediaType
		}
		if a.Landed {
			extra += " landed"
		}
		if extra != "" {
			extra = "  [" + strings.TrimSpace(extra) + "]"
		}
		fmt.Fprintf(w, "  %s  %s%s\n", a.Name, where, extra)
	}
}
