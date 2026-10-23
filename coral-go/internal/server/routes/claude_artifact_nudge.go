package routes

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/claudeartifact"
	"github.com/subgentic/coral/internal/logparse"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
)

// An agent that publishes a claude.ai artifact is reminded once to make a Coral artifact instead (#6583). Coral only types a fixed line: it never uploads, edits
// or posts for the agent, and the line carries nothing from the page or the agent.
//
// Signals, in order: the agent's own Artifact tool call (its hook event), then a claude.ai artifact link in the agent's own board post or in its last reply (sent
// with its Stop event). One reminder per artifact: a publish by the artifact's page (url, else file), a link by its id across the whole board, so a link an agent
// repeats from someone else's post, or one the Operator posted, reminds nobody.

// claudeArtifactNudgeText is the reminder: one line, fixed.
const claudeArtifactNudgeText = "You published a claude.ai artifact. Make it a Coral artifact instead: run 'coral-agent artifact upload <file>' and post the coral:// link on the board. This is a reminder only; Coral did nothing for you."

const claudeArtifactToolName = "Artifact"

// ClaudeArtifactNudger types the reminder.
type ClaudeArtifactNudger struct {
	ss   *store.SessionStore
	term ptymanager.SessionTerminal
	now  func() time.Time
	// deliver types text into a session (tests replace it); sync makes the send run in the caller's goroutine (tests).
	deliver func(ctx context.Context, term ptymanager.SessionTerminal, name, agentType, sessionID, key, text, what string) error
	sync    bool
}

// NewClaudeArtifactNudger is the nudger for the sessions in ss, typing through term.
func NewClaudeArtifactNudger(ss *store.SessionStore, term ptymanager.SessionTerminal) *ClaudeArtifactNudger {
	return &ClaudeArtifactNudger{ss: ss, term: term, now: time.Now, deliver: deliverAutomated}
}

func (n *ClaudeArtifactNudger) enabled(ctx context.Context) bool {
	if n == nil || n.ss == nil {
		return false
	}
	s, err := n.ss.GetSettings(ctx)
	return err == nil && settings.Bool(s, settings.NudgeClaudeArtifacts)
}

// PublishKey is what names the artifact an Artifact tool call published: its page's url (an update), else the file it was made from (a republish of the same
// file), else the call itself. ok is false when the call is not a publish (read, list, open, delete, pin, unpin, quickstart) or it failed.
func PublishKey(toolName string, detail map[string]any, toolUseID string) (key string, ok bool) {
	if toolName != claudeArtifactToolName {
		return "", false
	}
	if failed, _ := detail["failed"].(bool); failed {
		return "", false
	}
	if action, _ := detail["action"].(string); action != "" && action != "publish" {
		return "", false
	}
	if u, _ := detail["url"].(string); u != "" {
		if ids := claudeartifact.LinkIDs(u); len(ids) == 1 {
			return "link:" + ids[0], true // the same key a link to it has, so its link in a later post is not a second reminder
		}
		return "url:" + u, true
	}
	if fp, _ := detail["file_path"].(string); fp != "" {
		return "file:" + filepath.Clean(fp), true
	}
	if toolUseID != "" {
		return "call:" + toolUseID, true
	}
	return "", false
}

// FromPublish reminds the agent that owns the session once for the artifact its Artifact tool call published.
func (n *ClaudeArtifactNudger) FromPublish(ctx context.Context, owner *store.SessionTokenOwner, key string) {
	if owner == nil || key == "" || !n.enabled(ctx) {
		return
	}
	scoped := key
	if !strings.HasPrefix(key, "link:") { // an artifact's id is the same whoever published it; a file or call is the agent's own
		scoped = "tool:" + owner.SessionID + ":" + key
	}
	n.remind(ctx, owner, scoped)
}

// FromLinks reminds once for each new claude.ai artifact id in text. owner is the agent whose own text it is; nil (the Operator's or a Hub user's post) only
// records the ids as seen, so an agent that later repeats the link is not reminded for it.
func (n *ClaudeArtifactNudger) FromLinks(ctx context.Context, owner *store.SessionTokenOwner, ids []string) {
	if n == nil || n.ss == nil || len(ids) == 0 {
		return
	}
	if owner != nil && !n.enabled(ctx) {
		return
	}
	for _, id := range ids {
		if !claudeartifact.ValidID(id) {
			continue
		}
		if owner == nil {
			if _, err := n.ss.ClaimClaudeArtifactSeen(ctx, "link:"+id, "", n.now()); err != nil {
				slog.Warn("claude artifact: could not record a link", "error", err)
			}
			continue
		}
		if n.remind(ctx, owner, "link:"+id) {
			return // one reminder per post or reply, however many links it has
		}
	}
}

// remind types the reminder when key is new; it reports whether it did.
func (n *ClaudeArtifactNudger) remind(ctx context.Context, owner *store.SessionTokenOwner, key string) bool {
	first, err := n.ss.ClaimClaudeArtifactSeen(ctx, key, owner.SessionID, n.now())
	if err != nil {
		slog.Warn("claude artifact: could not record a publish", "error", err)
		return false
	}
	if !first {
		return false
	}
	agentType, sessionID := logparse.ParseSessionName(owner.SessionName)
	if sessionID == "" {
		return false
	}
	// A burst of artifacts is one held reminder: the key is the session's, so a newer one replaces a held one.
	send := func() {
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := n.deliver(sctx, n.term, owner.SessionName, agentType, sessionID, "claude-artifact:"+sessionID, claudeArtifactNudgeText, "claude artifact reminder"); err != nil {
			slog.Info("claude artifact: reminder not typed", "session", owner.SessionName, "error", err)
		}
	}
	if n.sync {
		send()
	} else {
		safego.Go("routes/claude-artifact-nudge", send)
	}
	slog.Info("claude artifact: reminded an agent to use a Coral artifact", "session", owner.SessionName, "key_kind", strings.SplitN(key, ":", 2)[0])
	return true
}

// SetArtifactNudger sets who reminds an agent that published a claude.ai artifact (#6583).
func (h *SessionsHandler) SetArtifactNudger(n *ClaudeArtifactNudger) { h.artifactNudge = n }

// remindClaudeArtifact is the hook side of the reminder: an agent's own (token-verified) tool_use event for an Artifact publish, or the links its Stop event
// carries from its last reply (claude_artifact_ids, which the hook read from its own transcript; each id is checked again here).
func (h *SessionsHandler) remindClaudeArtifact(ctx context.Context, owner *store.SessionTokenOwner, eventType, toolName, toolUseID string, detail any) {
	if h.artifactNudge == nil || owner == nil {
		return
	}
	d, _ := detail.(map[string]any)
	switch eventType {
	case "tool_use":
		if key, ok := PublishKey(toolName, d, toolUseID); ok {
			h.artifactNudge.FromPublish(ctx, owner, key)
		}
	case "stop":
		raw, _ := d["claude_artifact_ids"].([]any)
		var ids []string
		for _, v := range raw {
			if id, _ := v.(string); claudeartifact.ValidID(id) && len(ids) < claudeartifact.MaxIDs {
				ids = append(ids, id)
			}
		}
		h.artifactNudge.FromLinks(ctx, owner, ids)
	}
}
