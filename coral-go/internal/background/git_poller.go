// Package background provides long-running background services for Coral.
package background

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/subgentic/coral/internal/logsafe"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/executil"
	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/safefs"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
)

// MinGitPollInterval is the floor for a user-configured poll interval. Git
// queries on a large repository are not cheap, so a too-eager setting would
// spend more time scanning than working.
const MinGitPollInterval = 15 * time.Second

// GitPollIntervalSettingKey is the user setting for the poll cadence, in
// seconds; "0" turns polling off.
const GitPollIntervalSettingKey = setreg.GitPollIntervalS

// GitPollInterval resolves the configured git poll cadence from settings. An
// unset or unparseable value falls back to defaultSeconds; 0 or less means
// polling is off.
func GitPollInterval(settings map[string]string, defaultSeconds int) time.Duration {
	return time.Duration(setreg.IntOr(settings, GitPollIntervalSettingKey, int64(defaultSeconds))) * time.Second
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
	// written remembers the changed-file list last written per session (or
	// agent), so an unchanged list only has its recorded_at refreshed.
	written map[string]writtenFiles

	// intervalFn, when set, is consulted before every wait so a settings
	// change takes effect without a restart. A non-positive result pauses
	// polling entirely, for repositories where scanning is too expensive to
	// do on a timer at all.
	intervalFn func() time.Duration

	// failing holds the folders whose git query failed (most often not a repository: the
	// soak's folders logged a warning every 2 minutes, #4098), and when to try each again.
	// PollOnce runs on the poller's own goroutine only.
	failing map[string]time.Time
	now     func() time.Time
}

// gitFailureBackoff is how long a folder whose git query failed is left alone before it is
// checked again (it may have become a repository since).
const gitFailureBackoff = 30 * time.Minute

// prCacheTTL is how long a found PR number is reused. A branch's PR number
// rarely changes, and gh is the slowest call in a pass.
const prCacheTTL = 10 * time.Minute

// prCacheKeysMax is the size past which lookupPR sweeps expired entries out of the cache.
const prCacheKeysMax = 256

// prCacheExpiry keeps a found PR number for prCacheTTL, and a "no PR" answer
// for one poll interval as before, so a newly opened PR shows up promptly.
func prCacheExpiry(prNumber int, interval time.Duration) time.Duration {
	if prNumber == 0 {
		return interval
	}
	return max(interval, prCacheTTL)
}

// maxChangedFiles caps the list stored per agent on large monorepos.
const maxChangedFiles = 2000

type writtenFiles struct {
	hash  [sha256.Size]byte
	stamp string
	count int
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
		written:  make(map[string]writtenFiles),
		failing:  make(map[string]time.Time),
		now:      time.Now,
	}
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
	dirToAgents := p.groupByGitRoot(ctx, agents)
	p.logger.Debug("git poll starting", "dirs", len(dirToAgents), "agents", len(agents))
	seen := make(map[string]bool, len(agents))

	// A folder that is no longer anyone's working directory is forgotten.
	for workdir := range p.failing {
		if _, live := dirToAgents[workdir]; !live {
			delete(p.failing, workdir)
		}
	}
	for workdir, dirAgents := range dirToAgents {
		p.pollDir(ctx, workdir, dirAgents, seen)
	}

	// Forget agents that are gone, so the map stays bounded.
	for key := range p.written {
		if !seen[key] {
			delete(p.written, key)
		}
	}

	totalDur := time.Since(pollStart)
	p.logger.Debug("git poll complete", "dirs", len(dirToAgents), "total_ms", totalDur.Milliseconds())
	if totalDur > 5*time.Second {
		p.logger.Warn("git poll slow", "total_ms", totalDur.Milliseconds(), "dirs", len(dirToAgents))
	}
	return nil
}

// groupByGitRoot groups the agents by their working directory's git root, so agents in different subdirectories of
// one repository share a single scan (they used to be scanned once per working directory, with identical results).
func (p *GitPoller) groupByGitRoot(ctx context.Context, agents []AgentInfo) map[string][]AgentInfo {
	rootOf := make(map[string]string)
	dirToAgents := make(map[string][]AgentInfo)
	for _, agent := range agents {
		if agent.WorkingDirectory == "" {
			continue
		}
		root, ok := rootOf[agent.WorkingDirectory]
		if !ok {
			resolveStart := time.Now()
			root = gitutil.ResolveGitRoot(ctx, agent.WorkingDirectory)
			p.logger.Debug("git resolve root", "workdir", logsafe.Dir(agent.WorkingDirectory), "root", logsafe.Dir(root), "duration_ms", time.Since(resolveStart).Milliseconds())
			rootOf[agent.WorkingDirectory] = root
		}
		dirToAgents[root] = append(dirToAgents[root], agent)
	}
	return dirToAgents
}

