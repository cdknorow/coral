package routes

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// #4007: an upload the server ACCEPTS (2xx, carries a uri) can never vanish on its own. One
// agent uploads 120 distinct small files; the hourly count cap (100/session) refuses the rest,
// but every accepted artifact still downloads afterward — nothing is evicted to make room, and
// a refusal is never a success-shaped 2xx.
func TestArtifactsAcceptedUploadsNeverVanish(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	rig := newArtifactRig(t)
	var ids []string
	accepted, refused := 0, 0
	for n := 1; n <= 120; n++ {
		body := fmt.Sprintf("artifact %d unique-%d-payload", n, n*911+7)
		code, out := rig.upload("s1", fmt.Sprintf("f%d.txt", n), "text/plain", strings.NewReader(body))
		if code >= 200 && code < 300 {
			accepted++
			id := idOf(out["uri"])
			require.NotEmpty(t, id, "upload %d: a 2xx must carry a coral:// uri", n)
			ids = append(ids, id)
		} else {
			refused++
			require.Empty(t, out["uri"], "upload %d: a refusal (%d) carries no uri", n, code)
		}
	}
	require.Equal(t, 100, accepted, "the first 100 uploads of the session are accepted")
	require.Equal(t, 20, refused, "uploads past the hourly count cap are refused, not lost")

	// The point: every accepted artifact is still downloadable. None was evicted to make room.
	for i, id := range ids {
		code, _, _ := rig.get(id)
		require.Equalf(t, http.StatusOK, code, "accepted artifact #%d (%s) must still download", i+1, id)
	}
}
