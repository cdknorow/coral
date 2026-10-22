package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/hooks"
)

// `coral artifacts prune` (#3554): the Operator frees space from Coral's stored artifacts and
// diffs. It always shows a dry run first, then asks (or needs --yes without a terminal). It talks
// only to the Coral on this computer, signed with Coral's command-line token, so an agent can't
// run it in the Operator's name.
//
// Exit codes: 0 done (or nothing to do), 1 the server refused or failed, 2 a usage problem or no
// confirmation, 4 Coral isn't running here, 130 cancelled.
func runArtifacts(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: coral artifacts prune --older-than 14d [--kind diffs|objects|diff-objects|all] [--dry-run] [--yes] [--url ...] [--home DIR]")
		fmt.Fprintln(stderr, "       coral artifacts prune --shrink [--older-than 0] [--dry-run] [--yes] [--url ...] [--home DIR]")
		return 2
	}
	if len(args) == 0 || args[0] != "prune" {
		return usage()
	}
	fs := flag.NewFlagSet("artifacts prune", flag.ContinueOnError)
	fs.SetOutput(stderr)
	olderThan := fs.String("older-than", "", "only what is at least this old: 14d, 36h, 90m (a prune needs at least 1d)")
	kind := fs.String("kind", "diffs", "what to prune: diffs (closed tasks' and stopped sessions' stored diffs), objects (artifacts nothing links), diff-objects (shared diff copies nothing uses), or all; comma-separated")
	shrink := fs.Bool("shrink", false, "rewrite closed diffs without secrets, dot-files, binaries and oversized files instead of deleting them (the text changes stay)")
	dryRun := fs.Bool("dry-run", false, "only show what would be freed")
	yes := fs.Bool("yes", false, "don't ask (needed without a terminal)")
	base := fs.String("url", hooks.CoralBase(), "the running Coral server, on this computer")
	home := fs.String("home", "", "Coral's data folder, if the server was started with --home (default: CORAL_DATA_DIR or ~/.coral)")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		return usage()
	}
	*base = runningServerURL(fs, *base, *home)
	age, err := parseAge(*olderThan)
	if err != nil {
		fmt.Fprintf(stderr, "coral artifacts prune: --older-than: %v\n", err)
		return 2
	}
	if !*shrink && *olderThan == "" {
		fmt.Fprintln(stderr, "coral artifacts prune: --older-than is required (for example --older-than 14d)")
		return 2
	}
	server := strings.TrimRight(*base, "/")
	if !loopbackURL(server) {
		fmt.Fprintf(stderr, "coral artifacts prune: %s is not this computer: this command only talks to the Coral running here\n", server)
		return 2
	}
	token, err := cloud.ReadCLIToken(config.ResolveCoralDir(*home))
	if err != nil {
		fmt.Fprintf(stderr, "coral artifacts prune: could not read Coral's command-line token (%v): run this as the user Coral runs as, with Coral started (and --home if it uses one)\n", err)
		return 2
	}
	cli := cloud.CLIClient{Base: server, Token: token}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	req := pruneRequest{OlderThanSeconds: int64(age / time.Second), Kinds: strings.Split(*kind, ","), Shrink: *shrink}
	dry, code := prunePost(ctx, cli, req, true, stderr)
	if code >= 0 {
		return code
	}
	verb := "freed"
	if *shrink {
		verb = "saved"
	}
	printPruneReport(stdout, dry, true)
	items := dry.items()
	if items == 0 {
		fmt.Fprintln(stdout, "Nothing to do.")
		return 0
	}
	if *dryRun {
		return 0
	}
	if !*yes {
		if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
			fmt.Fprintln(stderr, "coral artifacts prune: not a terminal: run it again with --yes to go ahead")
			return 2
		}
		action := "Delete"
		if *shrink {
			action = "Rewrite"
		}
		fmt.Fprintf(stdout, "%s %d item(s), about %s %s? [y/N] ", action, items, humanSize(dry.Freed), verb)
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(stdout, "Nothing changed.")
			return 0
		}
	}
	done, code := prunePost(ctx, cli, req, false, stderr)
	if code >= 0 {
		return code
	}
	printPruneReport(stdout, done, false)
	return 0
}

