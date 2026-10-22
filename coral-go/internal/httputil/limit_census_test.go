package httputil

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// too429 finds a 429 in Go code: the constant, or the number given as a status.
var too429 = regexp.MustCompile(`http\.StatusTooManyRequests|WriteHeader\(429\)|,\s*429\)`)

// tooManyRequestsSites is every place in Coral's own (non-test) code that mentions a 429,
// with why it is fine (#3434). A 429 Coral WRITES must be a per-action limit sent with
// WriteActionLimit (or the marker set by hand): the dashboard pauses every poller on an
// unmarked 429, taking it for an overloaded server. A new site fails this test until it is
// marked (or, for a real overload answer, explained here).
var tooManyRequestsSites = map[string]struct {
	count  int
	reason string
}{
	"internal/httputil/limit.go":                        {1, "WriteActionLimit itself: always sends X-Coral-Limit: action"},
	"internal/auth/middleware.go":                       {1, "too many wrong API keys: marked by hand (X-Coral-Limit set just before), plain-text body"},
	"internal/cloud/enroll.go":                          {2, "client side: reads Coral Hub's 429, never writes one"},
	"internal/cloud/redeem.go":                          {1, "client side: reads Coral Hub's 429"},
	"internal/cloud/signin.go":                          {1, "client side: the Hub sign-in check reads Coral Hub's busy 429 as transient, like no answer (#3966)"},
	"internal/tracking/posthog.go":                      {2, "client side: retries the analytics service's 429 (and says so in a comment)"},
	"cmd/coral/cloud_connect.go":                        {1, "client side: the CLI reads Coral Hub's 429"},
	"cmd/coral-board/send.go":                           {1, "client side: coral-board reads a relayed send's 429 to suggest a retry (#3489)"},
	"pkg/tunnel/control.go":                             {1, "relay protocol, not HTTP to the dashboard: the hub's one-a-second limit on the connector's local mode messages, answered to the connector, which falls back to a reconnect (#5918)"},
	"pkg/tunnel/inbound.go":                             {1, "relay protocol, not HTTP to the dashboard: the hub's per-tunnel in-flight cap answer to a connector, which Coral passes on marked (#3489; moved from relay.go into the shared table of connector-opened kinds, #3966)"},
	"cmd/coral-agent/main.go":                           {1, "client side: coral-agent reads Coral's 429 on an upload and surfaces its Retry-After (#4004)"},
	"pkg/tunnel/limit.go":                               {1, "the connector's hub-wide in-flight cap (#3624): a load limit, not a per-action one, so it is marked X-Coral-Limit: connector and the dashboard backs off on it"},
	"internal/server/routes/cross_team_attach.go":       {3, "the hourly attach limit of coral-board send --attach (two sites) and the check in attachRefusal.write that sends every 429 with WriteActionLimit (marked X-Coral-Limit: action; #4379)"},
	"internal/server/routes/cross_team_attach_relay.go": {1, "client side: reads Coral Hub's 429 on a file of a send with --attach and passes it on through relayError, which writes every 429 with WriteActionLimitBody (marked), keeping its Retry-After (#4403)"},
	"pkg/hubproxy/handler.go":                           {1, "client side: writeUpstreamErr reads a machine's own 429 on an upgrade (the socket caps, #5011) and answers 503 'remote busy' with the cap's code and the machine's Retry-After: the hub writes no 429 of its own here (#5035)"},
	"pkg/tunnel/local_ws.go":                            {1, "client side: the connector reads the local server's 429 on an upgrade and refuses the hub's stream as busy (Stream.RejectBusy, with the Retry-After): no 429 is written to a dashboard (#5035)"},
	"pkg/tunnel/relay_artifact.go":                      {1, "relay protocol, not HTTP to the dashboard: the hub's per-tunnel in-flight cap answer to a connector's file request (the artifact twin of inbound.go's), read by ClientSession.RelayArtifact and passed on marked by relayError (#4378)"},
	"internal/server/routes/cross_team_send.go":         {2, "a relayed send's per-sender limit, and Coral Hub's 429 passed on: relayError writes every 429 with WriteActionLimitBody (marked), the other mention is that check (#3489)"},
}

func TestEveryTooManyRequestsIsAccountedFor(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "node_modules", "testdata", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if n := len(too429.FindAllIndex(b, -1)); n > 0 {
			rel, _ := filepath.Rel(root, path)
			got[filepath.ToSlash(rel)] = n
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	for f, n := range got {
		want, ok := tooManyRequestsSites[f]
		switch {
		case !ok:
			problems = append(problems, f+": a new 429; a per-action limit must use httputil.WriteActionLimit (or be explained in tooManyRequestsSites)")
		case n != want.count:
			problems = append(problems, f+": the number of 429s changed; check each one is marked, then update tooManyRequestsSites")
		}
	}
	for f := range tooManyRequestsSites {
		if _, ok := got[f]; !ok {
			problems = append(problems, f+": listed but has no 429 any more; remove it from tooManyRequestsSites")
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
	// The hand-marked one really is marked.
	mw, err := os.ReadFile(filepath.Join(root, "internal/auth/middleware.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`ActionLimitHeader, "action"\)\s*\n\s*http\.Error\(w, "Too many authentication attempts", http\.StatusTooManyRequests\)`).Match(mw) {
		t.Error("internal/auth/middleware.go: the 429 for too many wrong keys must set X-Coral-Limit: action just before")
	}
}
