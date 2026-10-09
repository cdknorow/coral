// Package background provides long-running background services for Coral.
package background

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/executil"
	"github.com/cdknorow/coral/internal/gitutil"
	"github.com/cdknorow/coral/internal/store"
)

// MinGitPollInterval is the floor for a user-configured poll interval. Git
// queries on a large repository are not cheap, so a too-eager setting would
// spend more time scanning than working.
const MinGitPollInterval = 15 * time.Second

// GitPollIntervalSettingKey is the user setting for the poll cadence, in
// seconds; "0" turns polling off.
const GitPollIntervalSettingKey = "git_poll_interval_s"

// GitPollInterval resolves the configured git poll cadence from settings. An
// unset or unparseable value falls back to defaultSeconds; 0 or less means
// polling is off.
func GitPollInterval(settings map[string]string, defaultSeconds int) time.Duration {
	raw := strings.TrimSpace(settings[GitPollIntervalSettingKey])
	if raw == "" {
		return time.Duration(defaultSeconds) * time.Second
	}
	secs, err := strconv.Atoi(raw)
	if err != nil {
		return time.Duration(defaultSeconds) * time.Second
	}
	return time.Duration(secs) * time.Second
}

// pausedRecheck is how often a paused poller re-reads its interval, so turning
// polling back on takes effect without restarting Coral.
const pausedRecheck = 30 * time.Second

// GitPoller periodically polls git branch/commit info for live agents.
type GitPoller struct {
	store      *store.GitStore
	runtime    AgentRuntime
	interval   time.Duration
	logger     *slog.Logger
	discoverFn func(ctx context.Context) ([]AgentInfo, error)
	prCache    map[string]prCacheEntry // keyed by "remote_url::branch"

	// intervalFn, when set, is consulted before every wait so a settings
	// change takes effect without a restart. A non-positive result pauses
	// polling entirely, for repositories where scanning is too expensive to
	// do on a timer at all.
	intervalFn func() time.Duration
}

type prCacheEntry struct {
	prNumber  int
	expiresAt time.Time
}

// AgentInfo holds minimal agent metadata needed by background services.
type AgentInfo struct {
	AgentName        string
	AgentType        string
	SessionID        string
	WorkingDirectory string
	DisplayName      string // role name, used as stable board subscriber_id
}

// NewGitPoller creates a new GitPoller.
func NewGitPoller(gitStore *store.GitStore, runtime AgentRuntime, interval time.Duration) *GitPoller {
	return &GitPoller{
		store:    gitStore,
		runtime:  runtime,
		interval: interval,
		logger:   slog.Default().With("service", "git_poller"),
		prCache:  make(map[string]prCacheEntry),
	}
}

// SetDiscoverFn sets a custom agent discovery function (for dependency injection).
func (p *GitPoller) SetDiscoverFn(fn func(ctx context.Context) ([]AgentInfo, error)) {
	p.discoverFn = fn
}

// SetIntervalFn supplies a user-configured poll interval, re-read before every
// wait. Return a non-positive duration to pause polling.
func (p *GitPoller) SetIntervalFn(fn func() time.Duration) { p.intervalFn = fn }

// nextWait returns how long to wait before the next pass, and whether that
// pass should actually poll. A paused poller still wakes periodically so it
// can notice the setting being turned back on.
func (p *GitPoller) nextWait() (time.Duration, bool) {
	d := p.interval
	if p.intervalFn != nil {
		d = p.intervalFn()
	}
	if d <= 0 {
		return pausedRecheck, false
	}
	if d < MinGitPollInterval {
		d = MinGitPollInterval
	}
	return d, true
}

// Run starts the polling loop. Blocks until context is cancelled.
func (p *GitPoller) Run(ctx context.Context) error {
	wait, _ := p.nextWait()
	timer := time.NewTimer(wait)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if _, enabled := p.nextWait(); enabled {
				if err := p.PollOnce(ctx); err != nil {
					p.logger.Error("poll error", "error", err)
				}
			}
			next, _ := p.nextWait()
			timer.Reset(next)
		}
	}
}

