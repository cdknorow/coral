package routes

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/secrets"
)

// #5389: the tunnel runs a reply through the base redactor and the Hub redactor (its keyword rules too). Every new fact, in every state that carries
// it, reaches the hub whole through both, and the free-text-looking one (the Homebrew command) is not taken for a flag with a secret value.
func TestEveryNewFactReachesTheHubWholeThroughBothRedactors(t *testing.T) {
	require.NoError(t, secrets.Init())
	e := newRelaunchEnv(t)
	states := map[string]func(){
		"homebrew with a roll-back": func() {
			e.up.rec.Staged, e.up.rec.State = nil, updNone
			e.up.rec.CheckedAt, e.up.rec.InstallKind = e.now, "homebrew"
			e.up.rec.NotReady, e.up.rec.Latest = "not_promoted", "1.4.140"
			e.up.rec.LastRollback = &UpdateRollback{At: e.now, From: "1.4.130", To: "1.4.131", Reason: "swap_failed"}
		},
		"unwritable, nothing promoted": func() {
			e.up.rec.Staged, e.up.rec.State = nil, updNone
			e.up.rec.InstallKind, e.up.rec.NotReady, e.up.rec.Latest = "unwritable", "unsupported_install", ""
			e.up.rec.LastRollback = &UpdateRollback{At: e.now, From: "1.4.130", To: "1.4.131", Reason: "something else"}
		},
		"tarball, check failed": func() {
			e.up.rec.Staged, e.up.rec.State = nil, updFailed
			e.up.rec.InstallKind, e.up.rec.NotReady, e.up.rec.CheckedAt = "tarball", "check_failed", e.now
		},
	}
	for name, set := range states {
		set()
		_, st := e.status(hubHdr(ownerA))
		body, err := json.Marshal(st)
		require.NoError(t, err)
		for rname, red := range map[string]func([]byte) ([]byte, error){
			"base": secrets.Redactor().Text,
			"hub":  secrets.HubRedactor(func() bool { return true }).Text,
		} {
			out, err := red(body)
			require.NoError(t, err)
			assert.JSONEq(t, string(body), string(out), "%s: the %s redactor left the status as it was", name, rname)
		}
		if name == "homebrew with a roll-back" {
			assert.Equal(t, "brew upgrade --cask coral", st["upgrade_command"])
		}
	}
}
