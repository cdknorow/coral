package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #6108: a computer linked before Coral recorded the owner label refuses the owner's own requests on the owner-only routes. The refusal says what to do (owner_label_missing), and the person at this
// computer presses Confirm owner, which arms the adoption for a minute: the first owner-only request the Hub stamps for the owner in that minute is adopted as the owner label.

func resetOwnerState(t *testing.T) {
	t.Helper()
	clearAll := func() {
		ownerStamps.Lock()
		ownerStamps.seen = map[string]time.Time{}
		ownerStamps.Unlock()
		ownerArm.Lock()
		ownerArm.until = time.Time{}
		ownerArm.arms = nil
		ownerArm.Unlock()
	}
	clearAll()
	oldNow, oldAdopt := ownerNow, hubOwnerAdopt
	t.Cleanup(func() { clearAll(); ownerNow = oldNow; hubOwnerAdopt = oldAdopt })
}

func decodeStrings(t *testing.T, body string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, json.Unmarshal([]byte(body), &out), body)
	return out
}

func refusalCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	return decodeStrings(t, rec.Body.String())["code"]
}

func TestTheOwnerOfAComputerWithNoRecordedLabelIsToldWhatToDo(t *testing.T) {
	resetOwnerState(t)
	h := customRig(t)

	withLabel(t, "", nil)
	rec := customCall(h, http.MethodGet, "", ownerHeaders(ownerLabel))
	require.Equal(t, "owner_label_missing", refusalCode(t, rec))
	text := decodeStrings(t, rec.Body.String())["error"]
	assert.Contains(t, text, "Confirm owner")
	assert.Contains(t, text, "Settings > Network and Hub")
	assert.NotContains(t, strings.ToLower(text), "link this computer again", "the refusal points at the local fix first; re-linking is the Settings row's fallback")

	// A grantee, an unreadable record and a different recorded account keep the plain refusal.
	assert.Equal(t, "owner_only", refusalCode(t, customCall(h, http.MethodGet, "", granteeHeaders())))
	withLabel(t, "", errors.New("database is locked"))
	assert.Equal(t, "owner_only", refusalCode(t, customCall(h, http.MethodGet, "", ownerHeaders(ownerLabel))))
	withLabel(t, "hub-user-ffff0000", nil)
	assert.Equal(t, "owner_only", refusalCode(t, customCall(h, http.MethodGet, "", ownerHeaders(ownerLabel))))

	withLabel(t, ownerLabel, nil)
	assert.Equal(t, http.StatusOK, customCall(h, http.MethodGet, "", ownerHeaders(ownerLabel)).Code)
}

// fakeOwnerLabels is the enrolment record: the label (empty = none recorded), its server id, and what AdoptOwnerUser does.
type fakeOwnerLabels struct {
	mu      sync.Mutex
	label   string
	server  string
	adopted []string
	at      string
	err     error
}

func (f *fakeOwnerLabels) OwnerUser(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.label, f.err
}

func (f *fakeOwnerLabels) ServerID(context.Context) (string, error) { return f.server, nil }

func (f *fakeOwnerLabels) OwnerAdoptedAt(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.at, nil
}

func (f *fakeOwnerLabels) AdoptOwnerUser(_ context.Context, serverID, user, at string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if serverID != f.server || f.label != "" {
		return false, nil // never over a recorded label
	}
	f.label, f.at = user, at
	f.adopted = append(f.adopted, user)
	return true, nil
}

type ownerLabelEnv struct {
	t     *testing.T
	h     *CloudHandler
	store *fakeOwnerLabels
	now   time.Time
	up    bool
}

func newOwnerLabelEnv(t *testing.T) *ownerLabelEnv {
	t.Helper()
	resetOwnerState(t)
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	e := &ownerLabelEnv{t: t, now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC), up: true}
	h := NewCloudHandler(store.NewSessionStore(db), t.TempDir())
	h.linked = func() bool { return true }
	h.connected = func() bool { return e.up }
	h.SetPeerCheck(func(*http.Request) (bool, bool) { return true, true })
	e.store = &fakeOwnerLabels{server: "srv-1"}
	h.SetOwnerLabelStore(e.store)
	e.h = h
	ownerNow = func() time.Time { return e.now }
	old := hubOwnerLabel
	hubOwnerLabel = e.store.OwnerUser
	t.Cleanup(func() { hubOwnerLabel = old })
	return e
}