// PollOnce performs a single git polling pass for all live agents.
func (p *GitPoller) PollOnce(ctx context.Context) error {
	pollStart := time.Now()

	agents, err := p.discoverAgents(ctx)
	if err != nil {
		return fmt.Errorf("discover agents: %w", err)
	}

	// Group agents by working directory to minimize git calls
	dirToAgents := make(map[string][]AgentInfo)
	for _, agent := range agents {
		if agent.WorkingDirectory == "" {
			continue
		}
		dirToAgents[agent.WorkingDirectory] = append(dirToAgents[agent.WorkingDirectory], agent)
	}

	p.logger.Debug("git poll starting", "dirs", len(dirToAgents), "agents", len(agents))

	for workdir, dirAgents := range dirToAgents {
		dirStart := time.Now()

		resolveStart := time.Now()
		workdir = gitutil.ResolveGitRoot(ctx, workdir)
		p.logger.Debug("git resolve root", "workdir", workdir, "duration_ms", time.Since(resolveStart).Milliseconds())

		queryStart := time.Now()
		gitInfo, err := queryGit(ctx, workdir)
		queryDur := time.Since(queryStart)
		if err != nil {
			p.logger.Warn("git query failed", "workdir", workdir, "duration_ms", queryDur.Milliseconds(), "error", err)
			continue
		}
		if gitInfo == nil {
			continue
		}
		p.logger.Debug("git query", "workdir", workdir, "branch", gitInfo.Branch, "duration_ms", queryDur.Milliseconds())

		filesStart := time.Now()
		changedFiles, err := queryChangedFiles(ctx, workdir)
		filesDur := time.Since(filesStart)
		if err != nil {
			// Leaving the last list in place would show it as current
			// indefinitely; an empty cache makes the Files view recompute.
			p.logger.Warn("git changed files query failed; clearing cached list", "workdir", workdir, "duration_ms", filesDur.Milliseconds(), "error", err)
			changedFiles = []store.ChangedFile{}
		} else {
			p.logger.Debug("git changed files", "workdir", workdir, "file_count", len(changedFiles), "duration_ms", filesDur.Milliseconds())
		}

		// Look up PR number for non-default branches
		prNumber := 0
		if gitInfo.RemoteURL != nil && gitInfo.Branch != "main" && gitInfo.Branch != "master" && gitInfo.Branch != "HEAD" {
			prNumber = p.lookupPR(ctx, *gitInfo.RemoteURL, gitInfo.Branch, workdir)
		}

		dbStart := time.Now()
		for _, agent := range dirAgents {
			sessionID := agent.SessionID
			var sidPtr *string
			if sessionID != "" {
				sidPtr = &sessionID
			}

			snap := &store.GitSnapshot{
				AgentName:        agent.AgentName,
				AgentType:        agent.AgentType,
				WorkingDirectory: workdir,
				Branch:           gitInfo.Branch,
				CommitHash:       gitInfo.CommitHash,
				CommitSubject:    gitInfo.CommitSubject,
				CommitTimestamp:  &gitInfo.CommitTimestamp,
				SessionID:        sidPtr,
				RemoteURL:        gitInfo.RemoteURL,
				PRNumber:         prNumber,
				Worktree:         gitInfo.Worktree,
				BaseBranch:       gitInfo.BaseBranch,
				Ahead:            gitInfo.Ahead,
				Behind:           gitInfo.Behind,
				DirtyCount:       gitInfo.DirtyCount,
			}
			if err := p.store.UpsertGitSnapshot(ctx, snap); err != nil {
				p.logger.Warn("upsert snapshot failed", "agent", agent.AgentName, "error", err)
			}

			// Cap file count to prevent DB thrashing on large monorepos
			if len(changedFiles) > 2000 {
				p.logger.Warn("capping changed files", "agent", agent.AgentName, "total", len(changedFiles), "cap", 2000)
				changedFiles = changedFiles[:2000]
			}
			if err := p.store.ReplaceChangedFiles(ctx, agent.AgentName, workdir, changedFiles, sidPtr, "branch_point"); err != nil {
				p.logger.Warn("replace changed files failed", "agent", agent.AgentName, "error", err)
			}
		}
		p.logger.Debug("git db writes", "workdir", workdir, "agents", len(dirAgents), "duration_ms", time.Since(dbStart).Milliseconds())

		dirDur := time.Since(dirStart)
		if dirDur > 2*time.Second {
			p.logger.Warn("git poll slow for directory", "workdir", workdir, "total_ms", dirDur.Milliseconds(),
				"query_ms", queryDur.Milliseconds(), "files_ms", filesDur.Milliseconds())
		}
	}

	totalDur := time.Since(pollStart)
	p.logger.Debug("git poll complete", "dirs", len(dirToAgents), "total_ms", totalDur.Milliseconds())
	if totalDur > 5*time.Second {
		p.logger.Warn("git poll slow", "total_ms", totalDur.Milliseconds(), "dirs", len(dirToAgents))
	}

	return nil
}

