package tunnel

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GET /api/system/cli-check is a Launch route (#4579): it runs "<cli> --version" on the computer. Only type (an exact agent type) and
// source (cli_recheck) pass; binary, which would name a program to run, never does, and neither does an unknown type, which the handler
// answers as found.
func TestCLICheckRouteIsLaunchAndNarrow(t *testing.T) {
	for _, typ := range []string{"claude", "codex", "gemini", "agy", "antigravity", "pi"} {
		assert.Equal(t, TierLaunch, Classify("GET", "/api/system/cli-check", "type="+typ), typ)
		assert.Equal(t, TierLaunch, Classify("GET", "/api/system/cli-check", "type="+typ+"&source=cli_recheck"), typ+" recheck")
		assert.Equal(t, TierLaunch, Classify("GET", "/api/system/cli-check", "source=cli_recheck&type="+typ), typ+" order")
	}
	for _, q := range []string{
		"type=claude&binary=/bin/sh", "binary=/bin/sh", "binary=claude&type=claude", "type=bogus", "type=", "type=Claude", "type=terminal",
		"type=claude&x=1", "x=1", "type=claude&type=codex", "type=claude&type=claude", "type=claude,codex", "type=claude%00",
		"type=claude&source=other", "type=claude&source=", "type=claude&source=cli_recheck&source=cli_recheck", "type=claude&source=CLI_RECHECK",
		"type=../claude", "type=claude%20",
	} {
		assert.Equal(t, TierNever, Classify("GET", "/api/system/cli-check", q), q)
	}
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		assert.Equal(t, TierNever, Classify(m, "/api/system/cli-check", "type=claude"), m)
	}
	assert.Equal(t, TierNever, Classify("GET", "/api/system/cli-check/x", "type=claude"))
}

// Through a session: a machine whose maximum mode is view or interact refuses it (the check starts a program), launch passes it.
func TestCLICheckNeedsTheLaunchMode(t *testing.T) {
	hub := newHub(t, fastCfg())
	mode := TierView
	calls := 0
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"found":true}`))
	}), func(l *LocalHandler) {
		l.AllowRequest = AllowRequest(func() Mode { return mode })
		l.MaxMode = func() Mode { return mode }
	})
	do := func(q string) error {
		req, _ := http.NewRequest("GET", "http://x/api/system/cli-check?"+q, nil)
		resp, err := sess.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	var se *StreamError
	for _, m := range []Mode{TierView, TierInteract} {
		mode = m
		err := do("type=claude")
		require.ErrorAs(t, err, &se, "mode %v", m)
		assert.Equal(t, CodeLocalMode, se.Code)
	}
	assert.Equal(t, 0, calls, "the handler never ran")
	mode = TierLaunch
	require.NoError(t, do("type=claude"))
	assert.Equal(t, 1, calls)
	// binary does not pass in any mode, even launch
	err := do(url.Values{"type": {"claude"}, "binary": {"/bin/sh"}}.Encode())
	require.ErrorAs(t, err, &se)
	assert.Equal(t, CodePathNotAllowed, se.Code)
	assert.Equal(t, 1, calls)
}
