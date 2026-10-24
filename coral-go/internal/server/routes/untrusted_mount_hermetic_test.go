package routes

import "github.com/subgentic/coral/internal/agent"

// This computer's real PATH may hold a folder Windows will not open (an untrusted mount point, such as Codex's junctions: #340). The tests of this package launch agents by their
// bare names and must not depend on that: no PATH folder is untrusted unless a test says so.
func init() {
	agent.UntrustedMountDir = func(string) (string, bool) { return "", false }
}
