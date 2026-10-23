//go:build darwin

package apply

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/updater/verify"
)

// #6290: the pinned Developer ID team really is the one that signs Coral's releases. Skipped unless handed a real, released Coral.app (CORAL_REAL_MAC_APP, a copy in a folder of its own)
// and its version (CORAL_REAL_MAC_VERSION). It runs the Mac apply's own checks, from a test and never in a release build: Verify on the real app passes with the pins this build carries,
// the team requirement alone says yes to the real app and no to an ad-hoc-signed copy of it, and a wrong team id is refused for the real app, so it is the pin that decides.
func TestThePinnedTeamIsTheOneThatSignsTheRealApp(t *testing.T) {
	app, version := os.Getenv("CORAL_REAL_MAC_APP"), os.Getenv("CORAL_REAL_MAC_VERSION")
	if app == "" || version == "" {
		t.Skip("set CORAL_REAL_MAC_APP (a copy of a released Coral.app) and CORAL_REAL_MAC_VERSION to run this")
	}
	ctx := context.Background()
	m := &Mac{Run: NewRunner(), TeamIDs: verify.DefaultPins().MacTeamIDs}
	require.NotEmpty(t, m.TeamIDs)

	require.NoError(t, m.Verify(ctx, app, version), "the real app passes the Mac apply's whole check with the pinned team")

	req, err := teamRequirement(m.TeamIDs)
	require.NoError(t, err)
	_, err = m.Run.Run(ctx, codesign, "--verify", "--deep", "--strict", "-R", req, app)
	assert.NoError(t, err, "the pinned team signs the real app")

	adhoc := filepath.Join(t.TempDir(), "Coral.app")
	_, err = m.Run.Run(ctx, "/usr/bin/ditto", app, adhoc)
	require.NoError(t, err)
	_, err = m.Run.Run(ctx, codesign, "--force", "--deep", "-s", "-", adhoc)
	require.NoError(t, err)
	assert.Error(t, m.Verify(ctx, adhoc, version), "an ad-hoc-signed copy fails the whole check")
	_, err = m.Run.Run(ctx, codesign, "--verify", "--deep", "--strict", "-R", req, adhoc)
	assert.Error(t, err, "and fails the team requirement itself")

	wrong := &Mac{Run: m.Run, TeamIDs: []string{"AAAAAAAAAA"}}
	assert.Error(t, wrong.Verify(ctx, app, version), "another team id is refused for the real app: the pin decides")
}
