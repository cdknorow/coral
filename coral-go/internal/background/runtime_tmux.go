package background

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/terminalinput"
	"github.com/subgentic/coral/internal/tmux"
)

// TmuxRuntime implements AgentRuntime using tmux sessions.
type TmuxRuntime struct {
	client *tmux.Client
}

// NewTmuxRuntime creates a TmuxRuntime wrapping the given tmux client.
func NewTmuxRuntime(client *tmux.Client) *TmuxRuntime {
	return &TmuxRuntime{client: client}
}

// TagsRuns says agents started on this runtime carry a CORAL_PTY_RUN tag, recorded in live_sessions.run_tag, so a Kill finds the processes they detached (#6141, #6770).
func (r *TmuxRuntime) TagsRuns() bool { return true }

// Client returns the underlying tmux client for cases that need direct access.
func (r *TmuxRuntime) Client() *tmux.Client {
	return r.client
}

func (r *TmuxRuntime) SpawnAgent(ctx context.Context, name, workDir, logFile, command string) error {
	ptymanager.DropHeld(name, "spawned") // nothing held for the old process is typed into a new one (#3431)
	if err := r.client.NewSession(ctx, name, workDir); err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}

	// Set CORAL_SESSION_NAME so coral-board CLI can identify this session
	r.client.SetEnvironment(ctx, name, "CORAL_SESSION_NAME", name)

	if err := r.client.PipePane(ctx, name, logFile); err != nil {
		return fmt.Errorf("tmux pipe-pane: %w", err)
	}

	// Set pane title
	target := name + ".0"
	parts := strings.SplitN(name, "-", 2)
	agentType := name
	if len(parts) >= 1 {
		agentType = parts[0]
	}
	folderName := filepath.Base(strings.TrimRight(workDir, "/"))
	r.client.SetPaneTitle(ctx, target, fmt.Sprintf("%s — %s", folderName, agentType))

	if command != "" {
		log.Printf("[launch] session=%s agent=%s cmd=%s", name, agentType, command)
		if err := r.client.SendKeysToTarget(ctx, terminalinput.Internal, target, command); err != nil {
			log.Printf("[launch] session=%s: the start command could not be typed into its terminal: %v", name, err)
			_ = r.client.KillSessionOnly(ctx, name, agentType, "") // not left at an idle shell, "running" until its deadline (#6978)
			return ptymanager.ErrStartCommand
		}
	}

	return nil
}

func (r *TmuxRuntime) SendInput(ctx context.Context, o terminalinput.Origin, name, text string) error {
	// Parse agent type and session ID from name format "{type}-{uuid}"
	agentType, sessionID := parseSessionName(name)
	agentName := name
	return r.client.SendKeys(ctx, o, agentName, text, agentType, sessionID)
}

// SendRawInput implements ptymanager.RawInputSender: single keys typed into the agent's pane. The gate dismisses Claude's session survey with a 0 through it, whatever the terminal a send was handed over with (#6974).
func (r *TmuxRuntime) SendRawInput(ctx context.Context, o terminalinput.Origin, name string, keys []string, agentType, sessionID string) error {
	return r.client.SendRawKeys(ctx, o, name, keys, agentType, sessionID)
}

func (r *TmuxRuntime) KillAgent(ctx context.Context, name string) error {
	ptymanager.DropHeld(name, "killed") // nothing held for the old process is typed into a new one (#3431)
	_, err := r.client.DisplayMessage(ctx, name, "")
	if err != nil {
		// Session doesn't exist, nothing to kill
		return nil
	}
	agentType, sessionID := parseSessionName(name)
	return r.client.KillSession(ctx, name, agentType, sessionID)
}

func (r *TmuxRuntime) IsAlive(ctx context.Context, name string) bool {
	return r.client.HasSession(ctx, name)
}

func (r *TmuxRuntime) ListAgents(ctx context.Context) ([]AgentInfo, error) {
	return DiscoverAgentsFromTmux(ctx, r.client)
}

// parseSessionName splits a "{type}-{uuid}" session name into its parts.
func parseSessionName(name string) (agentType, sessionID string) {
	parts := strings.SplitN(name, "-", 2)
	if len(parts) == 2 && len(parts[1]) >= 36 {
		return parts[0], parts[1]
	}
	return "", ""
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

// AgentShellIdle reports whether an agent's pane is back at its idle shell, for
// ptymanager.AgentExited (#3219).
func (r *TmuxRuntime) AgentShellIdle(ctx context.Context, name, agentType, sessionID string) (bool, string, bool) {
	if agentType == "" && sessionID == "" {
		agentType, sessionID = parseSessionName(name)
	}
	idle, shell, err := r.client.AgentShellIdle(ctx, name, agentType, sessionID)
	if err != nil {
		return false, "", false
	}
	return idle, shell, true
}

// AgentScreen implements ptymanager.ScreenReader: the bottom of the agent's pane,
// read by the automated-input gate before typing (#3431).
func (r *TmuxRuntime) AgentScreen(ctx context.Context, name, agentType, sessionID string) (string, error) {
	if agentType == "" && sessionID == "" {
		agentType, sessionID = parseSessionName(name)
	}
	return r.client.CapturePane(ctx, name, 40, agentType, sessionID)
}

// AgentScreenStyled implements ptymanager.StyledScreenReader: AgentScreen with each
// cell's style, so the gate can tell Claude's faint suggestion from a draft (#3468).
func (r *TmuxRuntime) AgentScreenStyled(ctx context.Context, name, agentType, sessionID string) (string, error) {
	if agentType == "" && sessionID == "" {
		agentType, sessionID = parseSessionName(name)
	}
	return r.client.CapturePaneStyled(ctx, name, 40, agentType, sessionID)
}

// SendPrompt implements ptymanager.PromptSender: automated text as one bracketed
// paste and its Enter (#3457).
func (r *TmuxRuntime) SendPrompt(ctx context.Context, o terminalinput.Origin, name, text, agentType, sessionID string) error {
	if agentType == "" && sessionID == "" {
		agentType, sessionID = parseSessionName(name)
	}
	return r.client.SendPrompt(ctx, o, name, text, agentType, sessionID)
}

// RenderedScreen implements ptymanager.RenderedScreen: AgentScreen is the pane as drawn.
func (r *TmuxRuntime) HasRenderedScreen() bool { return true }

// SendEnter implements ptymanager.EnterSender: the Enter a send withheld because
// a prompt opened after its text (#3431).
func (r *TmuxRuntime) SendEnter(ctx context.Context, name, agentType, sessionID string) error {
	if agentType == "" && sessionID == "" {
		agentType, sessionID = parseSessionName(name)
	}
	return r.client.SendRawKeys(ctx, terminalinput.Internal, name, []string{"Enter"}, agentType, sessionID)
}
