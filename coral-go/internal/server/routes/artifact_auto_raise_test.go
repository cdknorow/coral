package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testGiB = int64(1) << 30

// #4602 (the Orchestrator's rule): a Coral with no limit set whose store is already at 80% of the 20 GiB default must not start refusing
// uploads on upgrade. At startup its limit is written once, as twice the store's size rounded up to a whole GiB, with a marker for the
// Settings note. It writes a setting and deletes nothing, never touches a value the Operator chose (0 included), and runs only once.
func TestRaiseArtifactStoreLimitIfOver(t *testing.T) {
	ctx := context.Background()
	stored := func(rig *artifactRig, key string) string {
		s, err := rig.ss.GetSettings(ctx)
		require.NoError(t, err)
		return s[key]
	}
	status := func(rig *artifactRig) map[string]any {
		resp, err := http.Get(rig.srv.URL + "/api/system/artifact-store")
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return out
	}
	fill := func(rig *artifactRig, n int64) {
		st := storeFor(rig.coralDir)
		b, _, _ := st.size()
		st.added(n - b)
	}

	t.Run("raised to twice the size, rounded up, once", func(t *testing.T) {
		rig := newArtifactRig(t)
		fill(rig, 16*testGiB+testGiB/5) // 16.2 GiB: twice is 32.4, so 33
		require.Equal(t, 33*testGiB, RaiseArtifactStoreLimitIfOver(ctx, rig.ss, rig.coralDir))
		assert.Equal(t, strconv.FormatInt(33*testGiB, 10), stored(rig, ArtifactStoreMaxBytesSetting))
		assert.Equal(t, strconv.FormatInt(33*testGiB, 10), stored(rig, ArtifactStoreAutoRaisedSetting))
		st := status(rig)
		assert.EqualValues(t, 33*testGiB, st["max_bytes"], "the store's limit is the raised one")
		assert.EqualValues(t, 33*testGiB, st["auto_raised"], "Settings can say it was raised automatically")
		code, out := rig.upload("s1", "still-works.txt", "", strings.NewReader("an upload after the upgrade"))
		assert.Equal(t, http.StatusCreated, code, "the upgrade did not stop uploads: %v", out)
		assert.Zero(t, RaiseArtifactStoreLimitIfOver(ctx, rig.ss, rig.coralDir), "a second start writes nothing: the limit is set")
	})

	t.Run("exactly 16 GiB counts, whole GiB stay", func(t *testing.T) {
		rig := newArtifactRig(t)
		fill(rig, 16*testGiB)
		assert.Equal(t, 32*testGiB, RaiseArtifactStoreLimitIfOver(ctx, rig.ss, rig.coralDir))
	})

	t.Run("under 80% of the default writes nothing", func(t *testing.T) {
		rig := newArtifactRig(t)
		fill(rig, 16*testGiB-1)
		assert.Zero(t, RaiseArtifactStoreLimitIfOver(ctx, rig.ss, rig.coralDir))
		assert.Empty(t, stored(rig, ArtifactStoreMaxBytesSetting))
		assert.Empty(t, stored(rig, ArtifactStoreAutoRaisedSetting))
		assert.EqualValues(t, defaultArtifactStoreMaxBytes, status(rig)["max_bytes"], "everyone else gets the 20 GiB default")
		assert.NotContains(t, status(rig), "auto_raised")
	})

	t.Run("the Operator's own value is never changed", func(t *testing.T) {
		for _, v := range []string{"0", "5368709120", "99999999999"} {
			rig := newArtifactRig(t)
			require.NoError(t, rig.ss.SetSetting(ctx, ArtifactStoreMaxBytesSetting, v))
			fill(rig, 30*testGiB)
			assert.Zero(t, RaiseArtifactStoreLimitIfOver(ctx, rig.ss, rig.coralDir), v)
			assert.Equal(t, v, stored(rig, ArtifactStoreMaxBytesSetting), "%q stays as the Operator set it", v)
			assert.Empty(t, stored(rig, ArtifactStoreAutoRaisedSetting))
		}
	})

	t.Run("the note goes when the Operator saves another limit, and nothing is deleted", func(t *testing.T) {
		rig := newArtifactRig(t)
		code, _ := rig.upload("s1", "keep.txt", "", strings.NewReader("keep me"))
		require.Equal(t, http.StatusCreated, code)
		fill(rig, 20*testGiB)
		require.Equal(t, 40*testGiB, RaiseArtifactStoreLimitIfOver(ctx, rig.ss, rig.coralDir))
		require.Contains(t, status(rig), "auto_raised")
		require.NoError(t, rig.ss.SetSetting(ctx, ArtifactStoreMaxBytesSetting, strconv.FormatInt(50*testGiB, 10)))
		assert.NotContains(t, status(rig), "auto_raised", "the saved limit is no longer the one Coral wrote")
		_, err := os.Stat(filepath.Join(rig.coralDir, "artifacts", "objects", sha([]byte("keep me"))))
		assert.NoError(t, err, "the stored object is still there")
	})

	t.Run("no clients write the marker", func(t *testing.T) {
		put, _ := settingsClient(t)
		assert.Equal(t, http.StatusBadRequest, put(`{"`+ArtifactStoreAutoRaisedSetting+`": "1"}`))
	})
}
