package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// cmdTaskQueueVerb is the merge queue's two task verbs (#5689):
//
//	attach <id> --artifacts manifest.json   add patch rows to a task in review (the author's next revision, a reviewer's fix commit)
//	send-back <id> --note "why"             the orchestrator returns an approved, completed task to its author
//	mark-landed <id> --digest D [--commit SHA[:subject]]   the orchestrator records that approved patches landed, and the commits
//
// digestRe is what --digest takes: 64 hex characters, with the sha256: prefix of an upload's digest or without it (#5793).
var digestRe = regexp.MustCompile(`^(sha256:)?[0-9a-fA-F]{64}$`)

func cmdTaskQueueVerb(st *boardState, args []string, action string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, `Usage: coral-board task attach <id> --artifacts manifest.json | send-back <id> --note "why" | mark-landed <id> --digest D [--commit SHA[:subject]]`)
		os.Exit(1)
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Invalid task ID")
		os.Exit(1)
	}
	fs := flag.NewFlagSet(action, flag.ExitOnError)
	manifest := fs.String("artifacts", "", "Manifest of the patch rows to add (attach)")
	note := fs.String("note", "", "Why (send-back)")
	var digests, commits listFlag
	fs.Var(&digests, "digest", "Digest of a patch that landed: 64 hex characters, with or without the sha256: prefix (mark-landed; repeat)")
	fs.Var(&commits, "commit", "A commit that landed it: the full 40-hex SHA, or SHA:subject (mark-landed; repeat)")
	fs.Parse(args[1:])
	*note = flagText(*note, "note")
	body := map[string]any{"subscriber_id": resolveSubscriberID()}
	switch action {
	case "attach":
		if *manifest == "" {
			fmt.Fprintln(os.Stderr, "--artifacts manifest.json is required")
			os.Exit(1)
		}
		data, err := os.ReadFile(*manifest)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var artifacts []map[string]any
		if err := json.Unmarshal(data, &artifacts); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		body["artifacts"] = artifacts
	case "mark-landed":
		if len(digests) == 0 {
			fmt.Fprintln(os.Stderr, "--digest D is required (repeat it for each patch that landed)")
			os.Exit(1)
		}
		for _, d := range digests {
			if !digestRe.MatchString(d) {
				fmt.Fprintf(os.Stderr, "--digest %q is not 64 hex characters (with or without the sha256: prefix)\n", d)
				exitFn(1)
				return
			}
		}
		var cs []map[string]string
		for _, c := range commits {
			sha, subject, _ := strings.Cut(c, ":")
			cs = append(cs, map[string]string{"sha": sha, "subject": subject})
		}
		body["digests"], body["commits"] = []string(digests), cs
	case "send-back":
		if strings.TrimSpace(*note) == "" {
			fmt.Fprintln(os.Stderr, `--note "why" is required`)
			os.Exit(1)
		}
		body["note"] = *note
	}
	data, status, err := apiCallRaw("POST", fmt.Sprintf("/%s/tasks/%d/%s", url.PathEscape(st.Project), id, action), body)
	if err != nil || status != http.StatusOK {
		fmt.Fprintln(os.Stderr, "Task", action, "failed:", err, string(data))
		exitFn(1)
		return
	}
	switch action {
	case "attach":
		var out struct {
			NewRound *struct {
				Round int `json:"round"`
			} `json:"new_round"`
		}
		_ = json.Unmarshal(data, &out)
		if out.NewRound != nil {
			fmt.Printf("Task #%d: attached. An earlier approval does not carry over: the reviewers review the new bytes in round %d.\n", id, out.NewRound.Round)
		} else {
			fmt.Printf("Task #%d: attached.\n", id)
		}
		printUnverifiedArtifacts(data)
	case "mark-landed":
		fmt.Printf("Task #%d: %d patch(es) marked landed, %d commit(s) recorded.\n", id, len(digests), len(commits))
	default:
		fmt.Printf("Task #%d sent back to its author.\n", id)
	}
}
