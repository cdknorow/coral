package trigger

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

func TestTheStagersNamesAreKept(t *testing.T) {
	assert.Equal(t, stage.StagingDir, stagingDir)
	assert.Equal(t, stage.RecordFile, recordFile)
	// the record's two fields read here are the stager's
	rec := verify.Record{Version: "1.5.0", Asset: "Coral-1.5.0-x64.msi"}
	assert.Equal(t, "1.5.0", rec.Version)
}

func stagedFixture(t *testing.T, dir, version, record, asset string) {
	t.Helper()
	v := filepath.Join(dir, UpdatesDir, stagingDir, version)
	require.NoError(t, os.MkdirAll(v, 0o700))
	if record != "" {
		require.NoError(t, os.WriteFile(filepath.Join(v, recordFile), []byte(record), 0o600))
	}
	if asset != "" {
		require.NoError(t, os.WriteFile(filepath.Join(v, asset), []byte("msi"), 0o600))
	}
}

func TestStagedMSI(t *testing.T) {
	t.Run("one staged MSI", func(t *testing.T) {
		d := t.TempDir()
		stagedFixture(t, d, "1.5.0", `{"version":"1.5.0","asset":"Coral-1.5.0-x64.msi"}`, "Coral-1.5.0-x64.msi")
		v, err := StagedMSI(d)
		require.NoError(t, err)
		assert.Equal(t, "1.5.0", v)
	})
	for name, f := range map[string]func(t *testing.T, d string){
		"nothing": func(t *testing.T, d string) {},
		"a portable zip": func(t *testing.T, d string) {
			stagedFixture(t, d, "1.5.0", `{"version":"1.5.0","asset":"Coral-1.5.0-x64-portable.zip"}`, "Coral-1.5.0-x64-portable.zip")
		},
		"no record": func(t *testing.T, d string) { stagedFixture(t, d, "1.5.0", "", "Coral-1.5.0-x64.msi") },
		"the package is gone": func(t *testing.T, d string) {
			stagedFixture(t, d, "1.5.0", `{"version":"1.5.0","asset":"Coral-1.5.0-x64.msi"}`, "")
		},
		"record of another version": func(t *testing.T, d string) {
			stagedFixture(t, d, "1.5.0", `{"version":"1.6.0","asset":"Coral-1.5.0-x64.msi"}`, "Coral-1.5.0-x64.msi")
		},
		"a path for an asset": func(t *testing.T, d string) {
			stagedFixture(t, d, "1.5.0", `{"version":"1.5.0","asset":"..\\\\x.msi"}`, "")
		},
		"a version that is not one": func(t *testing.T, d string) {
			stagedFixture(t, d, "abc", `{"version":"abc","asset":"x.msi"}`, "x.msi")
		},
		"two versions": func(t *testing.T, d string) {
			stagedFixture(t, d, "1.5.0", `{"version":"1.5.0","asset":"a.msi"}`, "a.msi")
			stagedFixture(t, d, "1.6.0", `{"version":"1.6.0","asset":"b.msi"}`, "b.msi")
		},
		"a partial download only": func(t *testing.T, d string) {
			stagedFixture(t, d, ".1.5.0", `{"version":"1.5.0","asset":"a.msi"}`, "a.msi")
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := t.TempDir()
			f(t, d)
			_, err := StagedMSI(d)
			assert.ErrorIs(t, err, ErrNoStaged)
		})
	}
}
