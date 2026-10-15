package store

import (
	"encoding/json"
	"sort"
)

// MarshalJSON writes a live session with its MCP servers as their names only (#3627 F1): a
// server's env and headers routinely carry tokens, and session objects go to the dashboard
// and, at View, through Coral Hub, where only recognisable token shapes are redacted. The full
// configuration stays on the server, where launch, restart and wake read it.
func (ls LiveSession) MarshalJSON() ([]byte, error) {
	type plain LiveSession
	return json.Marshal(struct {
		plain
		MCPServers []string `json:"mcp_servers,omitempty"`
	}{plain(ls), MCPServerNames(ls.MCPServers)})
}

// MCPServerNames lists the servers in a stored mcp_servers JSON object, sorted; nil for none
// or for a value that isn't an object.
func MCPServerNames(raw *string) []string {
	if raw == nil || *raw == "" {
		return nil
	}
	var servers map[string]json.RawMessage
	if json.Unmarshal([]byte(*raw), &servers) != nil || len(servers) == 0 {
		return nil
	}
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
