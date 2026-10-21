package routes

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
)

// QA (#5508): the door from angles the feature's own tests leave.

// post is a same-Coral send with any body fields, as a session (sid) or the verified Operator ("operator").
func (e *attachEnv) post(sid, to string, body map[string]any) (int, []byte) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", e.url+"/api/board/"+to+"/external-messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if sid == "operator" {
		req.Header.Set("X-Test-Operator", "1")
	} else if sid != "" {
		req.Header.Set("X-Test-Session", sid)
		req.Header.Set("X-Test-Name", "Orchestrator")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// The sender's cross-team id is read from its own board row; one named in the body is not read (T5): an unlisted board that claims a listed board's id is refused.
func TestQA_ASameCoralSenderCannotClaimAnotherBoardsID(t *testing.T) {
	e := newAttachEnv(t, false)
	e.enforced(true)
	e.setList("beta", board.AllowedSender{Server: board.LocalServer, CrossTeamID: e.idOf("gamma")}) // gamma is listed; alpha is not
	before := e.messages("beta")
	for _, field := range []string{"from_cross_team_id", "cross_team_id", "from_board_id"} {
		code, raw := e.post(alphaOrchSID, "beta", map[string]any{"from_board": "alpha", "subscriber_id": "Orchestrator", "session_name": "claude-" + alphaOrchSID, "content": "claiming", field: e.idOf("gamma")})
		assert.NotEqual(t, http.StatusOK, code, "%s: %s", field, raw)
	}
	assert.Equal(t, before, e.messages("beta"), "nothing was delivered")
}

// Removal under load (T6): sends racing a removal; once the removal has returned, every send is refused. Run with -race.
func TestQA_AfterARemovalReturnsNoConcurrentSendIsDelivered(t *testing.T) {
	e := newAttachEnv(t, false)
	e.enforced(true)
	alpha := board.AllowedSender{Server: board.LocalServer, CrossTeamID: e.idOf("alpha")}
	e.setList("beta", alpha)
	code, _ := e.post(alphaOrchSID, "beta", map[string]any{"from_board": "alpha", "subscriber_id": "Orchestrator", "session_name": "claude-" + alphaOrchSID, "content": "one before"})
	require.Equal(t, http.StatusOK, code)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ { // background traffic during the edit
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					e.post(alphaOrchSID, "beta", map[string]any{"from_board": "alpha", "subscriber_id": "Orchestrator", "session_name": "claude-" + alphaOrchSID, "content": "noise"})
				}
			}
		}()
	}
	e.setList("beta") // the removal returns
	delivered := e.messages("beta")
	var wg2 sync.WaitGroup
	var mu sync.Mutex
	var ok int
	for i := 0; i < 20; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			c, _ := e.post(alphaOrchSID, "beta", map[string]any{"from_board": "alpha", "subscriber_id": "Orchestrator", "session_name": "claude-" + alphaOrchSID, "content": "after"})
			if c == http.StatusOK {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg2.Wait()
	close(stop)
	wg.Wait()
	assert.Zero(t, ok, "no send started after the removal returned was delivered")
	assert.Equal(t, delivered, e.messages("beta"), "and nothing arrived after it, from the background traffic either")
}
