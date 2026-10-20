package routes

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/redact"
)

// #5388: the facts the screens and the docs need beyond the design's status, each present only when known and only in its fixed vocabulary.
func TestStatusCarriesNoneOfTheNewFactsWhenTheUpdaterKnowsNone(t *testing.T) {
	e := newRelaunchEnv(t)
	_, st := e.status(hubHdr(ownerA))
	for _, k := range []string{"checked_at", "install_kind", "upgrade_command", "not_ready_reason", "latest", "last_rollback"} {
		assert.NotContains(t, st, k)
	}
}

func TestStatusShowsWhenCoralCheckedAndHowItIsInstalled(t *testing.T) {
	e := newRelaunchEnv(t)
	e.up.rec.CheckedAt = e.now.Add(-time.Hour)
	for _, kind := range []string{"app", "msi", "portable", "tarball", "unwritable"} {
		e.up.rec.InstallKind = kind
		_, st := e.status(hubHdr(ownerA))
		assert.Equal(t, "2026-10-20T11:00:00Z", st["checked_at"])
		assert.Equal(t, kind, st["install_kind"])
		assert.NotContains(t, st, "upgrade_command", kind+": only Homebrew has one")
	}
	e.up.rec.InstallKind = "homebrew"
	_, st := e.status(hubHdr(ownerA))
	assert.Equal(t, "homebrew", st["install_kind"])
	assert.Equal(t, "brew upgrade --cask coral", st["upgrade_command"])

	for _, bad := range []string{"flatpak", "Homebrew", "brew upgrade; rm -rf /", "/Applications/Coral.app", ""} {
		e.up.rec.InstallKind = bad
		_, st = e.status(hubHdr(ownerA))
		assert.NotContains(t, st, "install_kind", bad)
		assert.NotContains(t, st, "upgrade_command", bad)
	}
}

func TestStatusSaysWhyNothingIsReadyOnlyWhenNothingIsStaged(t *testing.T) {
	e := newRelaunchEnv(t)
	e.up.rec.NotReady, e.up.rec.Latest = "not_promoted", "1.4.140"
	_, st := e.status(hubHdr(ownerA))
	assert.NotContains(t, st, "not_ready_reason", "a version is staged: nothing to explain")
	assert.NotContains(t, st, "latest")

	e.up.rec.Staged, e.up.rec.State = nil, updNone
	for r := range notReadyReasons {
		e.up.rec.NotReady = r
		_, st = e.status(hubHdr(ownerA))
		assert.Equal(t, r, st["not_ready_reason"])
		assert.Equal(t, "1.4.140", st["latest"])
	}
	e.up.rec.NotReady, e.up.rec.Latest = "up_to_date", "1.4.140-rc1\n"
	_, st = e.status(hubHdr(ownerA))
	assert.Equal(t, "up_to_date", st["not_ready_reason"])
	assert.NotContains(t, st, "latest", "a version outside the pattern is not sent")
	for _, bad := range []string{"because I said so", "", "Up_To_Date"} {
		e.up.rec.NotReady = bad
		_, st = e.status(hubHdr(ownerA))
		assert.NotContains(t, st, "not_ready_reason", bad)
	}
}

func TestStatusShowsTheLastRollbackInTheFixedVocabulary(t *testing.T) {
	e := newRelaunchEnv(t)
	e.up.rec.LastRollback = &UpdateRollback{At: e.now.Add(-time.Minute), From: "1.4.130", To: "1.4.131", Reason: "did_not_start"}
	_, st := e.status(hubHdr(ownerA))
	assert.Equal(t, map[string]any{"at": "2026-10-20T11:59:00Z", "from": "1.4.130", "to": "1.4.131", "reason": "did_not_start"}, st["last_rollback"])

	e.up.rec.LastRollback.Reason = "the disk at a private path is full"
	_, st = e.status(hubHdr(ownerA))
	assert.Equal(t, "other", st["last_rollback"].(map[string]any)["reason"], "a reason outside the vocabulary is shown as other, never as the updater's words")
	for _, bad := range []UpdateRollback{
		{At: e.now, From: "1.4.130", To: "evil", Reason: "did_not_start"},
		{At: e.now, From: "x", To: "1.4.131", Reason: "did_not_start"},
		{From: "1.4.130", To: "1.4.131", Reason: "did_not_start"},
	} {
		bad := bad
		e.up.rec.LastRollback = &bad
		_, st = e.status(hubHdr(ownerA))
		assert.NotContains(t, st, "last_rollback", "%+v", bad)
	}
}

// Coral Hub's redactor runs over the whole reply (a View route): none of the new values is changed by it, so the hub shows what this computer sent.
func TestTheNewFactsSurviveTheHubRedactor(t *testing.T) {
	e := newRelaunchEnv(t)
	e.up.rec.Staged, e.up.rec.State = nil, updNone
	e.up.rec.CheckedAt, e.up.rec.InstallKind = e.now, "homebrew"
	e.up.rec.NotReady, e.up.rec.Latest = "not_promoted", "1.4.140"
	e.up.rec.LastRollback = &UpdateRollback{At: e.now, From: "1.4.130", To: "1.4.131", Reason: "not_healthy"}
	_, st := e.status(hubHdr(ownerA))
	b, err := json.Marshal(st)
	require.NoError(t, err)
	assert.JSONEq(t, string(b), string(redact.Tokens(b)), "the redactor leaves the status as it is")
}
