package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

const (
	ownerA = "hub-user-1234abcd"
	ownerB = "hub-user-9999ffff"
)

type stubUpdater struct {
	mu       sync.Mutex
	rec      UpdaterRecord
	applies  []string
	audits   []RelaunchAudit
	applyErr error
	// checks: how many times Check ran; checkResult and checkErr are what it answers; checkBlock, when set, holds it until closed.
	checks      int
	checkResult string
	checkErr    error
	checkBlock  chan struct{}
}

func (s *stubUpdater) Check(context.Context) (string, error) {
	s.mu.Lock()
	s.checks++
	block, res, err := s.checkBlock, s.checkResult, s.checkErr
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	return res, err
}

func (s *stubUpdater) checkCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checks
}

func (s *stubUpdater) Record(context.Context) (UpdaterRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec, nil
}

func (s *stubUpdater) Apply(_ context.Context, by string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applyErr != nil {
		return "", s.applyErr
	}
	s.applies = append(s.applies, by)
	return "op-1", nil
}

func (s *stubUpdater) Audit(_ context.Context, a RelaunchAudit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, a)
	return nil
}

func (s *stubUpdater) applyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.applies)
}

type relaunchEnv struct {
	t        *testing.T
	h        *UpdateRelaunchHandler
	up       *stubUpdater
	settings map[string]string
	label    string
	agents   []RestartAgent
	now      time.Time
	on       bool
}

func newRelaunchEnv(t *testing.T) *relaunchEnv {
	t.Helper()
	// These tests are about the relaunch itself, on every OS: Windows' refusal (#166) is tested on its own (TestRelaunchIsRefusedOnWindows).
	was := relaunchNeedsLocalApproval
	relaunchNeedsLocalApproval = false
	t.Cleanup(func() { relaunchNeedsLocalApproval = was })
	e := &relaunchEnv{t: t, settings: map[string]string{MaxModeSetting: "launch"}, label: ownerA, on: true,
		now:    time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
		agents: []RestartAgent{{SessionID: "claude-1", Name: "Dev", State: "idle"}}}
	e.up = &stubUpdater{rec: UpdaterRecord{Enabled: true, Installed: "1.4.130", State: updStaged, Supported: true,
		Staged: &StagedUpdate{Version: "1.4.131", StagedAt: e.now.Add(-48 * time.Hour), Verified: true},
		Trust:  UpdateTrust{Issuer: "https://token.actions.githubusercontent.com", Signer: "signer", ManifestSequence: 42, PromotedAt: e.now.Add(-72 * time.Hour), ExpireAt: e.now.Add(24 * time.Hour)}}}
	e.h = e.build(filepath.Join(t.TempDir(), "updates"))
	return e
}

func (e *relaunchEnv) build(dir string) *UpdateRelaunchHandler {
	return &UpdateRelaunchHandler{
		Enabled: func() bool { return e.on }, Updater: e.up, Dir: dir, Now: func() time.Time { return e.now },
		Settings:  func(context.Context) (map[string]string, error) { return e.settings, nil },
		OwnerUser: func(context.Context) (string, error) { return e.label, nil },
		Agents:    func(context.Context) ([]RestartAgent, error) { return e.agents, nil },
	}
}

// hubHdr is what the connector sets for the owner on the update routes.
func hubHdr(user string) map[string]string {
	return map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), tunnel.HubRoleHeader: "owner", tunnel.HubUserHeader: user}
}