func (p *GitPoller) discoverAgents(ctx context.Context) ([]AgentInfo, error) {
	if p.discoverFn != nil {
		return p.discoverFn(ctx)
	}
	// Default: discover from runtime
	return p.runtime.ListAgents(ctx)
}


// gitInfo holds parsed git query results.
type gitInfo struct {
	Branch          string
	CommitHash      string
	CommitSubject   string
	CommitTimestamp  string
	RemoteURL       *string
	Worktree        bool
	BaseBranch      string
	Ahead           int
	Behind          int
	DirtyCount      int
}

func queryGit(ctx context.Context, workdir string) (*gitInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	logger := slog.Default().With("service", "git_poller", "workdir", workdir)

	// Get branch name
	t0 := time.Now()
	out, err := executil.Command(ctx, "git", "--no-optional-locks", "-C", workdir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	logger.Debug("git rev-parse", "duration_ms", time.Since(t0).Milliseconds())
	if err != nil {
		return nil, err
	}
	branch := strings.TrimSpace(string(out))

	// Get latest commit
	t1 := time.Now()
	out, err = executil.Command(ctx, "git", "--no-optional-locks", "-C", workdir, "log", "-1", "--format=%H|%s|%aI").Output()
	logger.Debug("git log", "duration_ms", time.Since(t1).Milliseconds())
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("unexpected git log format")
	}

	// Get remote URL (best-effort)
	t2 := time.Now()
	var remoteURL *string
	out, err = executil.Command(ctx, "git", "--no-optional-locks", "-C", workdir, "remote", "get-url", "origin").Output()
	logger.Debug("git remote", "duration_ms", time.Since(t2).Milliseconds())
	if err == nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			remoteURL = &s
		}
	}

	info := &gitInfo{
		Branch:         branch,
		CommitHash:     parts[0],
		CommitSubject:  parts[1],
		CommitTimestamp: parts[2],
		RemoteURL:      remoteURL,
	}
	queryWorktreeState(ctx, workdir, info)
	return info, nil
}

