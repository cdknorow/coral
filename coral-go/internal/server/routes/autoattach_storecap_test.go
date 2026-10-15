package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// QA, #3553 review: auto-attach at the artifact store's cap (artifact_store_max_bytes) skips the
// file, leaves the post's link as written, never fails the post, and resumes once there is room.
func TestAutoAttachSkipsAtTheStoreCap(t *testing.T) {
	rig := newAttachRig(t)
	rig.write("first.png", pngBytes(t, 4, 4))
	rig.write("second.png", pngBytes(t, 6, 6))

	// Room for the first image only.
	st := storeFor(rig.coralDir)
	first := rig.post("![a](first.png)")
	require.Regexp(t, coralLinkRe, first, "the first image is attached while there is room")
	used, _, _ := st.size()
	st.mu.Lock()
	st.maxBytes = func() int64 { return used }
	st.mu.Unlock()

	// At the cap: the link is left as written; the post itself goes through unchanged.
	unchanged(t, rig, "![b](second.png)", "the store is at its cap")
	unchanged(t, rig, "![b](second.png)", "still at its cap, and no failure")

	// A duplicate of an object already stored costs nothing, so it should still attach.
	dup := rig.post("![a again](first.png)")
	assert.Regexp(t, coralLinkRe, dup, "a duplicate is free even at the cap (design: never charged, never refused)")

	// The Operator raises the cap: auto-attach resumes.
	st.mu.Lock()
	st.maxBytes = func() int64 { return 0 }
	st.mu.Unlock()
	assert.Regexp(t, coralLinkRe, rig.post("![b](second.png)"), "room again: attached")
}
