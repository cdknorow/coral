// Command qa3713seed seeds an isolated Coral data dir with a realistic dataset
// for the #3713 dashboard latency-budget harness.
//
// It is a MEASUREMENT helper only; it does not change product code. Build it
// inside the coral-go module (so it can import the internal packages), e.g.
//
//	cp -r seed coral-go/cmd/qa3713seed
//	cd coral-go && ~/go-install/go/bin/go build -tags "dev fts5" -o /tmp/qa3713/seed ./cmd/qa3713seed
//
// What it seeds:
//   - N live sessions (store.live_sessions), agent_type=claude, board_name=<board id>
//   - a board + a subscriber per session + several tasks (messageboard.db)
//   - one Claude JSONL transcript per session under <projects>/<encoded-workdir>/<uuid>.jsonl
//     (a few "big" 60-100MB ones, the rest small), matching resolveClaudeTranscript's layout
//   - a files dir with many subdirectories for /api/filesystem/list
//   - real tmux sessions "claude-<uuid>" on <datadir>/tmux.sock so the sessions are "alive"
//     (the startup reconciler then leaves them active, and poll/capture work)
//
// It writes a targets JSON (-out) the measurement script reads.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/store"
	"github.com/google/uuid"
)

type target struct {
	Name       string `json:"name"`
	ID         string `json:"id"`
	AgentType  string `json:"agent_type"`
	WorkingDir string `json:"working_dir"`
}

type targets struct {
	ProjectsDir  string   `json:"projects_dir"`
	DataDir      string   `json:"data_dir"`
	Socket       string   `json:"socket"`
	BoardID      string   `json:"board_id"`
	BoardLabel   string   `json:"board_label"`
	Chat         target   `json:"chat"`
	Poll         target   `json:"poll"`
	FilesPath    string   `json:"files_path"`
	SessionIDs   []string `json:"session_ids"`
	NumSessions  int      `json:"num_sessions"`
	NumBig       int      `json:"num_big"`
	BigBytesEach int64    `json:"big_bytes_each"`
}