// pollDir scans one repository and writes each of its agents' git snapshot and changed files, noting them in seen.
// A folder whose git query fails is warned about once and checked again every gitFailureBackoff; the changed files
// are capped at maxChangedFiles, and a failed list is written empty (the last one would otherwise show as current).
func (p *GitPoller) pollDir(ctx context.Context, workdir string, dirAgents []AgentInfo, seen map[string]bool) {
	dirStart := time.Now()
	retryAt, failed := p.failing[workdir]
	if failed && p.now().Before(retryAt) {
		return
	}
	queryStart := time.Now()
	gitInfo, err := queryGit(ctx, workdir)
	queryDur := time.Since(queryStart)
	if err != nil {
		if !failed {
			p.logger.Warn("git query failed (not a git repository?); checking this folder again every 30 minutes", "workdir", logsafe.Dir(workdir), "duration_ms", queryDur.Milliseconds(), "error", err)
		} else {
			p.logger.Debug("git query still failing", "workdir", logsafe.Dir(workdir), "error", err)
		}
		p.failing[workdir] = p.now().Add(gitFailureBackoff)
		return
	}
	if failed {
		delete(p.failing, workdir)
		p.logger.Info("git query works again", "workdir", logsafe.Dir(workdir))
	}
	if gitInfo == nil {
		return
	}
	p.logger.Debug("git query", "workdir", logsafe.Dir(workdir), "branch", gitInfo.Branch, "duration_ms", queryDur.Milliseconds())

	filesStart := time.Now()
	changedFiles, err := queryChangedFilesFrom(ctx, workdir, gitInfo.status, gitInfo.statusErr)
	filesDur := time.Since(filesStart)
	if err != nil {
		p.logger.Warn("git changed files query failed; clearing cached list", "workdir", logsafe.Dir(workdir), "duration_ms", filesDur.Milliseconds(), "error", err)
		changedFiles = []store.ChangedFile{}
	} else {
		p.logger.Debug("git changed files", "workdir", logsafe.Dir(workdir), "file_count", len(changedFiles), "duration_ms", filesDur.Milliseconds())
	}
	// Cap file count to prevent DB thrashing on large monorepos
	if len(changedFiles) > maxChangedFiles {
		p.logger.Warn("capping changed files", "workdir", logsafe.Dir(workdir), "agents", len(dirAgents), "total", len(changedFiles), "cap", maxChangedFiles)
		changedFiles = changedFiles[:maxChangedFiles]
	}
	filesHash := hashChangedFiles(workdir, changedFiles)

	// Look up PR number for non-default branches
	prNumber := 0
	if gitInfo.RemoteURL != nil && gitInfo.Branch != "main" && gitInfo.Branch != "master" && gitInfo.Branch != "HEAD" {
		prNumber = p.lookupPR(ctx, *gitInfo.RemoteURL, gitInfo.Branch, workdir)
	}

	dbStart := time.Now()
	for _, agent := range dirAgents {
		snap := snapshotFor(agent, workdir, gitInfo, prNumber)
		if err := p.store.UpsertGitSnapshot(ctx, snap); err != nil {
			p.logger.Warn("upsert snapshot failed", "agent", agent.AgentName, "error", err)
		}
		key := writtenKey(agent.AgentName, snap.SessionID)
		seen[key] = true
		p.writeChangedFiles(ctx, key, agent.AgentName, workdir, changedFiles, snap.SessionID, filesHash)
	}
	p.logger.Debug("git db writes", "workdir", logsafe.Dir(workdir), "agents", len(dirAgents), "duration_ms", time.Since(dbStart).Milliseconds())

	if dirDur := time.Since(dirStart); dirDur > 2*time.Second {
		p.logger.Warn("git poll slow for directory", "workdir", logsafe.Dir(workdir), "total_ms", dirDur.Milliseconds(),
			"query_ms", queryDur.Milliseconds(), "files_ms", filesDur.Milliseconds())
	}
}