func queryChangedFiles(ctx context.Context, workdir string) ([]store.ChangedFile, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	logger := slog.Default().With("service", "git_poller", "workdir", workdir)

	t0 := time.Now()
	base := gitutil.GetDiffBase(ctx, workdir, "") // uses default branch_point mode
	logger.Debug("git diff base", "base", base, "duration_ms", time.Since(t0).Milliseconds())

	t1 := time.Now()
	baseTS := getBaseTimestamp(ctx, workdir, base)
	logger.Debug("git base timestamp", "duration_ms", time.Since(t1).Milliseconds())

	fileMap := make(map[string]store.ChangedFile)

	// git diff base --numstat
	t2 := time.Now()
	// -z: NUL-terminated records with raw paths. Without it git quotes names
	// containing non-ASCII bytes (e.g. macOS screenshots' U+202F) with octal
	// escapes, which then never match a file on disk.
	out, err := executil.Command(ctx, "git", "--no-optional-locks", "-C", workdir, "diff", base, "--numstat", "-z").Output()
	if err != nil {
		// A timeout or git failure: the list would be incomplete.
		return nil, fmt.Errorf("git diff %s --numstat: %w", base, err)
	}
	records := strings.Split(string(out), "\x00")
	for i := 0; i < len(records); i++ {
		rec := records[i]
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		path := parts[2]
		if path == "" {
			// Rename: "adds\tdels\t\0old\0new\0"; the new path is what exists.
			if i+2 >= len(records) {
				break
			}
			path = records[i+2]
			i += 2
		}
		a, _ := strconv.Atoi(parts[0])
		d, _ := strconv.Atoi(parts[1])
		fileMap[path] = store.ChangedFile{
			Filepath:  path,
			Additions: a,
			Deletions: d,
			Status:    "M",
		}
	}

	logger.Debug("git diff --numstat", "files", len(fileMap), "duration_ms", time.Since(t2).Milliseconds())

	// git status --porcelain for untracked files
	lineCounts := 0
	t3 := time.Now()
	out, err = executil.Command(ctx, "git", "--no-optional-locks", "-C", workdir, "status", "--porcelain", "-z", "--untracked-files=normal").Output()
	if err != nil {
		return nil, fmt.Errorf("git status --porcelain: %w", err)
	}
	if len(out) > 0 {
		records := strings.Split(string(out), "\x00")
		for i := 0; i < len(records); i++ {
			line := records[i]
			if len(line) < 4 {
				continue
			}
			statusCode := strings.TrimSpace(line[:2])
			fp := line[3:]
			// Renames/copies carry the original path as the next record.
			if line[0] == 'R' || line[0] == 'C' || line[1] == 'R' || line[1] == 'C' {
				i++
			}
			if statusCode == "??" {
				if _, exists := fileMap[fp]; !exists {
					fullPath := filepath.Join(workdir, fp)
					info, err := os.Stat(fullPath)
					if err != nil {
						continue
					}
					if info.ModTime().Unix() < int64(baseTS) {
						continue
					}
					// Reading each new file to count its lines is the only
					// per-file I/O in this loop, so it is budgeted: a working
					// tree with this many new files is not one anybody reads
					// per-file counts off.
					adds := 0
					if lineCounts < gitutil.MaxNewFileStatFiles {
						adds = gitutil.NewFileLineCount(fullPath)
						lineCounts++
					}
					fileMap[fp] = store.ChangedFile{
						Filepath:  fp,
						Additions: adds,
						Deletions: 0,
						Status:    "??",
					}
				}
			} else if _, exists := fileMap[fp]; exists {
				f := fileMap[fp]
				f.Status = statusCode
				fileMap[fp] = f
			}
		}
	}

	logger.Debug("git status --porcelain", "duration_ms", time.Since(t3).Milliseconds())

	files := make([]store.ChangedFile, 0, len(fileMap))
	for _, f := range fileMap {
		files = append(files, f)
	}

	if len(files) > 5000 {
		logger.Warn("large changed file set", "file_count", len(files), "base", base)
	}

	return files, nil
}

func getBaseTimestamp(ctx context.Context, workdir, baseRef string) float64 {
	out, err := executil.Command(ctx, "git", "--no-optional-locks", "-C", workdir, "log", "-1", "--format=%ct", baseRef).Output()
	if err != nil {
		return 0
	}
	ts, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return ts
}