func (e *ownerLabelEnv) call(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	dashboardRequest(req)
	rec := httptest.NewRecorder()
	switch {
	case method == http.MethodGet:
		e.h.OwnerLabel(rec, req)
	default:
		e.h.ArmOwnerLabel(rec, req)
	}
	return rec
}

func (e *ownerLabelEnv) arm() *httptest.ResponseRecorder {
	e.t.Helper()
	return e.call(http.MethodPost, "/api/cloud/owner-label/arm", `{}`, nil)
}

// an owner-only request through the hub (the custom models list stands for artifacts, saved and the rest: one owner check), with the given headers.
func (e *ownerLabelEnv) hubOwnerRequest(hdr map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	return customCall(customRig(e.t), http.MethodGet, "", hdr)
}

func TestConfirmOwnerAdoptsTheFirstOwnerRequestInTheMinute(t *testing.T) {
	e := newOwnerLabelEnv(t)

	// Before arming nothing is adopted, and a request is refused with the reason.
	assert.Equal(t, "owner_label_missing", refusalCode(t, e.hubOwnerRequest(ownerHeaders(ownerLabel))))
	assert.Empty(t, e.store.adopted)

	// GET says what is missing and who the Hub last named (so the person knows who Confirm owner will trust).
	rec := e.call(http.MethodGet, "/api/cloud/owner-label", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"label_missing":true`)
	assert.Contains(t, rec.Body.String(), `"candidate":"`+ownerLabel+`"`)

	// Pressing the button arms for a minute.
	rec = e.arm()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"seconds":60`)

	// The first owner-only request adopts and is served as the owner's; a second one finds a label recorded and needs no adoption.
	e.now = e.now.Add(20 * time.Second)
	assert.Equal(t, http.StatusOK, e.hubOwnerRequest(ownerHeaders(ownerLabel)).Code)
	assert.Equal(t, []string{ownerLabel}, e.store.adopted)
	assert.NotEmpty(t, e.store.at, "the adoption is recorded as local, with its time")
	assert.Equal(t, http.StatusOK, e.hubOwnerRequest(ownerHeaders(ownerLabel)).Code)
	assert.Equal(t, []string{ownerLabel}, e.store.adopted)
	assert.False(t, e.h.status().OwnerLabelMissing)
	assert.Equal(t, e.store.at, e.h.status().OwnerAdoptedAt)
}

func TestAnExpiredWindowAdoptsNothing(t *testing.T) {
	e := newOwnerLabelEnv(t)
	require.Equal(t, http.StatusOK, e.arm().Code)
	e.now = e.now.Add(61 * time.Second)
	assert.Equal(t, "owner_label_missing", refusalCode(t, e.hubOwnerRequest(ownerHeaders(ownerLabel))))
	assert.Empty(t, e.store.adopted)
}

func TestTwoStampedRequestsAdoptExactlyTheFirst(t *testing.T) {
	e := newOwnerLabelEnv(t)
	require.Equal(t, http.StatusOK, e.arm().Code)
	// The first request in the window is a grantee's (no owner marker): it adopts nothing and does not use the window up.
	assert.Equal(t, "owner_only", refusalCode(t, e.hubOwnerRequest(granteeHeaders())))
	assert.Empty(t, e.store.adopted)
	// Then two owner-stamped requests with different accounts: the first is adopted, the second is not.
	assert.Equal(t, http.StatusOK, e.hubOwnerRequest(ownerHeaders(ownerLabel)).Code)
	assert.Equal(t, "owner_only", refusalCode(t, e.hubOwnerRequest(ownerHeaders("hub-user-00001111"))), "a label is recorded now: the other account is a stranger")
	assert.Equal(t, []string{ownerLabel}, e.store.adopted)
}

