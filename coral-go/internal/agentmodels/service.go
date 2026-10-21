package agentmodels

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/agentenv"
	"github.com/subgentic/coral/internal/agentsignin"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/executil"
	"github.com/subgentic/coral/internal/safego"
)

const (
	// maxAge is how long a list is used before it is fetched again in the background.
	maxAge = 24 * time.Hour
	// retryAfterError: a CLI that failed (not signed in, offline) is asked again after this.
	retryAfterError = time.Hour
	// minManualGap limits the Operator's Refresh, per family.
	minManualGap = 30 * time.Second
	maxOutput    = 8 << 20
)

// fetchTimeout bounds one CLI run (a var for tests).
var fetchTimeout = 30 * time.Second

// ErrSettingsUnreadable is what a finder returns when Coral's settings could not be read, so the CLI
// to run is unknown: no probe is started, and the CLI found on PATH is never asked instead (#4350).
var ErrSettingsUnreadable = errors.New("Coral's settings could not be read")

// ErrTooSoon is a manual refresh within minManualGap of the last one.
var ErrTooSoon = errors.New("the model list was refreshed moments ago; try again shortly")

// Family is the CLI behind an agent type ("" when it has no model list): Coral's agy,
// antigravity and gemini types all run agy.
func Family(agentType string) string {
	switch agentType {
	case at.Claude:
		return "claude"
	case at.Codex:
		return "codex"
	case at.Agy, at.Antigravity, at.Gemini:
		return "agy"
	case at.Pi:
		return "pi"
	}
	return ""
}

// Result is what the models route returns for an agent type.
type Result struct {
	AgentType  string     `json:"agent_type"`
	Models     []Model    `json:"models"`
	FetchedAt  *time.Time `json:"fetched_at"`
	Refreshing bool       `json:"refreshing"`
	Source     string     `json:"source,omitempty"` // "account", or "bundled" (codex's shipped list)
	Error      string     `json:"error,omitempty"`
	// Thinking is the levels Coral gives this type's CLI at launch (ThinkingLevels); empty when the type takes none. A model's own `efforts` narrow it.
	Thinking []string `json:"thinking,omitempty"`
}

type entry struct {
	key         string // the CLI's path, modification time and size
	models      []Model
	source      string
	fetchedAt   time.Time
	attemptedAt time.Time
	err         string
	refreshing  bool
	manualAt    time.Time
}

// Fetcher runs a family's CLI at path and returns its models (tests replace it).
type Fetcher func(ctx context.Context, family, path string) ([]Model, string, error)

// Service keeps each family's list. Get never runs a CLI on the caller's goroutine: it returns
// what is cached and, when that is missing, old or for another build of the CLI, starts a
// background fetch. Fetches run one at a time.
type Service struct {
	find  func(agentType string) (string, error)
	fetch Fetcher
	now   func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
	busy    chan struct{}
	wg      sync.WaitGroup
}

// NewService returns a Service that finds each CLI with find (agent.FindCLI with the
// current settings: the same executable a launch runs).
func NewService(find func(agentType string) (string, error)) *Service {
	return &Service{find: find, fetch: FetchFromCLI, now: time.Now, entries: map[string]*entry{}, busy: make(chan struct{}, 1)}
}

// SetFetcher replaces how a CLI is run (tests).
func (s *Service) SetFetcher(f Fetcher) { s.fetch = f }

// cliKey identifies a build of the CLI at path.
func cliKey(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return path
	}
	return fmt.Sprintf("%s|%d|%d", path, info.ModTime().UnixNano(), info.Size())
}

// Get returns the cached list for agentType, starting a background fetch when it is due.
func (s *Service) Get(agentType string) Result {
	return withBuiltin(s.get(agentType, false))
}

// builtinDisplayNames are the names the curated list shows for the models it names; any other model shows its id.
var builtinDisplayNames = map[string]string{
	"claude-opus-5-5":   "Claude Opus 5.5",
	"claude-sonnet-5-5": "Claude Sonnet 5.5",
	"claude-haiku-5-5":  "Claude Haiku 5.5",
	"claude-fable-5-1":  "Claude Fable 5.1",
}

// withBuiltin fills an empty result with Coral's curated list for the type
// (agenttypes.AgentTypeModels, "source": "builtin"), so a picker has choices before the CLI
// has listed its own, or when it can't.
func withBuiltin(res Result) Result {
	res.Thinking = ThinkingLevels(res.AgentType)
	if len(res.Models) > 0 {
		return res
	}
	var models []Model
	for _, v := range at.AgentTypeModels[res.AgentType] {
		name := v
		if n, ok := builtinDisplayNames[v]; ok {
			name = n
		}
		models = append(models, Model{Value: v, DisplayName: name})
	}
	if models = clean(models); len(models) > 0 {
		res.Models, res.Source = models, "builtin"
	}
	return res
}

// Refresh starts a fetch now (the Operator's Refresh), at most once per minManualGap per
// family, and returns the cached list meanwhile.
func (s *Service) Refresh(agentType string) (Result, error) {
	family := Family(agentType)
	if family != "" {
		s.mu.Lock()
		e := s.entries[family]
		if e != nil && !e.manualAt.IsZero() && s.now().Sub(e.manualAt) < minManualGap {
			s.mu.Unlock()
			return withBuiltin(s.get(agentType, false)), ErrTooSoon
		}
		s.mu.Unlock()
	}
	return withBuiltin(s.get(agentType, true)), nil
}