func (e *relaunchEnv) do(h http.HandlerFunc, method, path, body string, hdr map[string]string) (int, map[string]any) {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (e *relaunchEnv) status(hdr map[string]string) (int, map[string]any) {
	return e.do(e.h.Status, "GET", "/api/system/update", "", hdr)
}

func (e *relaunchEnv) post(body string, hdr map[string]string) (int, map[string]any) {
	return e.do(e.h.Relaunch, "POST", "/api/system/update/relaunch", body, hdr)
}

func (e *relaunchEnv) token(user string) string {
	e.t.Helper()
	code, st := e.status(hubHdr(user))
	require.Equal(e.t, 200, code)
	rl := st["relaunch"].(map[string]any)
	require.Equal(e.t, true, rl["available"], "%v", rl)
	return rl["ack"].(string)
}

func ack(tok string) string {
	b, _ := json.Marshal(map[string]string{"ack": tok})
	return string(b)
}

func TestRelaunchRoutesAre404WhileTheFlagIsOff(t *testing.T) {
	e := newRelaunchEnv(t)
	e.on = false
	c, _ := e.status(hubHdr(ownerA))
	assert.Equal(t, 404, c)
	c, _ = e.post(`{"ack":"x"}`, hubHdr(ownerA))
	assert.Equal(t, 404, c)
	e.on = true
	e.h.Updater = nil
	c, _ = e.status(hubHdr(ownerA))
	assert.Equal(t, 404, c, "no updater: no routes")
}

func TestRelaunchHappyPathAndStatusShape(t *testing.T) {
	e := newRelaunchEnv(t)
	code, st := e.status(hubHdr(ownerA))
	require.Equal(t, 200, code)
	assert.Equal(t, "1.4.130", st["installed"])
	assert.Equal(t, "staged", st["state"])
	assert.Equal(t, map[string]any{"version": "1.4.131", "staged_at": "2026-10-18T12:00:00Z", "days_ready": float64(2), "verified": true}, st["staged"])
	trust := st["trust"].(map[string]any)
	assert.Equal(t, "signer", trust["signer"])
	assert.EqualValues(t, 42, trust["manifest_sequence"])
	rl := st["relaunch"].(map[string]any)
	assert.Equal(t, true, rl["available"])
	assert.Equal(t, "", rl["reason"])
	assert.EqualValues(t, 60, rl["ack_expires_in"])
	assert.Equal(t, false, rl["tmux_agents_restart"])
	assert.Equal(t, []any{map[string]any{"session_id": "claude-1", "name": "Dev", "state": "idle"}}, rl["restarts"])

	code, out := e.post(ack(rl["ack"].(string)), hubHdr(ownerA))
	require.Equal(t, 202, code, "%v", out)
	assert.Equal(t, map[string]any{"ok": true, "operation": "op-1"}, out)
	assert.Equal(t, []string{ownerA}, e.up.applies)
	require.NotEmpty(t, e.up.audits)
	last := e.up.audits[len(e.up.audits)-1]
	assert.Equal(t, "started", last.Code)
	assert.Equal(t, ownerA, last.By)
	assert.Equal(t, []string{"claude-1"}, last.AgentIDs)
}

func TestStatusNeverHoldsAPathURLHashOrLog(t *testing.T) {
	e := newRelaunchEnv(t)
	e.agents = []RestartAgent{{SessionID: "claude-1", Name: "Dev\x00\u202e" + strings.Repeat("x", 100), State: "<script>"}}
	_, st := e.status(hubHdr(ownerA))
	b, _ := json.Marshal(st)
	rl := st["relaunch"].(map[string]any)
	row := rl["restarts"].([]any)[0].(map[string]any)
	assert.NotContains(t, row, "backend", "a relaunch restarts only pty agents: the field said nothing")
	assert.Equal(t, "working", row["state"], "a state outside the vocabulary is not sent")
	assert.Len(t, []rune(row["name"].(string)), 64, "controls and bidi removed, 64 characters")
	assert.NotContains(t, row["name"], "\u202e")
	for _, bad := range []string{"/tmp", "http://", "sha256", "\\"} {
		assert.NotContains(t, string(b), bad)
	}
}

func TestVersionsOutsideThePatternAreNotSent(t *testing.T) {
	for _, v := range []string{"1.4.131-rc1", "v1.4.131", "1.4", "1.4.131\n", "1234.1.1", ""} {
		e := newRelaunchEnv(t)
		e.up.rec.Staged.Version = v
		_, st := e.status(hubHdr(ownerA))
		assert.Nil(t, st["staged"], v)
		assert.Equal(t, "failed", st["state"], v)
		assert.Equal(t, reasonNothing, st["relaunch"].(map[string]any)["reason"], v)
		e = newRelaunchEnv(t)
		e.up.rec.Installed = v
		_, st = e.status(hubHdr(ownerA))
		assert.Nil(t, st["installed"], v)
	}
}

func TestOwnerChecksFailClosed(t *testing.T) {
	forgedVia := hubHdr(ownerA)
	forgedVia[tunnel.ViaHeader] = "1"
	noVia := hubHdr(ownerA)
	delete(noVia, tunnel.ViaHeader)
	noRole := hubHdr(ownerA)
	delete(noRole, tunnel.HubRoleHeader)
	otherRole := hubHdr(ownerA)
	otherRole[tunnel.HubRoleHeader] = "viewer"
	noUser := hubHdr(ownerA)
	delete(noUser, tunnel.HubUserHeader)
	badUser := hubHdr("someone@example.invalid")
	otherLabel := hubHdr(ownerB)
	for name, hdr := range map[string]map[string]string{
		"forged stamp": forgedVia, "no stamp (a local process sent the marker)": noVia, "no marker (a share grantee)": noRole,
		"another marker": otherRole, "no user": noUser, "malformed user": badUser, "a label other than the enrolled one": otherLabel, "no headers": nil,
	} {
		e := newRelaunchEnv(t)
		code, out := e.post(`{"ack":"x"}`, hdr)
		assert.Equal(t, 403, code, name)
		assert.Equal(t, "owner_only", out["code"], name)
		code, st := e.status(hdr)
		require.Equal(t, 200, code, name)
		rl := st["relaunch"].(map[string]any)
		if hdr[tunnel.ViaHeader] == "" {
			// no tunnel stamp at all: a request that did not come through the Hub is the person at this computer, whatever Hub headers it carries; its token is the local one and the
			// Hub's route (just refused above) does not take it
			assert.Equal(t, true, rl["available"], name)
			code, out := e.post(ack(rl["ack"].(string)), hdr)
			assert.Equal(t, 403, code, name+": a local token is no key to the Hub's route")
			assert.Equal(t, "owner_only", out["code"], name)
		} else {
			assert.Equal(t, false, rl["available"], name)
			assert.Equal(t, "owner_only", rl["reason"], name)
			assert.NotContains(t, rl, "ack", name)
		}
		assert.Zero(t, e.up.applyCount(), name)
	}
	// Two copies of the marker, or of the user, are not one.
	e := newRelaunchEnv(t)
	req := httptest.NewRequest("POST", "/api/system/update/relaunch", strings.NewReader(`{"ack":"x"}`))
	for k, v := range hubHdr(ownerA) {
		req.Header.Set(k, v)
	}
	req.Header.Add(tunnel.HubRoleHeader, "owner")
	rec := httptest.NewRecorder()
	e.h.Relaunch(rec, req)
	assert.Equal(t, 403, rec.Code)

	// An unreadable enrolment record is no owner.
	e = newRelaunchEnv(t)
	e.h.OwnerUser = func(context.Context) (string, error) { return "", errors.New("db closed") }
	code, _ := e.post(`{"ack":"x"}`, hubHdr(ownerA))
	assert.Equal(t, 403, code)
}

func TestAMachineEnrolledWithoutALabelReliesOnTheMarkerAlone(t *testing.T) {
	e := newRelaunchEnv(t)
	e.label = ""
	tok := e.token(ownerA)
	code, _ := e.post(ack(tok), hubHdr(ownerA))
	assert.Equal(t, 202, code)
}

func TestTheCapTheSwitchAndTheOperatorOptOutAreReadOnEveryRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		set    map[string]string
		code   string
		reason string
	}{
		{"cap view", map[string]string{MaxModeSetting: "view"}, "max_mode", reasonMaxMode},
		{"cap interact", map[string]string{MaxModeSetting: "interact"}, "max_mode", reasonMaxMode},
		{"no cap set (view)", map[string]string{}, "max_mode", reasonMaxMode},
		{"junk cap", map[string]string{MaxModeSetting: "root"}, "max_mode", reasonMaxMode},
		{"switch off", map[string]string{MaxModeSetting: "launch", HubRelaunchSetting: "false"}, "hub_relaunch_off", reasonSwitchOff},
		{"Operator opt-out", map[string]string{MaxModeSetting: "launch", HubOperatorDisabledSetting: "true"}, "hub_relaunch_off", reasonSwitchOff},
	} {
		e := newRelaunchEnv(t)
		tok := e.token(ownerA) // issued while allowed
		e.settings = tc.set
		code, out := e.post(ack(tok), hubHdr(ownerA))
		assert.Equal(t, 403, code, tc.name)
		assert.Equal(t, tc.code, out["code"], tc.name)
		_, st := e.status(hubHdr(ownerA))
		assert.Equal(t, tc.reason, st["relaunch"].(map[string]any)["reason"], tc.name)
		assert.NotContains(t, st["relaunch"], "ack", tc.name)
		assert.Zero(t, e.up.applyCount(), tc.name)
	}
	// Settings that cannot be read fail closed.
	e := newRelaunchEnv(t)
	tok := e.token(ownerA)
	e.h.Settings = func(context.Context) (map[string]string, error) { return nil, errors.New("db closed") }
	code, _ := e.post(ack(tok), hubHdr(ownerA))
	assert.Equal(t, 403, code)
}