func TestARecordedLabelIsNeverOverwritten(t *testing.T) {
	e := newOwnerLabelEnv(t)
	e.store.label = "hub-user-ffff0000"
	// Arming is refused: a label is recorded.
	rec := e.arm()
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "owner_label_set")
	// Even with the window forced open, a stamped request of another account is refused and the label stays.
	ownerArm.Lock()
	ownerArm.until = e.now.Add(time.Minute)
	ownerArm.Unlock()
	assert.Equal(t, "owner_only", refusalCode(t, e.hubOwnerRequest(ownerHeaders(ownerLabel))))
	assert.Equal(t, "hub-user-ffff0000", e.store.label)
	assert.Empty(t, e.store.adopted)
}

func TestAForgedOwnerHeaderFromNotTheTunnelAdoptsNothing(t *testing.T) {
	e := newOwnerLabelEnv(t)
	require.Equal(t, http.StatusOK, e.arm().Code)
	// The owner headers without the connector's secret (a request that did not come through the tunnel, or a forged stamp).
	forged := map[string]string{"X-Coral-Hub-Role": "owner", "X-Coral-Hub-User": ownerLabel}
	customCall(customRig(t), http.MethodGet, "", forged) // served as a local call (it is not the tunnel's), and it is not an owner request
	assert.Empty(t, e.store.adopted)
	// the window was not used up by it: the real request still adopts
	assert.Equal(t, http.StatusOK, e.hubOwnerRequest(ownerHeaders(ownerLabel)).Code)
	assert.Equal(t, []string{ownerLabel}, e.store.adopted)
}

func TestARestartBetweenArmingAndTheRequestAdoptsNothing(t *testing.T) {
	e := newOwnerLabelEnv(t)
	require.Equal(t, http.StatusOK, e.arm().Code)
	resetOwnerState(t) // the process restarted: the window lived in memory only
	ownerNow = func() time.Time { return e.now }
	e.h.SetOwnerLabelStore(e.store)
	assert.Equal(t, "owner_label_missing", refusalCode(t, e.hubOwnerRequest(ownerHeaders(ownerLabel))))
	assert.Empty(t, e.store.adopted)
}

func TestNothingIsAdoptedWhenTheTunnelIsNotToTheEnrolledHub(t *testing.T) {
	e := newOwnerLabelEnv(t)
	require.Equal(t, http.StatusOK, e.arm().Code)
	e.up = false // the hub the connector reaches is not the enrolled one (or it dropped)
	assert.Equal(t, "owner_label_missing", refusalCode(t, e.hubOwnerRequest(ownerHeaders(ownerLabel))))
	assert.Empty(t, e.store.adopted)
	// and arming is refused outright while it is not
	assert.Equal(t, http.StatusConflict, e.arm().Code)
}

func TestConfirmOwnerIsRateLimitedAndLocalOnly(t *testing.T) {
	e := newOwnerLabelEnv(t)
	for i := 0; i < 3; i++ {
		require.Equal(t, http.StatusOK, e.arm().Code, i)
		e.now = e.now.Add(2 * time.Minute)
	}
	rec := e.arm()
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, "a fourth press within the hour")
	assert.Contains(t, rec.Body.String(), "too_soon")
	e.now = e.now.Add(time.Hour)
	assert.Equal(t, http.StatusOK, e.arm().Code, "after the hour")

	// Through Coral Hub the route is refused and shows nothing; a Bearer proxy is not a browser; a body other than {} is a 400.
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		rec := e.call(m, "/api/cloud/owner-label/arm", `{}`, ownerHeaders(ownerLabel))
		assert.Equal(t, http.StatusForbidden, rec.Code, m)
		assert.NotContains(t, rec.Body.String(), ownerLabel, m)
		rec = e.call(m, "/api/cloud/owner-label/arm", `{}`, map[string]string{"Authorization": "Bearer proxy"})
		assert.Equal(t, http.StatusForbidden, rec.Code, m)
	}
	assert.Equal(t, http.StatusBadRequest, e.call(http.MethodPost, "/api/cloud/owner-label/arm", `{"user":"`+ownerLabel+`"}`, nil).Code)
	e.store.err = errors.New("locked")
	assert.Equal(t, http.StatusInternalServerError, e.call(http.MethodGet, "/api/cloud/owner-label", "", nil).Code)
}