func (s *Service) get(agentType string, manual bool) Result {
	res := Result{AgentType: agentType, Models: []Model{}}
	family := Family(agentType)
	if family == "" {
		res.Error = "this agent type has no model list"
		return res
	}
	path, err := s.find(agentType)
	if err != nil {
		if errors.Is(err, ErrSettingsUnreadable) {
			res.Error = "Coral's settings could not be read, so the CLI could not be found: try again"
		} else {
			res.Error = "the CLI is not installed, or not where Settings > CLI paths says"
		}
		return res
	}
	key := cliKey(path)
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[family]
	if e == nil {
		e = &entry{}
		s.entries[family] = e
	}
	due := manual || e.key != key ||
		(e.err == "" && now.Sub(e.fetchedAt) >= maxAge) ||
		(e.err != "" && now.Sub(e.attemptedAt) >= retryAfterError)
	if due && !e.refreshing {
		e.refreshing = true
		e.attemptedAt = now
		if manual {
			e.manualAt = now
		}
		s.wg.Add(1)
		safego.Go("agentmodels:"+family, func() { defer s.wg.Done(); s.runFetch(family, path, key) })
	}
	if len(e.models) > 0 {
		res.Models = append([]Model{}, e.models...)
		t := e.fetchedAt
		res.FetchedAt = &t
		res.Source = e.source
	}
	res.Refreshing = e.refreshing
	res.Error = e.err
	return res
}

// runFetch runs one family's CLI, one fetch at a time, and stores what it listed. A failure
// keeps the previous list and records why, without the CLI's output.
func (s *Service) runFetch(family, path, key string) {
	s.busy <- struct{}{}
	defer func() { <-s.busy }()
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	models, source, err := s.fetch(ctx, family, path)

	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[family]
	e.refreshing = false
	e.key = key // a failure too: this build is retried after retryAfterError, not on every Get
	if err != nil {
		e.err = describe(err)
		slog.Info("agent models: listing failed", "cli", family, "reason", e.err)
		return
	}
	e.key, e.models, e.source, e.fetchedAt, e.err = key, models, source, s.now(), ""
}

// describe is a failure in Coral's words: never the CLI's own output.
func describe(err error) string {
	var exitErr *exec.ExitError
	var notSignedIn *agentsignin.NotSignedInError
	switch {
	case errors.As(err, &notSignedIn):
		return notSignedIn.Error() // Coral's own sentence (#5469): "Claude is not signed in"
	case errors.Is(err, context.DeadlineExceeded):
		return "the CLI didn't answer in time"
	case errors.Is(err, ErrNoModels):
		return "the CLI listed no models: is it signed in?"
	case errors.As(err, &exitErr):
		return fmt.Sprintf("the CLI exited with status %d: is it signed in?", exitErr.ExitCode())
	}
	return "the CLI's model list couldn't be read"
}

// Wait waits for background fetches (tests and shutdown).
func (s *Service) Wait() { s.wg.Wait() }

// FetchFromCLI runs the family's CLI at path once.
func FetchFromCLI(ctx context.Context, family, path string) ([]Model, string, error) {
	dir, err := os.MkdirTemp("", "coral-models-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(dir)
	switch family {
	case "claude":
		if err := agentsignin.Require(ctx, "claude", path); err != nil { // signed out: Claude is not started, its sign-in flow would hold the login (#5469)
			return nil, "", err
		}
		m, err := fetchClaude(ctx, path, dir)
		return m, "account", err
	case "codex":
		out, err := runCLI(ctx, path, dir, "debug", "models")
		if err == nil {
			if m, perr := ParseCodexCatalog(out); perr == nil {
				return m, "account", nil
			}
		}
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		// Offline or signed out: the catalog shipped with this codex.
		out, err = runCLI(ctx, path, dir, "debug", "models", "--bundled")
		if err != nil {
			return nil, "", err
		}
		m, err := ParseCodexCatalog(out)
		return m, "bundled", err
	case "agy":
		out, err := runCLI(ctx, path, dir, "models")
		if err != nil {
			return nil, "", err
		}
		m, err := ParseAgyModels(out)
		return m, "account", err
	case "pi":
		out, err := runCLI(ctx, path, dir, "--list-models")
		if err != nil {
			return nil, "", err
		}
		m, err := ParsePiModels(out)
		return m, "account", err
	}
	return nil, "", fmt.Errorf("no model list for %q", family)
}

// cliCommand is the CLI at path with args (never a shell), run in dir, with Coral's own
// variables removed from its environment: it runs as the user, like the user's own terminal.
func cliCommand(ctx context.Context, path, dir string, args ...string) *exec.Cmd {
	cmd := executil.Command(ctx, path, args...)
	cmd.Dir = dir
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	var env []string
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); !agentenv.IsCoral(name) { // any case (#19)
			env = append(env, kv)
		}
	}
	cmd.Env = env
	return cmd
}

// limitedBuffer keeps the first maxOutput bytes and reports an overflow.
type limitedBuffer struct {
	bytes.Buffer
	over bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxOutput - b.Len(); len(p) > room {
		b.over = true
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

func runCLI(ctx context.Context, path, dir string, args ...string) ([]byte, error) {
	cmd := cliCommand(ctx, path, dir, args...)
	var out limitedBuffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if out.over {
		return nil, errors.New("the CLI's output was too long")
	}
	return out.Bytes(), nil
}

// fetchClaude sends Claude Code's initialize control request (no prompt, so nothing is sent
// to a model) and reads its models from the reply.
func fetchClaude(ctx context.Context, path, dir string) ([]Model, error) {
	cmd := cliCommand(ctx, path, dir, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		in.Close()
		_ = cmd.Process.Kill() // it has answered: nothing else is wanted from it
		_ = cmd.Wait()
	}()
	if _, err := io.WriteString(in, `{"type":"control_request","request_id":"coral-models","request":{"subtype":"initialize"}}`+"\n"); err != nil {
		return nil, err
	}
	models, err := ParseClaudeInitialize(io.LimitReader(out, maxOutput))
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return models, err
}