// lookupPR returns the PR number for the given remote+branch, using a cache
// keyed by remote_url::branch. The cache TTL matches the poller interval so
// each branch is looked up at most once per poll cycle.
func (p *GitPoller) lookupPR(ctx context.Context, remoteURL, branch, workdir string) int {
	cacheKey := remoteURL + "::" + branch
	if entry, ok := p.prCache[cacheKey]; ok && time.Now().Before(entry.expiresAt) {
		return entry.prNumber
	}

	prNumber := queryPRNumber(ctx, branch, workdir, p.logger)
	p.prCache[cacheKey] = prCacheEntry{
		prNumber:  prNumber,
		expiresAt: time.Now().Add(p.interval),
	}
	return prNumber
}

// queryPRNumber shells out to `gh pr list` to find an open or merged PR for a branch.
func queryPRNumber(ctx context.Context, branch, workdir string, logger *slog.Logger) int {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := executil.Command(ctx, "gh", "pr", "list",
		"--head", branch,
		"--state", "all",
		"--json", "number",
		"--limit", "1",
		"--repo", getRepoFromWorkdir(ctx, workdir),
	).Output()
	if err != nil {
		logger.Debug("gh pr list failed", "branch", branch, "error", err)
		return 0
	}

	// Parse minimal JSON: [{"number":42}]
	s := strings.TrimSpace(string(out))
	if s == "" || s == "[]" || s == "null" {
		return 0
	}
	// Quick parse without importing encoding/json for a single int field
	idx := strings.Index(s, `"number":`)
	if idx < 0 {
		return 0
	}
	numStr := s[idx+len(`"number":`):]
	numStr = strings.TrimLeft(numStr, " ")
	end := strings.IndexAny(numStr, ",}")
	if end > 0 {
		numStr = numStr[:end]
	}
	n, _ := strconv.Atoi(strings.TrimSpace(numStr))
	return n
}

func getRepoFromWorkdir(ctx context.Context, workdir string) string {
	out, err := executil.Command(ctx, "git", "--no-optional-locks", "-C", workdir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	remote := strings.TrimSpace(string(out))

	// Convert git@github.com:org/repo.git or https://github.com/org/repo.git to org/repo
	remote = strings.TrimSuffix(remote, ".git")
	if strings.HasPrefix(remote, "git@") {
		// git@github.com:org/repo
		parts := strings.SplitN(remote, ":", 2)
		if len(parts) == 2 {
			return parts[1]
		}
	}
	// https://github.com/org/repo
	if idx := strings.Index(remote, "github.com/"); idx >= 0 {
		return remote[idx+len("github.com/"):]
	}
	return remote
}

// queryWorktreeState fills in whether workdir is a linked worktree, how far
// its branch has diverged from the default branch, and how many paths are
// uncommitted. Every probe is best-effort: a failure leaves the zero value.
func queryWorktreeState(ctx context.Context, workdir string, info *gitInfo) {
	run := func(args ...string) (string, bool) {
		out, err := executil.Command(ctx, "git", append([]string{"--no-optional-locks", "-C", workdir}, args...)...).Output()
		if err != nil {
			return "", false
		}
		return strings.TrimSpace(string(out)), true
	}

	// In a linked worktree the per-worktree git dir differs from the common one.
	if out, ok := run("rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir"); ok {
		if lines := strings.Split(out, "\n"); len(lines) == 2 {
			info.Worktree = filepath.Clean(lines[0]) != filepath.Clean(lines[1])
		}
	}

	if out, ok := run("status", "--porcelain"); ok && out != "" {
		info.DirtyCount = len(strings.Split(out, "\n"))
	}

	if info.Branch == "" || info.Branch == "HEAD" {
		return
	}
	for _, base := range []string{"main", "master"} {
		if _, ok := run("rev-parse", "--verify", "--quiet", base); !ok {
			continue
		}
		info.BaseBranch = base
		if info.Branch == base {
			return
		}
		if out, ok := run("rev-list", "--left-right", "--count", base+"...HEAD"); ok {
			var behind, ahead int
			if _, err := fmt.Sscanf(out, "%d\t%d", &behind, &ahead); err == nil {
				info.Behind, info.Ahead = behind, ahead
			}
		}
		return
	}
}