// One chance: a request that fails to adopt (the tunnel is not to the enrolled hub) still uses the window up, so a later request adopts nothing.
func TestAFailedAdoptionUsesTheWindowUp(t *testing.T) {
	e := newOwnerLabelEnv(t)
	require.Equal(t, http.StatusOK, e.arm().Code)
	e.up = false
	assert.Equal(t, "owner_label_missing", refusalCode(t, e.hubOwnerRequest(ownerHeaders(ownerLabel))))
	e.up = true
	assert.Equal(t, "owner_label_missing", refusalCode(t, e.hubOwnerRequest(ownerHeaders(ownerLabel))), "the window was used up by the first request")
	assert.Empty(t, e.store.adopted)
}

// #6186: only a read adopts. A write through the hub while armed is refused as before and leaves the window for the read that follows.
func TestOnlyAReadAdoptsAndAWriteDoesNotUseTheWindowUp(t *testing.T) {
	e := newOwnerLabelEnv(t)
	require.Equal(t, http.StatusOK, e.arm().Code)
	rec := customCall(customRig(t), http.MethodPut, oneCustomModel, ownerHeaders(ownerLabel))
	assert.Equal(t, "owner_label_missing", refusalCode(t, rec), "a write is refused, not adopted")
	assert.Empty(t, e.store.adopted)
	assert.Equal(t, http.StatusOK, e.hubOwnerRequest(ownerHeaders(ownerLabel)).Code, "the read after it still adopts: the write did not use the window up")
	assert.Equal(t, []string{ownerLabel}, e.store.adopted)
}

// #6186: when more than one hub account was stamped as the owner in the last hour, Confirm owner is refused with a plain message and nothing is adopted, even if it was armed earlier.
func TestSeveralStampedHubUsersRefuseToArmAndToAdopt(t *testing.T) {
	e := newOwnerLabelEnv(t)
	other := "hub-user-00001111"
	require.NotEqual(t, ownerLabel, other)
	// a first account is stamped, then a second one
	assert.Equal(t, "owner_label_missing", refusalCode(t, e.hubOwnerRequest(ownerHeaders(ownerLabel))))
	assert.Equal(t, "owner_label_missing", refusalCode(t, e.hubOwnerRequest(ownerHeaders(other))))
	rec := e.arm()
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "several_hub_users")
	assert.Contains(t, rec.Body.String(), "More than one Coral Hub account tried this; check who has access, then try again")
	assert.Empty(t, e.store.adopted)
	// #6186: it says when to try again: the stamps are at most an hour old, and it clears when the older of the two has aged out
	var body struct {
		RetryAfter int `json:"retry_after"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, 3600+1, body.RetryAfter, "both stamps were made at this instant: one hour, rounded up")
	assert.Equal(t, "3601", rec.Header().Get("Retry-After"))
	e.now = e.now.Add(40 * time.Minute)
	rec = e.arm()
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, 20*60+1, body.RetryAfter, "forty minutes later: twenty minutes are left")
	e.now = e.now.Add(21 * time.Minute)
	assert.Equal(t, http.StatusOK, e.arm().Code, "once the older stamp has aged out only one account is left, and Confirm owner works")

	// armed with one account stamped, a second account stamped afterwards: the request that would adopt is refused and the label stays empty
	e2 := newOwnerLabelEnv(t)
	assert.Equal(t, "owner_label_missing", refusalCode(t, e2.hubOwnerRequest(ownerHeaders(ownerLabel))))
	require.Equal(t, http.StatusOK, e2.arm().Code)
	assert.Equal(t, "owner_label_missing", refusalCode(t, e2.hubOwnerRequest(ownerHeaders(other))), "two accounts in the hour: nothing is adopted")
	assert.Empty(t, e2.store.adopted)
}
