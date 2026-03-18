// Package background provides long-running background services for Coral.
package background

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/tmux"
)

// GitPoller periodically polls git branch/commit info for live agents.
type GitPoller struct {
	store     *store.GitStore
	tmux      *tmux.Client
	interval  time.Duration
	logger    *slog.Logger
	discoverFn func(ctx context.Context) ([]AgentInfo, error)
}

// AgentInfo holds minimal agent metadata needed by background services.
type AgentInfo struct {
	AgentName        string
	AgentType        string
	SessionID        string
	WorkingDirectory string
}

// NewGitPoller creates a new GitPoller.
func NewGitPoller(gitStore *store.GitStore, tmuxClient *tmux.Client, interval time.Duration) *GitPoller {
	return &GitPoller{
		store:    gitStore,
		tmux:     tmuxClient,
		interval: interval,
		logger:   slog.Default().With("service", "git_poller"),
	}
}

// SetDiscoverFn sets a custom agent discovery function (for dependency injection).
func (p *GitPoller) SetDiscoverFn(fn func(ctx context.Context) ([]AgentInfo, error)) {
	p.discoverFn = fn
}

// Run starts the polling loop. Blocks until context is cancelled.
func (p *GitPoller) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := p.PollOnce(ctx); err != nil {
				p.logger.Error("poll error", "error", err)
			}
		}
	}
}

// PollOnce performs a single git polling pass for all live agents.
func (p *GitPoller) PollOnce(ctx context.Context) error {
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

	for workdir, dirAgents := range dirToAgents {
		gitInfo, err := queryGit(ctx, workdir)
		if err != nil || gitInfo == nil {
			continue
		}
		changedFiles, err := queryChangedFiles(ctx, workdir)
		if err != nil {
			changedFiles = nil
		}

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
			}
			if err := p.store.UpsertGitSnapshot(ctx, snap); err != nil {
				p.logger.Warn("upsert snapshot failed", "agent", agent.AgentName, "error", err)
			}

			if changedFiles != nil {
				if err := p.store.ReplaceChangedFiles(ctx, agent.AgentName, workdir, changedFiles, sidPtr); err != nil {
					p.logger.Warn("replace changed files failed", "agent", agent.AgentName, "error", err)
				}
			}
		}
	}

	return nil
}

func (p *GitPoller) discoverAgents(ctx context.Context) ([]AgentInfo, error) {
	if p.discoverFn != nil {
		return p.discoverFn(ctx)
	}
	// Default: discover from tmux panes
	return DiscoverAgentsFromTmux(ctx, p.tmux)
}

// DiscoverAgentsFromTmux finds coral agents by scanning tmux sessions.
func DiscoverAgentsFromTmux(ctx context.Context, tmuxClient *tmux.Client) ([]AgentInfo, error) {
	panes, err := tmuxClient.ListPanes(ctx)
	if err != nil {
		return nil, err
	}

	var agents []AgentInfo
	seen := make(map[string]bool)

	for _, pane := range panes {
		// Parse session name: {agent_type}-{uuid}
		parts := strings.SplitN(pane.SessionName, "-", 2)
		if len(parts) != 2 || len(parts[1]) < 36 {
			continue
		}
		// Quick UUID format check
		sessionID := strings.ToLower(parts[1])
		if len(sessionID) != 36 || sessionID[8] != '-' || sessionID[13] != '-' {
			continue
		}
		if seen[sessionID] {
			continue
		}
		seen[sessionID] = true

		agentName := filepath.Base(strings.TrimRight(pane.CurrentPath, "/"))
		if agentName == "" {
			agentName = sessionID[:8]
		}

		agents = append(agents, AgentInfo{
			AgentName:        agentName,
			AgentType:        parts[0],
			SessionID:        sessionID,
			WorkingDirectory: pane.CurrentPath,
		})
	}
	return agents, nil
}

// gitInfo holds parsed git query results.
type gitInfo struct {
	Branch          string
	CommitHash      string
	CommitSubject   string
	CommitTimestamp  string
	RemoteURL       *string
}

func queryGit(ctx context.Context, workdir string) (*gitInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Get branch name
	out, err := exec.CommandContext(ctx, "git", "-C", workdir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return nil, err
	}
	branch := strings.TrimSpace(string(out))

	// Get latest commit
	out, err = exec.CommandContext(ctx, "git", "-C", workdir, "log", "-1", "--format=%H|%s|%aI").Output()
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("unexpected git log format")
	}

	// Get remote URL (best-effort)
	var remoteURL *string
	out, err = exec.CommandContext(ctx, "git", "-C", workdir, "remote", "get-url", "origin").Output()
	if err == nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			remoteURL = &s
		}
	}

	return &gitInfo{
		Branch:         branch,
		CommitHash:     parts[0],
		CommitSubject:  parts[1],
		CommitTimestamp: parts[2],
		RemoteURL:      remoteURL,
	}, nil
}

func queryChangedFiles(ctx context.Context, workdir string) ([]store.ChangedFile, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	base, err := getDiffBase(ctx, workdir)
	if err != nil {
		base = "HEAD"
	}
	baseTS := getBaseTimestamp(ctx, workdir, base)

	fileMap := make(map[string]store.ChangedFile)

	// git diff base --numstat
	out, err := exec.CommandContext(ctx, "git", "-C", workdir, "diff", base, "--numstat").Output()
	if err == nil && len(out) > 0 {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) < 3 {
				continue
			}
			a, _ := strconv.Atoi(parts[0])
			d, _ := strconv.Atoi(parts[1])
			fileMap[parts[2]] = store.ChangedFile{
				Filepath:  parts[2],
				Additions: a,
				Deletions: d,
				Status:    "M",
			}
		}
	}

	// git status --porcelain for untracked files
	out, err = exec.CommandContext(ctx, "git", "-C", workdir, "status", "--porcelain", "--untracked-files=all").Output()
	if err == nil && len(out) > 0 {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if len(line) < 4 {
				continue
			}
			statusCode := strings.TrimSpace(line[:2])
			fp := line[3:]
			if idx := strings.Index(fp, " -> "); idx >= 0 {
				fp = fp[idx+4:]
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
					fileMap[fp] = store.ChangedFile{
						Filepath:  fp,
						Additions: 0,
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

	files := make([]store.ChangedFile, 0, len(fileMap))
	for _, f := range fileMap {
		files = append(files, f)
	}
	return files, nil
}

func getDiffBase(ctx context.Context, workdir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", workdir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "HEAD", err
	}
	branch := strings.TrimSpace(string(out))
	if branch == "main" || branch == "master" || branch == "HEAD" || branch == "" {
		return "HEAD", nil
	}

	for _, baseBranch := range []string{"main", "master"} {
		out, err = exec.CommandContext(ctx, "git", "-C", workdir, "merge-base", baseBranch, "HEAD").Output()
		if err == nil && len(out) > 0 {
			return strings.TrimSpace(string(out)), nil
		}
	}
	return "HEAD", nil
}

func getBaseTimestamp(ctx context.Context, workdir, baseRef string) float64 {
	out, err := exec.CommandContext(ctx, "git", "-C", workdir, "log", "-1", "--format=%ct", baseRef).Output()
	if err != nil {
		return 0
	}
	ts, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return ts
}
