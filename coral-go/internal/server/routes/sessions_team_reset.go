// Resetting a team (moved out of sessions_launch.go, #5003; no behaviour change).

package routes

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentmodels"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/store"
)

// ResetTeam kills all agents on a board and re-launches them with their
// original prompts and configuration. Each agent gets a fresh context.
// POST /api/sessions/live/team/{boardName}/reset
func (h *SessionsHandler) ResetTeam(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	boardName := urlParam(r, "boardName")
	if boardName == "" {
		errBadRequest(w, "boardName is required")
		return
	}

	ctx := r.Context()
	bgCtx := context.Background()

	// Get all sessions on this board
	sessions, err := h.ss.GetBoardSessions(ctx, boardName)
	if err != nil {
		errInternalServer(w, "failed to get board sessions: "+err.Error())
		return
	}
	if len(sessions) == 0 {
		errBadRequest(w, "no agents found on board: "+boardName)
		return
	}

	// Save each agent's config before killing
	type agentConfig struct {
		DisplayName    *string
		WorkingDir     string
		AgentType      string
		Flags          *string
		Prompt         *string
		BoardServer    *string
		BoardType      *string
		Icon           *string
		Capabilities   *string
		Model          *string
		Thinking       *string
		Tools          *string
		PermissionMode *string
	}
	configs := make([]agentConfig, 0, len(sessions))
	for _, s := range sessions {
		dn := s.DisplayName
		// Fallback: if display_name is nil in live_sessions, check session_meta
		if dn == nil {
			if metaName, err := h.ss.GetDisplayName(ctx, s.SessionID); err == nil && metaName != nil {
				dn = metaName
			}
		}
		configs = append(configs, agentConfig{
			DisplayName:    dn,
			WorkingDir:     s.WorkingDir,
			AgentType:      s.AgentType,
			Flags:          s.Flags,
			Prompt:         s.Prompt,
			BoardServer:    s.BoardServer,
			BoardType:      s.BoardType,
			Icon:           s.Icon,
			Capabilities:   s.Capabilities,
			Model:          s.Model,
			Thinking:       s.Thinking,
			Tools:          s.Tools,
			PermissionMode: s.PermissionMode,
		})
	}

	// Kill all agents and unsubscribe from board
	for _, s := range sessions {
		h.chatMeta.forget(s.SessionID)
		h.ss.UnregisterLiveSession(bgCtx, s.SessionID)
		h.terminal.KillSession(bgCtx, s.AgentName, s.AgentType, s.SessionID)
		removeBoardStateFile(s.AgentName, h.cfg)
		// Mark subscriber as inactive during reset; re-launch will reactivate.
		if h.bs != nil && s.DisplayName != nil && *s.DisplayName != "" {
			h.bs.Unsubscribe(bgCtx, boardName, *s.DisplayName)
		}
	}

	// Clear board pause state
	if h.boardHandler != nil {
		h.boardHandler.SetPaused(boardName, false)
	}

	// Brief pause to let tmux sessions clean up
	time.Sleep(500 * time.Millisecond)

	// Re-launch each agent with original config
	var launched []map[string]any
	var relaunched []string // the names the agents were launched under
	for _, cfg := range configs {
		var flags []string
		if cfg.Flags != nil && *cfg.Flags != "" {
			json.Unmarshal([]byte(*cfg.Flags), &flags)
		}
		prompt := ""
		if cfg.Prompt != nil {
			prompt = *cfg.Prompt
		}
		boardServer := ""
		if cfg.BoardServer != nil {
			boardServer = *cfg.BoardServer
		}
		boardType := ""
		if cfg.BoardType != nil {
			boardType = *cfg.BoardType
		}
		displayName := ""
		if cfg.DisplayName != nil {
			displayName = *cfg.DisplayName
		}

		// Restore model and capabilities from saved config
		modelStr := ""
		if cfg.Model != nil {
			modelStr = *cfg.Model
		}
		var caps *agent.Capabilities
		if cfg.Capabilities != nil && *cfg.Capabilities != "" {
			caps = &agent.Capabilities{}
			if err := json.Unmarshal([]byte(*cfg.Capabilities), caps); err != nil {
				log.Printf("[reset-team] error unmarshaling capabilities for agent %s: %v", displayName, err)
				continue
			}
		}
		// Add model flag if stored
		if modelArgs, err := agentmodels.ModelArgs(modelStr); err != nil {
			log.Printf("[reset-team] stored model of agent %s is not a valid model id; it starts with the CLI's default", displayName)
		} else {
			flags = append(flags, modelArgs...)
		}
		storedPermMode := ""
		if cfg.PermissionMode != nil && *cfg.PermissionMode != "" {
			if !agent.ValidPermissionMode(*cfg.PermissionMode) {
				log.Printf("[reset-team] corrupt stored permission mode for agent %s: %s", displayName, *cfg.PermissionMode)
				continue
			}
			storedPermMode = *cfg.PermissionMode
		}
		cleanFlags := stripAgentPermissionFlags(flags)
		if carried, _ := agent.SplitPermissionFlags(flags); caps.IsEmpty() && len(carried) > 0 {
			cleanFlags = append(cleanFlags, carried...) // what the agent was started with, as it was (#6423)
		} else if caps.IsEmpty() && storedPermMode != "" && storedPermMode != "default" {
			cleanFlags = append(cleanFlags, agent.PermissionFlagsForAgent(cfg.AgentType, storedPermMode)...)
		}
		tools := store.UnmarshalFlags(cfg.Tools)
		prompt = InjectKnowledgeIntoPrompt(h.cfg.CoralDir(), boardName, displayName, prompt)
		result, err := h.launchSession(bgCtx, launchSpec{
			WorkDir: cfg.WorkingDir, AgentType: cfg.AgentType, DisplayName: displayName, Flags: cleanFlags, Prompt: prompt,
			BoardName: boardName, BoardServer: boardServer, BoardType: boardType, Model: modelStr, Thinking: ptr.Value(cfg.Thinking), Capabilities: caps, Tools: tools,
			PermissionMode: storedPermMode,
		})
		if err != nil {
			log.Printf("[reset-team] failed to re-launch %s: %v", displayName, err)
			launched = append(launched, map[string]any{"name": displayName, "error": err.Error()})
			continue
		}

		// Re-setup board subscription
		h.setupBoardAndPrompt(result["session_id"].(string), result["session_name"].(string),
			cfg.AgentType, boardName, launchedName(result, displayName))

		name := launchedName(result, displayName)
		if name != "" {
			relaunched = append(relaunched, name)
		}
		launched = append(launched, map[string]any{
			"name":         name,
			"session_id":   result["session_id"],
			"session_name": result["session_name"],
		})
	}

	// Advance read cursors for ALL agents AFTER all launches complete.
	// This ensures no agent has a stale cursor from messages posted by
	// other agents during the sequential re-launch process. By the name each was launched
	// under: a numbered one ("Backend Dev 2", #3543) has its own subscription (#3549).
	if h.bs != nil {
		for _, name := range relaunched {
			if err := h.bs.AdvanceReadCursor(bgCtx, boardName, name); err != nil {
				log.Printf("[reset-team] failed to advance read cursor for %s: %v", name, err)
			}
		}
	}

	log.Printf("[reset-team] reset board %q: %d agents", boardName, len(launched))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "board": boardName, "agents": launched})
}