type pruneRequest struct {
	OlderThanSeconds int64    `json:"older_than_seconds"`
	Kinds            []string `json:"kinds,omitempty"`
	DryRun           bool     `json:"dry_run"`
	Shrink           bool     `json:"shrink"`
}

type pruneTotalReply struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

type pruneReply struct {
	DryRun  bool                       `json:"dry_run"`
	Shrink  bool                       `json:"shrink"`
	Kinds   map[string]pruneTotalReply `json:"kinds"`
	ByBoard map[string]int64           `json:"by_board"`
	Largest []struct {
		Kind    string  `json:"kind"`
		ID      string  `json:"id"`
		Name    string  `json:"name"`
		Board   string  `json:"board"`
		Bytes   int64   `json:"bytes"`
		AgeDays float64 `json:"age_days"`
	} `json:"largest"`
	Freed   int64 `json:"freed"`
	Removed int   `json:"removed"`
	Skipped int   `json:"skipped"`
	Errors  int   `json:"errors"`
	Capped  bool  `json:"capped"`
}

func (r pruneReply) items() int {
	n := 0
	for _, t := range r.Kinds {
		n += t.Count
	}
	return n
}

// prunePost sends one prune request; code is -1 with a reply, else the exit code (reported).
func prunePost(ctx context.Context, cli cloud.CLIClient, req pruneRequest, dry bool, stderr io.Writer) (pruneReply, int) {
	req.DryRun = dry
	body, _ := json.Marshal(req)
	hdr := http.Header{}
	hdr.Set("Content-Type", "application/json")
	var out pruneReply
	resp, err := cli.Do(ctx, http.MethodPost, "/api/artifacts/prune", body, hdr, 10*time.Minute)
	switch {
	case errors.Is(err, cloud.ErrNotCoral):
		fmt.Fprintf(stderr, "coral artifacts prune: %s: %v. Nothing was sent.\n", cli.Base, err)
		return out, 4
	case ctx.Err() != nil:
		return out, 130
	case err != nil:
		fmt.Fprintf(stderr, "coral artifacts prune: Coral is not running at %s: start it and run this again\n", cli.Base)
		return out, 4
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		fmt.Fprintf(stderr, "coral artifacts prune: %s\n", e.Error)
		if resp.StatusCode == http.StatusBadRequest {
			return out, 2
		}
		return out, 1
	}
	if err := json.Unmarshal(data, &out); err != nil {
		fmt.Fprintln(stderr, "coral artifacts prune: unexpected answer from Coral")
		return out, 1
	}
	return out, -1
}

func printPruneReport(w io.Writer, r pruneReply, dry bool) {
	what := "Would free"
	if r.Shrink {
		what = "Would save"
	}
	if !dry {
		what = "Freed"
		if r.Shrink {
			what = "Saved"
		}
	}
	kinds := make([]string, 0, len(r.Kinds))
	for k := range r.Kinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		t := r.Kinds[k]
		fmt.Fprintf(w, "  %-13s %5d item(s)  %s\n", k, t.Count, humanSize(t.Bytes))
	}
	if dry && len(r.Largest) > 0 {
		fmt.Fprintln(w, "  Largest:")
		for i, it := range r.Largest {
			if i == 10 {
				break
			}
			label := it.ID
			if len(label) == 64 { // an artifact or diff-object digest: its short form, then its name
				label = label[:12]
			}
			if it.Name != "" {
				label += " " + it.Name
			}
			board := ""
			if it.Board != "" {
				board = ", board " + it.Board
			}
			fmt.Fprintf(w, "    %-9s %-40s %9s, %.0f days old%s\n", it.Kind, truncate(label, 40), humanSize(it.Bytes), it.AgeDays, board)
		}
	}
	fmt.Fprintf(w, "%s: %s", what, humanSize(r.Freed))
	if !dry {
		fmt.Fprintf(w, " (%d removed or rewritten, %d left alone because they changed, %d errors)", r.Removed, r.Skipped, r.Errors)
	}
	if r.Capped {
		fmt.Fprint(w, "; stopped at the per-run cap")
	}
	fmt.Fprintln(w)
}

// parseAge reads 14d, 36h, 90m or 0 (empty is 0).
func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%q is not an age such as 14d, 36h or 90m", s)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%q is not an age such as 14d, 36h or 90m", s)
	}
	return d, nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
