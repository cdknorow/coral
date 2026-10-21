package ptymanager

import (
	"errors"
	"sort"
	"strings"
)

// windowsEnvBlock is env as CreateProcessW takes it in an environment block: one entry per
// variable, names compared without case as Windows does, the last entry for a name winning (so
// newSession's TERM, COLORTERM and CORAL_SESSION_NAME replace any the server had; a duplicate
// name left in the block would let the server's own come first), sorted by name without case
// as the block is documented to be. The per-drive "=C:" entries Windows keeps sort first and
// are kept.
func windowsEnvBlock(env []string) []string {
	index := make(map[string]int, len(env))
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name := envName(kv)
		key := strings.ToUpper(name)
		if i, ok := index[key]; ok {
			out[i] = kv
			continue
		}
		index[key] = len(out)
		out = append(out, kv)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToUpper(envName(out[i])) < strings.ToUpper(envName(out[j]))
	})
	return out
}

// envName is an entry's name: up to the first "=" after its first character (a per-drive
// entry's name, "=C:", starts with one).
func envName(kv string) string {
	if i := strings.Index(kv[min(1, len(kv)):], "="); i >= 0 {
		return kv[:i+min(1, len(kv))]
	}
	return kv
}

// errEmptyEnv: a process is never started with an empty environment, which ConPTY would turn
// into the parent's whole one.
var errEmptyEnv = errors.New("the agent's environment is empty: refusing to start it with Coral's own")