func TestTheBodyIsStrict(t *testing.T) {
	e := newRelaunchEnv(t)
	tok := e.token(ownerA)
	for name, body := range map[string]string{
		"empty": "", "spaces": "  ", "not json": "x", "array": "[]", "no token": `{}`, "empty token": `{"ack":""}`,
		"version": `{"ack":"` + tok + `","version":"9.9.9"}`, "url": `{"ack":"` + tok + `","url":"http://x"}`,
		"channel": `{"ack":"` + tok + `","channel":"beta"}`, "path": `{"ack":"` + tok + `","path":"/x"}`, "force": `{"ack":"` + tok + `","force":true}`,
		"two objects": ack(tok) + ack(tok), "trailing text": ack(tok) + " x", "oversize": `{"ack":"` + strings.Repeat("a", 2000) + `"}`,
		"wrong type": `{"ack":5}`,
	} {
		code, _ := e.post(body, hubHdr(ownerA))
		assert.Equal(t, 400, code, name)
	}
	assert.Zero(t, e.up.applyCount(), "the updater receives nothing from a bad body")
	code, _ := e.post(ack(tok), hubHdr(ownerA))
	assert.Equal(t, 202, code, "the token was not spent by the bad requests")
}

func TestNothingStagedUnverifiedAndUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mod    func(*UpdaterRecord)
		status int
		code   string
	}{
		{"nothing staged", func(r *UpdaterRecord) { r.Staged, r.State = nil, updNone }, 409, "nothing_staged"},
		{"unverified", func(r *UpdaterRecord) { r.Staged.Verified = false }, 409, "not_verified"},
		{"unsupported install", func(r *UpdaterRecord) { r.Supported = false }, 409, "nothing_staged"},
		{"applying", func(r *UpdaterRecord) { r.State = updApplying }, 409, "busy"},
		{"already relaunched for this version", func(r *UpdaterRecord) { r.HubRelaunched = "1.4.131" }, 409, "already_relaunched"},
		{"a relaunch a moment ago", func(r *UpdaterRecord) { r.LastHubRelaunch = time.Date(2026, 10, 20, 11, 55, 0, 0, time.UTC) }, 429, "too_soon"},
	} {
		e := newRelaunchEnv(t)
		tok := e.token(ownerA)
		tc.mod(&e.up.rec)
		code, out := e.post(ack(tok), hubHdr(ownerA))
		assert.Equal(t, tc.status, code, tc.name)
		assert.Equal(t, tc.code, out["code"], tc.name)
		assert.Zero(t, e.up.applyCount(), tc.name)
		_, st := e.status(hubHdr(ownerA))
		assert.Equal(t, false, st["relaunch"].(map[string]any)["available"], tc.name)
		assert.NotContains(t, st["relaunch"], "ack", tc.name)
	}
	// Ten minutes later it is allowed again for a newer staged version.
	e := newRelaunchEnv(t)
	e.up.rec.LastHubRelaunch = e.now.Add(-11 * time.Minute)
	e.up.rec.HubRelaunched = "1.4.130"
	assert.NotEmpty(t, e.token(ownerA))
}

