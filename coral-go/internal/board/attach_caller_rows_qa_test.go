package board

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// QA #7174: the attach entry too must leave the caller's rows as they were (the store stamps its own fields on a copy).
func TestAttachDoesNotWriteToTheCallersArtifactRows(t *testing.T) {
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
	row := patchRow("fix", "d2")
	row.Seq, row.AttachedBy, row.LandedBy, row.LandedAt = 7, "someone", "x", "2026-01-01T00:00:00Z"
	rows := []TaskArtifact{row}
	_, _, err := e.s.AttachPatches(e.ctx, "team", id, "rev1", rows)
	require.NoError(t, err)
	assert.Equal(t, 7, rows[0].Seq, "the caller's Seq is untouched by an attach")
	assert.Equal(t, "someone", rows[0].AttachedBy)
	assert.Equal(t, "x", rows[0].LandedBy)
	assert.Equal(t, "2026-01-01T00:00:00Z", rows[0].LandedAt)
}
