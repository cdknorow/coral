package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// #3387 phase 1: every request is classified; a token's board requests are checked
// against its session's subscription; mismatches are counted and listed, and nothing is
// ever refused or altered.
func TestIdentityObserver(t *testing.T) {
	_, bh := setupBoardTestServer(t)
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	defer db.Close()
	ctx := context.Background()
	_, err = bh.bs.Subscribe(ctx, "p", "Dev", "Developer", "claude-dev", nil, "")
	require.NoError(t, err)
	token, err := db.MintSessionToken(ctx, "sid-dev", "claude-dev")
	require.NoError(t, err)

	cookie := false
	o := NewIdentityObserver(db, bh.bs, func(*http.Request) bool { return cookie })
	var seenBody string
	reached := 0
	h := o.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		b, _ := io.ReadAll(r.Body)
		seenBody = string(b)
		if s, ok := VerifiedSession(r.Context()); ok {
			w.Header().Set("X-Verified", s.SessionName)
		}
	}))
	do := func(method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	tok := map[string]string{store.SessionTokenHeader: token, "User-Agent": "coral-board/dev"}

	rec := do("POST", "/api/board/p/messages", `{"subscriber_id":"Dev","content":"hi"}`, tok)
	assert.Equal(t, "claude-dev", rec.Header().Get("X-Verified"), "the request is attributed to its session")
	assert.Equal(t, `{"subscriber_id":"Dev","content":"hi"}`, seenBody, "the body is untouched")
	// A presented token decides who the caller is, in every mode (#3495): acting as another
	// subscriber or as the Operator is refused; another board stays log-only here.
	assert.Equal(t, http.StatusForbidden, do("POST", "/api/board/p/messages", `{"subscriber_id":"Lead","content":"x"}`, tok).Code)
	assert.Equal(t, http.StatusForbidden, do("POST", "/api/board/p/tasks", `{"created_by":"Operator","title":"x"}`, tok).Code)
	assert.Equal(t, http.StatusOK, do("POST", "/api/board/q/messages", `{"subscriber_id":"Dev","content":"x"}`, tok).Code)
	assert.Equal(t, http.StatusForbidden, do("GET", "/api/board/p/messages?subscriber_id=Lead", ``, tok).Code)
	do("POST", "/api/board/p/messages", `{"subscriber_id":"Dev","as":"Developer","content":"x"}`, tok)

	do("GET", "/api/board/p/messages", ``, map[string]string{store.SessionTokenHeader: "made-up"})
	do("GET", "/api/board/p/messages", ``, map[string]string{"User-Agent": "coral-hook-task-sync/dev"})
	do("GET", "/api/board/p/messages", ``, map[string]string{tunnel.ViaHeader: cloud.ViaSecret()})
	do("GET", "/api/sessions/live", ``, map[string]string{"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"})
	do("GET", "/api/sessions/live", ``, nil)
	cookie = true
	do("GET", "/api/sessions/live", ``, nil)
	assert.Equal(t, 9, reached, "outside strict mode only the three identity mismatches are refused")

	rep := o.Report()
	callers := rep["callers"].(map[string]int64)
	assert.EqualValues(t, 6, callers[callerAgentToken])
	assert.EqualValues(t, 1, callers[callerAgentBadToken])
	assert.EqualValues(t, 1, callers[callerAgentNoToken])
	assert.EqualValues(t, 1, callers[callerHub])
	assert.EqualValues(t, 1, callers[callerDashCookie])
	assert.EqualValues(t, 2, callers[callerLocalOther]+callers[callerDashNoCookie])
	mm := rep["mismatches"].(map[string]int64)
	assert.EqualValues(t, 2, mm[mismatchImpersonate], "posting as Lead, reading as Lead")
	assert.EqualValues(t, 1, mm[mismatchOperator])
	assert.EqualValues(t, 1, mm[mismatchCrossBoard])
	recent := rep["recent_mismatches"].([]IdentityMismatch)
	require.Len(t, recent, 4)
	assert.Equal(t, "Dev", recent[0].Expected)
	assert.Equal(t, "Lead", recent[0].Claimed)
	assert.EqualValues(t, 6, rep["attributed_by_session"].(map[string]int64)["claude-dev"])
	assert.EqualValues(t, 6, rep["attributed_by_tool"].(map[string]int64)["coral-board"])
	b, _ := json.Marshal(rep)
	assert.NotContains(t, string(b), token, "the report never shows a token")
	assert.Contains(t, rep["would_break_in_strict_mode"], callerAgentNoToken)
}

// The report: only the dashboard on this computer or the coral CLI, never an agent's
// plain request or the Coral Hub.
func TestIdentityReportRoute(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	defer db.Close()
	h := NewCloudHandler(store.NewSessionStore(db), t.TempDir())
	h.SetIdentityObserver(NewIdentityObserver(db, nil, nil))
	// No real socket below: say it's Coral's own user, or peercred fails closed on Linux (#3442).
	h.SetPeerCheck(func(*http.Request) (bool, bool) { return true, true })
	req := func(mut func(*http.Request)) int {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8420/api/system/identity-report", nil)
		r.RemoteAddr = "127.0.0.1:5555"
		mut(r)
		rec := httptest.NewRecorder()
		h.IdentityReport(rec, r)
		return rec.Code
	}
	assert.Equal(t, http.StatusOK, req(func(r *http.Request) {
		r.Header.Set("Origin", "http://127.0.0.1:8420")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	}))
	assert.Equal(t, http.StatusForbidden, req(func(r *http.Request) {}), "a plain local request (an agent's curl)")
	assert.Equal(t, http.StatusForbidden, req(func(r *http.Request) { r.Header.Set(tunnel.ViaHeader, cloud.ViaSecret()) }))
}
