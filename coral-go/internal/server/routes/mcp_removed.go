package routes

import (
	"bytes"
	"encoding/json"
)

// Coral doesn't configure MCP servers (#3644, the operator's decision): an agent's MCP servers
// come from its CLI's own configuration. A request that still names them is refused, so nobody
// thinks the setting applied; stored configurations that still hold them are ignored.
const errMCPNotSupported = "mcpServers is not supported: configure MCP servers in the agent CLI's own settings"

// mcpGiven reports whether a request field named mcpServers was sent with any value but null.
func mcpGiven(raw json.RawMessage) bool {
	v := bytes.TrimSpace(raw)
	return len(v) > 0 && !bytes.Equal(v, []byte("null"))
}

// agentNamesMCP reports whether an agent configuration object (a workflow's default_agent or a
// step's agent) names mcpServers.
func agentNamesMCP(raw json.RawMessage) bool {
	var a struct {
		MCPServers json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return false
	}
	return mcpGiven(a.MCPServers)
}