// snapshotFor is an agent's git snapshot of its repository (a nil session id for an agent without one).
func snapshotFor(agent AgentInfo, workdir string, g *gitInfo, prNumber int) *store.GitSnapshot {
	var sidPtr *string
	if agent.SessionID != "" {
		sessionID := agent.SessionID
		sidPtr = &sessionID
	}
	return &store.GitSnapshot{
		AgentName:        agent.AgentName,
		AgentType:        agent.AgentType,
		WorkingDirectory: workdir,
		Branch:           g.Branch,
		CommitHash:       g.CommitHash,
		CommitSubject:    g.CommitSubject,
		CommitTimestamp:  &g.CommitTimestamp,
		SessionID:        sidPtr,
		RemoteURL:        g.RemoteURL,
		PRNumber:         prNumber,
		Worktree:         g.Worktree,
		BaseBranch:       g.BaseBranch,
		Ahead:            g.Ahead,
		Behind:           g.Behind,
		DirtyCount:       g.DirtyCount,
	}
}

func writtenKey(agentName string, sessionID *string) string {
	if sessionID != nil {
		return "s\x00" + *sessionID
	}
	return "a\x00" + agentName
}

// hashChangedFiles fingerprints a changed-file list as it would be stored.
func hashChangedFiles(workdir string, files []store.ChangedFile) [sha256.Size]byte {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00", workdir, len(files))
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00%s\x00", f.Filepath, f.Additions, f.Deletions, f.Status)
	}
	var sum [sha256.Size]byte
	h.Sum(sum[:0])
	return sum
}

// writeChangedFiles stores an agent's changed-file list. When it is the list
// this poller wrote last time and the cache still holds exactly that write,
// only its recorded_at is refreshed; otherwise the list is replaced.
func (p *GitPoller) writeChangedFiles(ctx context.Context, key, agentName, workdir string, files []store.ChangedFile, sessionID *string, hash [sha256.Size]byte) {
	if prev, ok := p.written[key]; ok && prev.hash == hash {
		stamp, touched, err := p.store.TouchChangedFiles(ctx, agentName, sessionID, "branch_point", prev.stamp, prev.count)
		if err != nil {
			p.logger.Warn("refresh changed files failed", "agent", agentName, "error", err)
		}
		if touched {
			p.written[key] = writtenFiles{hash: hash, stamp: stamp, count: prev.count}
			return
		}
	}
	stamp, err := p.store.ReplaceChangedFilesStamped(ctx, agentName, workdir, files, sessionID, "branch_point")
	if err != nil {
		delete(p.written, key)
		p.logger.Warn("replace changed files failed", "agent", agentName, "error", err)
		return
	}
	p.written[key] = writtenFiles{hash: hash, stamp: stamp, count: len(files)}
	NotifyFilesChanged(workdir) // a different list: the folder's files changed (#3721)
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
	CommitTimestamp string
	RemoteURL       *string
	Worktree        bool
	BaseBranch      string
	Ahead           int
	Behind          int
	DirtyCount      int

	// status is the pass's one 'git status --porcelain -z' (#3730), shared by the dirty count
	// and the changed files; statusErr is why it failed.
	status    []byte
	statusErr error
}

