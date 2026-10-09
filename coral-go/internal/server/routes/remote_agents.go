package routes

import (
	"strings"

	"github.com/cdknorow/coral/internal/background"
)

// Hub list contract (see background/remote_agents.go for the full text):
// every session has "server" ("local" or the remote id) once any remote is
// registered; remote sessions of an unreachable server carry "stale": true;
// the live list becomes {"sessions": [...], "servers": [{id,label,status}]}.
// With no remotes registered nothing changes from the single-server shape.

// SetRemoteAgents wires the remote agent cache into the list and feed.
func (h *SessionsHandler) SetRemoteAgents(src background.RemoteAgentSource) {
	h.remoteAgents = src
}

func (h *SessionsHandler) remoteSnapshot() background.RemoteSnapshot {
	if h.remoteAgents == nil {
		return background.RemoteSnapshot{}
	}
	return h.remoteAgents.Snapshot()
}

// mergeRemote tags local sessions with server "local" and appends remote ones.
func mergeRemote(local []map[string]any, snap background.RemoteSnapshot) []map[string]any {
	out := make([]map[string]any, 0, len(local)+len(snap.Sessions))
	for _, s := range local {
		s["server"] = "local"
		out = append(out, s)
	}
	return append(out, snap.Sessions...)
}

const remoteKeyPrefix = "r\x00"

// remoteKey builds a diff key that cannot collide across servers or with
// local keys.
func remoteKey(server, key string) string { return remoteKeyPrefix + server + "\x00" + key }

func splitRemoteKey(k string) (server, key string, ok bool) {
	if !strings.HasPrefix(k, remoteKeyPrefix) {
		return "", "", false
	}
	parts := strings.SplitN(k[len(remoteKeyPrefix):], "\x00", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func feedKey(s map[string]any) string {
	if sid, _ := s["session_id"].(string); sid != "" {
		return sid
	}
	name, _ := s["name"].(string)
	return name
}