func TestAStaleTokenGetsTheNewListAndANewToken(t *testing.T) {
	for name, change := range map[string]func(e *relaunchEnv){
		"an agent starts": func(e *relaunchEnv) {
			e.agents = append(e.agents, RestartAgent{SessionID: "claude-2", Name: "QA", State: "working"})
		},
		"an agent's state changes": func(e *relaunchEnv) { e.agents[0].State = "working" },
		"the version changes":      func(e *relaunchEnv) { e.up.rec.Staged.Version = "1.4.132" },
		"tmux agents now restart":  func(e *relaunchEnv) { e.up.rec.TmuxAgentsRestart = true },
		"an agent ends":            func(e *relaunchEnv) { e.agents = nil },
	} {
		e := newRelaunchEnv(t)
		tok := e.token(ownerA)
		change(e)
		code, out := e.post(ack(tok), hubHdr(ownerA))
		assert.Equal(t, 409, code, name)
		assert.Equal(t, "stale_ack", out["code"], name)
		rl := out["relaunch"].(map[string]any)
		fresh := rl["ack"].(string)
		assert.NotEqual(t, tok, fresh, name)
		assert.Zero(t, e.up.applyCount(), name)
		code, out = e.post(ack(fresh), hubHdr(ownerA))
		assert.Equal(t, 202, code, "%s: %v", name, out)
	}
}