func main() {
	var (
		dataDir  = flag.String("datadir", "", "CORAL_DATA_DIR path")
		projects = flag.String("projects", "", "CLAUDE_PROJECTS_DIR path")
		label    = flag.String("board", "qa3713", "board label")
		n        = flag.Int("n", 24, "number of live sessions")
		big      = flag.Int("big", 4, "number of big transcripts")
		bigMB    = flag.Int("bigmb", 80, "size of each big transcript in MB")
		smallKB  = flag.Int("smallkb", 64, "size of each small transcript in KB")
		filesN   = flag.Int("filesn", 5000, "number of subdirectories in the files dir")
		socket   = flag.String("socket", "", "tmux socket path (default <datadir>/tmux.sock)")
		tmuxBin  = flag.String("tmux", "tmux", "tmux binary")
		out      = flag.String("out", "", "targets JSON output path")
	)
	flag.Parse()
	if *dataDir == "" || *projects == "" || *out == "" {
		fatal("need -datadir, -projects, -out")
	}
	if *socket == "" {
		*socket = filepath.Join(*dataDir, "tmux.sock")
	}
	ctx := context.Background()

	must(os.MkdirAll(*dataDir, 0o755))
	must(os.MkdirAll(*projects, 0o755))
	workRoot := filepath.Join(*dataDir, "work")
	must(os.MkdirAll(workRoot, 0o755))

	// Open stores (ensureSchema runs on Open).
	db, err := store.Open(filepath.Join(*dataDir, "sessions.db"))
	must(err)
	defer db.Close()
	ss := store.NewSessionStore(db)

	bs, err := board.NewStore(filepath.Join(*dataDir, "messageboard.db"))
	must(err)
	defer bs.Close()

	b, err := bs.CreateBoard(ctx, *label, "Operator")
	must(err)
	fmt.Printf("[seed] board label=%q id=%s\n", *label, b.ID)

	roles := []string{"Backend", "Frontend", "QA", "Security", "UX", "DevOps", "Lead", "Docs"}
	t := targets{
		ProjectsDir: *projects, DataDir: *dataDir, Socket: *socket,
		BoardID: b.ID, BoardLabel: *label, NumSessions: *n, NumBig: *big,
		BigBytesEach: int64(*bigMB) * 1024 * 1024,
	}

	for i := 0; i < *n; i++ {
		id := uuid.NewString()
		workdir := filepath.Join(workRoot, fmt.Sprintf("agent%02d", i))
		must(os.MkdirAll(workdir, 0o755))
		agentName := filepath.Base(workdir)

		// transcript
		bytes := int64(*smallKB) * 1024
		isBig := i < *big
		if isBig {
			bytes = int64(*bigMB) * 1024 * 1024
		}
		tp := transcriptPath(*projects, workdir, id)
		must(os.MkdirAll(filepath.Dir(tp), 0o755))
		writeTranscript(tp, bytes)

		// live session row
		bn := b.ID
		role := roles[i%len(roles)]
		dn := fmt.Sprintf("%s-%02d", role, i)
		backend := "tmux"
		ls := &store.LiveSession{
			SessionID:   id,
			AgentType:   "claude",
			AgentName:   agentName,
			WorkingDir:  workdir,
			DisplayName: &dn,
			BoardName:   &bn,
			Backend:     &backend,
			CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		}
		must(ss.RegisterLiveSession(ctx, ls))

		// board subscriber (session_name must be "claude-<uuid>" to match availability)
		sessionName := "claude-" + id
		_, err := bs.Subscribe(ctx, b.ID, dn, role, sessionName, nil, "")
		must(err)

		// real tmux session so the reconciler keeps it alive + capture works
		runTmux(*tmuxBin, *socket, "new-session", "-d", "-s", sessionName, "-c", workdir, "sleep 1000000")

		t.SessionIDs = append(t.SessionIDs, id)
		if isBig && t.Chat.ID == "" {
			t.Chat = target{Name: sessionName, ID: id, AgentType: "claude", WorkingDir: workdir}
			t.Poll = target{Name: sessionName, ID: id, AgentType: "claude", WorkingDir: workdir}
		}
		fmt.Printf("[seed] session %02d id=%s big=%v transcript=%s\n", i, id[:8], isBig, human(bytes))
	}

	// tasks: a spread of statuses/assignees
	statuses := []string{"pending", "in_progress", "blocked", "pending", "in_progress"}
	for i := 0; i < 15; i++ {
		role := roles[i%len(roles)]
		dn := fmt.Sprintf("%s-%02d", role, i%*n)
		tk, err := bs.CreateTask(ctx, b.ID, fmt.Sprintf("Task %d", i), "seeded task body", "medium", "Operator", dn)
		must(err)
		// nudge status away from default where possible
		_ = statuses
		_ = tk
	}
	fmt.Printf("[seed] created board tasks\n")

	// files dir with many subdirectories
	filesPath := filepath.Join(*dataDir, "filesbig")
	must(os.MkdirAll(filesPath, 0o755))
	for i := 0; i < *filesN; i++ {
		must(os.MkdirAll(filepath.Join(filesPath, fmt.Sprintf("dir_%05d", i)), 0o755))
	}
	t.FilesPath = filesPath
	fmt.Printf("[seed] files dir %s with %d subdirs\n", filesPath, *filesN)

	data, _ := json.MarshalIndent(t, "", "  ")
	must(os.WriteFile(*out, data, 0o644))
	fmt.Printf("[seed] wrote targets to %s\n", *out)
}

// transcriptPath mirrors resolveClaudeTranscript: <base>/<workdir with / -> ->/<id>.jsonl
func transcriptPath(base, workdir, id string) string {
	encoded := strings.ReplaceAll(workdir, "/", "-")
	return filepath.Join(base, encoded, id+".jsonl")
}

// writeTranscript writes a valid Claude JSONL transcript of about n bytes,
// alternating user/assistant entries the parser accepts.
func writeTranscript(path string, n int64) {
	f, err := os.Create(path)
	must(err)
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	defer w.Flush()

	// ~1.8KB assistant text block so the file is realistic prose, not one giant line.
	filler := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 40) // ~1.8KB
	var written int64
	i := 0
	ts := "2026-10-15T00:00:00.000Z"
	for written < n {
		var line string
		if i%2 == 0 {
			u := map[string]any{
				"type": "user", "timestamp": ts,
				"message": map[string]any{"role": "user", "content": fmt.Sprintf("Message %d: please continue the work and report status.", i)},
			}
			bl, _ := json.Marshal(u)
			line = string(bl)
		} else {
			a := map[string]any{
				"type": "assistant", "timestamp": ts,
				"message": map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "text", "text": fmt.Sprintf("Reply %d. %s", i, filler)},
				}},
			}
			bl, _ := json.Marshal(a)
			line = string(bl)
		}
		w.WriteString(line)
		w.WriteByte('\n')
		written += int64(len(line)) + 1
		i++
	}
}

func runTmux(bin, socket string, args ...string) {
	full := append([]string{"-S", socket}, args...)
	cmd := exec.Command(bin, full...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fatal(fmt.Sprintf("tmux %s: %v", strings.Join(args, " "), err))
	}
}

func human(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%dMB", b>>20)
	default:
		return fmt.Sprintf("%dKB", b>>10)
	}
}

func must(err error) {
	if err != nil {
		fatal(err.Error())
	}
}
func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "[seed] FATAL:", msg)
	os.Exit(1)
}
