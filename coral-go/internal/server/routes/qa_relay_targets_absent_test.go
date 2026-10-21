package routes

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/pkg/tunnel"
)

// QA #5599: Coral Hub sends no "reachable" field for a target (struck on purpose by hub Security). The server must pass that on as ABSENT: a field turned into "reachable": false is what
// `coral-board targets` printed as "(offline)" for every target, and the CLI's own fix (read it as optional) never sees an absent field if the server writes one.
func TestRelayTargetsWithoutReachableStayWithoutIt(t *testing.T) {
	e := newSendEnv(t)
	e.hub.answer = func(path string, body map[string]any) (tunnel.RelayResponse, error) {
		return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":true,"targets":[
			{"address":"homelab/qa","server":"homelab","team":"qa"},
			{"address":"lab2/ops","server":"lab2","team":"ops","reachable":false},
			{"address":"lab3/dev","server":"lab3","team":"dev","reachable":true}]}`)}, nil
	}
	code, out := e.do("GET", "/api/board/alpha/relay-targets?subscriber_id=Orchestrator&session_name=tmux-alphaOrchestrator", nil, nil)
	require.Equal(t, http.StatusOK, code, out)
	targets, _ := out["targets"].([]any)
	require.Len(t, targets, 3)
	byAddr := map[string]map[string]any{}
	for _, tg := range targets {
		m := tg.(map[string]any)
		byAddr[m["address"].(string)] = m
	}
	_, has := byAddr["homelab/qa"]["reachable"]
	assert.False(t, has, "no field from the hub: none to the CLI (it was written as false: %v)", byAddr["homelab/qa"])
	assert.Equal(t, false, byAddr["lab2/ops"]["reachable"], "a hub that says false is passed on as false")
	assert.Equal(t, true, byAddr["lab3/dev"]["reachable"], "and true as true")
}