func TestTheTokenIsBoundToTheIdentityAndExpires(t *testing.T) {
	// Two owners of a machine enrolled without a label: a token issued to one does not verify for the other.
	e := newRelaunchEnv(t)
	e.label = ""
	tok := e.token(ownerA)
	code, out := e.post(ack(tok), hubHdr(ownerB))
	assert.Equal(t, 409, code)
	assert.Equal(t, "stale_ack", out["code"])
	assert.Zero(t, e.up.applyCount())

	// A token with its payload or MAC changed does not verify.
	e = newRelaunchEnv(t)
	tok = e.token(ownerA)
	pb, mb, _ := strings.Cut(tok, ".")
	for name, bad := range map[string]string{
		"mac flipped": pb + "." + flip(mb), "payload flipped": flip(pb) + "." + mb, "no dot": pb + mb, "empty mac": pb + ".", "garbage": "!!!.!!!",
		"huge": strings.Repeat("a", 600),
	} {
		code, out := e.post(ack(bad), hubHdr(ownerA))
		assert.Equal(t, 409, code, name)
		assert.Equal(t, "stale_ack", out["code"], name)
		assert.Zero(t, e.up.applyCount(), name)
	}

	// Expiry: 60 seconds.
	e = newRelaunchEnv(t)
	tok = e.token(ownerA)
	e.now = e.now.Add(61 * time.Second)
	code, out = e.post(ack(tok), hubHdr(ownerA))
	assert.Equal(t, 409, code)
	assert.Equal(t, "stale_ack", out["code"])
	e = newRelaunchEnv(t)
	tok = e.token(ownerA)
	e.now = e.now.Add(59 * time.Second)
	code, _ = e.post(ack(tok), hubHdr(ownerA))
	assert.Equal(t, 202, code)
}

func flip(s string) string {
	b := []byte(s)
	if b[len(b)/2] == 'A' {
		b[len(b)/2] = 'B'
	} else {
		b[len(b)/2] = 'A'
	}
	return string(b)
}

func TestReusingATokenIsIdempotentAndStartsOneUpdater(t *testing.T) {
	e := newRelaunchEnv(t)
	tok := e.token(ownerA)
	for i := 0; i < 3; i++ {
		code, out := e.post(ack(tok), hubHdr(ownerA))
		assert.Equal(t, 202, code, i)
		assert.Equal(t, "op-1", out["operation"], i)
	}
	assert.Equal(t, 1, e.up.applyCount(), "one start however many posts")
	// A different token while one operation runs: busy.
	e.up.rec.State = updApplying
	code, out := e.post(ack(tok), hubHdr(ownerA))
	assert.Equal(t, 202, code, "the same token still answers with its operation")
	assert.Equal(t, "op-1", out["operation"])
	_, st := e.status(hubHdr(ownerA))
	assert.Equal(t, "applying", st["relaunch"].(map[string]any)["reason"])
	e.up.rec.State = updStaged
}

func TestAUsedTokenStaysUsedAcrossARestart(t *testing.T) {
	e := newRelaunchEnv(t)
	tok := e.token(ownerA)
	code, _ := e.post(ack(tok), hubHdr(ownerA))
	require.Equal(t, 202, code)
	dir := e.h.Dir
	// A new handler on the same folder: a restart.
	e.h = e.build(dir)
	code, out := e.post(ack(tok), hubHdr(ownerA))
	assert.Equal(t, 202, code)
	assert.Equal(t, "op-1", out["operation"])
	assert.Equal(t, 1, e.up.applyCount(), "the used token started nothing again")
	// And for another staged version the old token is spent: it answers its own operation only while it would be valid, never starts.
	e.up.rec.Staged.Version = "1.4.132"
	e.up.rec.HubRelaunched = ""
	code, out = e.post(ack(tok), hubHdr(ownerA))
	assert.Equal(t, 202, code, "repeat of the same token inside its minute")
	assert.Equal(t, 1, e.up.applyCount())
	e.now = e.now.Add(2 * time.Minute)
	code, out = e.post(ack(tok), hubHdr(ownerA))
	assert.Equal(t, 409, code)
	assert.Equal(t, "stale_ack", out["code"], "expired and used: refused")
	assert.Equal(t, 1, e.up.applyCount())
}

