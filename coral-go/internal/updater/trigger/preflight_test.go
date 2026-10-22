package trigger

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Preflight is the server's question to the updater before it quits (#5946): only an updater that would install gets the server's quit.
func TestPreflightAnswers(t *testing.T) {
	old := runStatus
	t.Cleanup(func() { runStatus = old })
	for name, tc := range map[string]struct {
		answer string
		runErr error
		want   error
	}{
		"it would install":                 {`{"feature":"on","apply_enabled":true,"install":"supported"}`, nil, nil},
		"the apply is off (today)":         {`{"feature":"on","apply_enabled":false,"install":"supported"}`, nil, ErrApplyDisabled},
		"updates off for the build tier":   {`{"feature":"off","off_reason":"build_tier","apply_enabled":true,"install":"supported"}`, nil, ErrUpdatesOff},
		"updates off by the environment":   {`{"feature":"off","off_reason":"env_switch","apply_enabled":true,"install":"supported"}`, nil, ErrUpdatesOff},
		"off wins over the apply switch":   {`{"feature":"off","apply_enabled":false,"install":"portable_install"}`, nil, ErrUpdatesOff},
		"a portable copy":                  {`{"feature":"on","apply_enabled":true,"install":"portable_install"}`, nil, ErrInstallUnsupported},
		"no registered product":            {`{"feature":"on","apply_enabled":true,"install":"no_registered_product"}`, nil, ErrInstallUnsupported},
		"an unreadable install":            {`{"feature":"on","apply_enabled":true,"install":"unreadable_install"}`, nil, ErrInstallUnsupported},
		"an unknown word is not supported": {`{"feature":"on","apply_enabled":true,"install":"yes"}`, nil, ErrInstallUnsupported},
		"an older updater with no install": {`{"feature":"on","apply_enabled":true}`, nil, ErrUpdaterUnavailable},
		"no apply_enabled at all":          {`{"feature":"on","install":"supported"}`, nil, ErrUpdaterUnavailable},
		"not JSON":                         {`Coral updater`, nil, ErrUpdaterUnavailable},
		"empty":                            {``, nil, ErrUpdaterUnavailable},
		"too long":                         {`{"feature":"on","apply_enabled":true,"install":"supported","x":"` + strings.Repeat("a", maxStatusBytes) + `"}`, nil, ErrUpdaterUnavailable},
		"it could not run":                 {``, errors.New("exec: not found"), ErrUpdaterUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			runStatus = func(context.Context, string) ([]byte, error) { return []byte(tc.answer), tc.runErr }
			err := Preflight(context.Background(), t.TempDir())
			if tc.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.want)
			for _, other := range []error{ErrApplyDisabled, ErrUpdatesOff, ErrInstallUnsupported, ErrUpdaterUnavailable} {
				if other != tc.want {
					assert.NotErrorIs(t, err, other, "one cause, one code")
				}
			}
		})
	}
}

func TestTheMarkerNamesTheServersFolder(t *testing.T) {
	dir := t.TempDir()
	install := t.TempDir()
	now := time.Now()
	require.NoError(t, WriteMarker(dir, "1.5.0", install, now))
	m, err := Consume(dir, now)
	require.NoError(t, err)
	assert.Equal(t, install, m.InstallDir)
}

// A folder that is not an absolute path is no folder to start Coral from: the marker is refused (and gone), never half-used.
func TestAMarkerWithARelativeInstallFolderIsRefused(t *testing.T) {
	for _, install := range []string{"coral", `..\x`, "./a", strings.Repeat("a", 2000)} {
		dir := t.TempDir()
		require.NoError(t, WriteMarker(dir, "1.5.0", install, time.Now()))
		_, err := Consume(dir, time.Now())
		assert.ErrorIs(t, err, ErrMarkerInvalid, install)
		_, err = Consume(dir, time.Now())
		assert.ErrorIs(t, err, ErrNoMarker, "and it is gone")
	}
}
