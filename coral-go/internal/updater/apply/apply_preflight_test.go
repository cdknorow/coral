//go:build !windows

package apply

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/updater/stage"
)

// #5419: an update that could end Coral and not bring it back does not begin, and a restart that fails during a rollback is said, not silent.

func TestNothingIsBuiltOrEndedWhenCoralCouldNotBeStartedAgain(t *testing.T) {
	r := newRig(t)
	r.plat.restartWhy = "no_run_record"
	res := r.run()
	assert.Equal(t, ResultUnsupported, res.Result, "%+v", res)
	assert.Equal(t, "no_run_record", res.FailedStage)
	assert.Empty(t, r.plat.calls, "not even a wait for Coral to end: nothing is asked of the platform past the question")
	assert.Equal(t, "old", r.installMarker())
	assert.NotNil(t, r.staged(), "the staged update is kept")
	_, err := os.Stat(filepath.Join(r.data, stage.UpdatesDir, journalFile))
	assert.ErrorIs(t, err, os.ErrNotExist, "no journal: nothing was begun")
}

func TestTheRecordGoingWhileThePackageIsCheckedStopsTheSwapAndRemovesTheCopy(t *testing.T) {
	r := newRig(t)
	r.plat.buildHook = func(string) { r.plat.restartWhy = "no_run_record" } // the record goes while the new install is built and checked
	res := r.run()
	assert.Equal(t, ResultUnsupported, res.Result, "%+v", res)
	assert.Equal(t, "no_run_record", res.FailedStage)
	assert.NotContains(t, r.plat.calls, "swap", "the swap did not happen")
	assert.Equal(t, "old", r.installMarker())
	_, err := os.Stat(filepath.Join(r.plat.root, ".Coral.app.new"))
	assert.ErrorIs(t, err, os.ErrNotExist, "the copy is removed")
}

func TestARestartThatFailsDuringARollbackIsSaidInTheResultAndTheHistory(t *testing.T) {
	r := newRig(t)
	r.plat.startErrs = []error{nil, errors.New("no record of how Coral was started")} // the new one starts, does not stay up; the old one cannot be started
	r.plat.healthErr = errors.New("did not stay up")
	res := r.run()
	assert.Equal(t, ResultRolledBack, res.Result, "%+v", res)
	assert.True(t, res.RestartFailed, "nothing is running: that is in the result")
	assert.Equal(t, "old", r.installMarker(), "the old install is back in place")
	st := r.state()
	require.NotEmpty(t, st.History)
	assert.Equal(t, "restart_failed", st.History[len(st.History)-1].Result)

	// With a restart that works it is the plain roll-back, as before.
	r = newRig(t)
	r.plat.healthErr = errors.New("did not stay up")
	res = r.run()
	assert.Equal(t, ResultRolledBack, res.Result)
	assert.False(t, res.RestartFailed)
	assert.Equal(t, "failed_rolled_back", r.state().History[len(r.state().History)-1].Result)
}

func TestLinuxCanRestartAsksTheRecordTheSameWayStartDoes(t *testing.T) {
	l := newLinux(t)
	install := linuxInstall(t, linuxPrograms, emX86_64)
	l.Manager = func() bool { return true }
	assert.Equal(t, "", l.CanRestart(install), "a service manager restarts Coral itself: no record is needed")

	l.Manager = func() bool { return false }
	assert.Equal(t, "no_run_record", l.CanRestart(install), "no record")
	dir := filepath.Join(l.DataDir, "updates")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	write := func(body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "run.json"), []byte(body), 0o600))
	}
	write(`{"argv":["` + install + `/coral"],"env":["PATH=/usr/bin"],"cwd":"/"}`)
	assert.Equal(t, "", l.CanRestart(install), "a record Start accepts")
	for name, body := range map[string]string{
		"no environment":  `{"argv":["` + install + `/coral"],"cwd":"/"}`,
		"another program": `{"argv":["/bin/sh"],"env":["PATH=/usr/bin"],"cwd":"/"}`,
		"another folder":  `{"argv":["/tmp/x/coral"],"env":["PATH=/usr/bin"],"cwd":"/"}`,
		"only a secret":   `{"argv":["` + install + `/coral"],"env":["ANTHROPIC_API_KEY=k"],"cwd":"/"}`,
		"not json":        `{`,
	} {
		write(body)
		assert.Equal(t, "no_run_record", l.CanRestart(install), name)
		assert.Error(t, l.Start(nil, install), name+": and Start would refuse it too")
	}
}