func TestStateFilesArePrivateAndAFailedStartKeepsTheToken(t *testing.T) {
	e := newRelaunchEnv(t)
	tok := e.token(ownerA)
	e.up.applyErr = errors.New("no updater")
	code, _ := e.post(ack(tok), hubHdr(ownerA))
	assert.Equal(t, 500, code)
	e.up.applyErr = nil
	code, _ = e.post(ack(tok), hubHdr(ownerA))
	assert.Equal(t, 202, code, "a start that failed did not spend the token")

	for _, f := range []string{ackKeyFile, ackUsedFile} {
		fi, err := os.Stat(filepath.Join(e.h.Dir, f))
		require.NoError(t, err, f)
		if runtime.GOOS != "windows" {
			assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), f)
		}
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(e.h.Dir)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
	}
	// A key readable by others, a damaged key and a damaged used-token record each refuse (no new key, no relaunch).
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Chmod(filepath.Join(e.h.Dir, ackKeyFile), 0o644))
		code, _ := e.status(hubHdr(ownerA))
		assert.Equal(t, 500, code, "a key others can read is refused, not replaced")
	}
}

func TestADamagedKeyOrUsedRecordRefusesTheRelaunch(t *testing.T) {
	e := newRelaunchEnv(t)
	tok := e.token(ownerA)
	require.NoError(t, os.WriteFile(filepath.Join(e.h.Dir, ackUsedFile), []byte("{not json"), 0o600))
	code, _ := e.post(ack(tok), hubHdr(ownerA))
	assert.Equal(t, 500, code)
	assert.Zero(t, e.up.applyCount())
	require.NoError(t, os.WriteFile(filepath.Join(e.h.Dir, ackUsedFile), []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(e.h.Dir, ackKeyFile), []byte("short"), 0o600))
	code, _ = e.post(ack(tok), hubHdr(ownerA))
	assert.Equal(t, 500, code)
	assert.Zero(t, e.up.applyCount())
	b, _ := os.ReadFile(filepath.Join(e.h.Dir, ackKeyFile))
	assert.Equal(t, "short", string(b), "a damaged key is not replaced")
}

func TestEveryAttemptIsAuditedWithoutPersonalData(t *testing.T) {
	e := newRelaunchEnv(t)
	e.post(`{"ack":"x"}`, nil)                   // refused: no owner
	e.post(`{"ack":"x"}`, hubHdr(ownerA))        // stale
	e.post(`{"version":"9"}`, hubHdr(ownerA))    // bad body
	e.post(ack(e.token(ownerA)), hubHdr(ownerA)) // started
	require.Len(t, e.up.audits, 4)
	for i, want := range []string{"owner_only", "stale_ack", "bad_request", "started"} {
		assert.Equal(t, want, e.up.audits[i].Code, i)
	}
	assert.Empty(t, e.up.audits[0].By)
	assert.Equal(t, ownerA, e.up.audits[3].By)
	b, _ := json.Marshal(e.up.audits)
	assert.NotContains(t, string(b), "@")
}

func TestLastResultIsShownOnlyInTheFixedVocabulary(t *testing.T) {
	e := newRelaunchEnv(t)
	e.up.rec.LastResult = &UpdateResult{At: e.now, From: "1.4.130", To: "1.4.131", Result: resultRolledBack, By: "hub:" + ownerA}
	_, st := e.status(hubHdr(ownerA))
	assert.Equal(t, map[string]any{"at": "2026-10-20T12:00:00Z", "from": "1.4.130", "to": "1.4.131", "result": "failed_rolled_back", "by": "hub:" + ownerA}, st["last_result"])
	for _, bad := range []UpdateResult{
		{At: e.now, From: "1.4.130", To: "1.4.131", Result: "exploded", By: "local"},
		{At: e.now, From: "1.4.130", To: "1.4.131", Result: resultRelaunched, By: "hub:person@example.invalid"},
		{At: e.now, From: "1.4.130", To: "evil", Result: resultRelaunched, By: "local"},
	} {
		bad := bad
		e.up.rec.LastResult = &bad
		_, st = e.status(hubHdr(ownerA))
		assert.Nil(t, st["last_result"], "%+v", bad)
	}
}

// A local caller (no tunnel stamp) is not the Hub's owner: the Hub's own route refuses it. Its relaunch is the local route's (relaunch_local_test.go), with a token of its own.
func TestALocalCallerCannotUseTheHubsRelaunchRoute(t *testing.T) {
	e := newRelaunchEnv(t)
	code, out := e.post(`{"ack":"x"}`, nil)
	assert.Equal(t, 403, code)
	assert.Equal(t, "owner_only", out["code"])
	assert.Empty(t, e.up.applies)
}
