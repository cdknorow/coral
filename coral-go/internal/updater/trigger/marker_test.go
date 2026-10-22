package trigger

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/stage"
)

func TestUpdatesDirIsTheStagersAndTheTriggersOwnFixedName(t *testing.T) {
	assert.Equal(t, stage.UpdatesDir, UpdatesDir)
}

func TestAMarkerIsGoodForOneConsumeOnly(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	require.NoError(t, WriteMarker(dir, "1.5.0", "", now))
	m, err := Consume(dir, now.Add(5*time.Second))
	require.NoError(t, err)
	assert.Equal(t, "1.5.0", m.Version)
	assert.Len(t, m.Nonce, 32)
	_, err = Consume(dir, now.Add(6*time.Second))
	assert.ErrorIs(t, err, ErrNoMarker, "the second apply finds none")
}

func TestNoMarkerMeansNoApply(t *testing.T) {
	_, err := Consume(t.TempDir(), time.Now())
	assert.ErrorIs(t, err, ErrNoMarker)
}

func TestAnOldMarkerIsRefusedAndDeleted(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	require.NoError(t, WriteMarker(dir, "1.5.0", "", now.Add(-MarkerTTL-time.Second)))
	_, err := Consume(dir, now)
	assert.ErrorIs(t, err, ErrMarkerExpired)
	_, err = Consume(dir, now)
	assert.ErrorIs(t, err, ErrNoMarker, "the leftover is gone")
}

func TestAMarkerFromTheFutureIsRefused(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	require.NoError(t, WriteMarker(dir, "1.5.0", "", now.Add(time.Hour)))
	_, err := Consume(dir, now)
	assert.ErrorIs(t, err, ErrMarkerExpired)
}

func TestADamagedMarkerIsRefusedAndDeleted(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      "nope",
		"no nonce":      `{"version":"1.5.0","issued_at":1}`,
		"short nonce":   `{"nonce":"abc","version":"1.5.0","issued_at":1}`,
		"no version":    `{"nonce":"0123456789abcdef0123456789abcdef","issued_at":1}`,
		"huge":          string(make([]byte, 5000)),
		"empty":         "",
		"wrong shape":   `[1,2,3]`,
		"nonce numeric": `{"nonce":12,"version":"1.5.0","issued_at":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, UpdatesDir), 0o700))
			require.NoError(t, os.WriteFile(markerPath(dir), []byte(body), 0o600))
			_, err := Consume(dir, time.Now())
			assert.ErrorIs(t, err, ErrMarkerInvalid)
			_, err = Consume(dir, time.Now())
			assert.ErrorIs(t, err, ErrNoMarker)
		})
	}
}

func TestAMarkerThatIsAFolderIsRefused(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(markerPath(dir), 0o700))
	_, err := Consume(dir, time.Now())
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoMarker)
}

func TestWritingAMarkerReplacesAnOlderOne(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	require.NoError(t, WriteMarker(dir, "1.4.0", "", now))
	require.NoError(t, WriteMarker(dir, "1.5.0", "", now))
	m, err := Consume(dir, now)
	require.NoError(t, err)
	assert.Equal(t, "1.5.0", m.Version)
}

func TestRemoveMarkerLeavesNone(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteMarker(dir, "1.5.0", "", time.Now()))
	RemoveMarker(dir)
	_, err := Consume(dir, time.Now())
	assert.ErrorIs(t, err, ErrNoMarker)
}