func queryGit(ctx context.Context, workdir string) (*gitInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	logger := slog.Default().With("service", "git_poller", "workdir", workdir)

	// Get branch name
	t0 := time.Now()
	out, err := gitutil.Command(ctx, workdir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	logger.Debug("git rev-parse", "duration_ms", time.Since(t0).Milliseconds())
	if err != nil {
		return nil, err
	}
	branch := strings.TrimSpace(string(out))

	// Get latest commit
	t1 := time.Now()
	out, err = gitutil.Command(ctx, workdir, "log", "-1", "--format=%H|%s|%aI").Output()
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
	out, err = gitutil.Command(ctx, workdir, "remote", "get-url", "origin").Output()
	logger.Debug("git remote", "duration_ms", time.Since(t2).Milliseconds())
	if err == nil {
		// Never record a credential embedded in the remote URL (#3558).
		s := gitutil.StripURLCredentials(strings.TrimSpace(string(out)))
		if s != "" {
			remoteURL = &s
		}
	}

	info := &gitInfo{
		Branch:          branch,
		CommitHash:      parts[0],
		CommitSubject:   parts[1],
		CommitTimestamp: parts[2],
		RemoteURL:       remoteURL,
	}
	queryWorktreeState(ctx, workdir, info)
	return info, nil
}

// statusEntry is one path in 'git status --porcelain -z' output.
type statusEntry struct {
	code string // the two status letters, trimmed ("M", "??", "R")
	path string
}

// statusEntries parses 'git status --porcelain -z': one record per path, a rename or copy
// followed by a record holding its old path, which is skipped.
func statusEntries(out []byte) []statusEntry {
	if len(out) == 0 {
		return nil
	}
	records := strings.Split(string(out), "\x00")
	var entries []statusEntry
	for i := 0; i < len(records); i++ {
		line := records[i]
		if len(line) < 4 {
			continue
		}
		// Renames/copies carry the original path as the next record.
		if line[0] == 'R' || line[0] == 'C' || line[1] == 'R' || line[1] == 'C' {
			i++
		}
		entries = append(entries, statusEntry{code: strings.TrimSpace(line[:2]), path: line[3:]})
	}
	return entries
}

// queryChangedFiles runs its own status; the poller passes the one queryGit ran instead
// (queryChangedFilesFrom).
func queryChangedFiles(ctx context.Context, workdir string) ([]store.ChangedFile, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := gitutil.Command(ctx, workdir, "status", "--porcelain", "-z", "--untracked-files=normal").Output()
	return queryChangedFilesFrom(ctx, workdir, out, err)
}

// queryChangedFilesFrom lists the files changed since the diff base, with status (the output
// of 'git status --porcelain -z --untracked-files=normal', or statusErr) for the untracked ones.
func queryChangedFilesFrom(ctx context.Context, workdir string, status []byte, statusErr error) ([]store.ChangedFile, error) {
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
	out, err := gitutil.Command(ctx, workdir, "diff", base, "--numstat", "-z").Output()
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
	if statusErr != nil {
		return nil, fmt.Errorf("git status --porcelain: %w", statusErr)
	}
	if entries := statusEntries(status); len(entries) > 0 {
		// New files' line counts are read inside the working tree, held open once per pass.
		countRoot, rootErr := safefs.OpenRoot(workdir)
		if rootErr != nil {
			countRoot = nil
		} else {
			defer countRoot.Close()
		}
		for _, e := range entries {
			statusCode, fp := e.code, e.path
			if statusCode == "??" {
				if _, exists := fileMap[fp]; !exists {
					fullPath := filepath.Join(workdir, fp)
					// Lstat: an untracked symlink is never followed (to a FIFO, /dev/zero or a
					// network mount; #3597, weak site W18).
					info, err := os.Lstat(fullPath)
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
					if lineCounts < gitutil.MaxNewFileStatFiles && countRoot != nil {
						adds = gitutil.NewFileLineCountIn(countRoot, fp)
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
	out, err := gitutil.Command(ctx, workdir, "log", "-1", "--format=%ct", baseRef).Output()
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
	p.sweepPRCache(time.Now())
	p.prCache[cacheKey] = prCacheEntry{
		prNumber:  prNumber,
		expiresAt: time.Now().Add(prCacheExpiry(prNumber, p.interval)),
	}
	return prNumber
}

// sweepPRCache drops expired entries once the cache is large: an expired entry is never read
// again, and the keys are remote and branch names, so they pile up as branches come and go (#3999).
func (p *GitPoller) sweepPRCache(now time.Time) {
	if len(p.prCache) < prCacheKeysMax {
		return
	}
	for k, e := range p.prCache {
		if !now.Before(e.expiresAt) {
			delete(p.prCache, k)
		}
	}
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
	out, err := gitutil.Command(ctx, workdir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	remote := gitutil.StripURLCredentials(strings.TrimSpace(string(out)))

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
		out, err := gitutil.Command(ctx, workdir, args...).Output()
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

	// One status for the pass (#3730): the changed files read it too (queryChangedFilesFrom).
	info.status, info.statusErr = gitutil.Command(ctx, workdir, "status", "--porcelain", "-z", "--untracked-files=normal").Output()
	if info.statusErr == nil {
		info.DirtyCount = len(statusEntries(info.status))
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
