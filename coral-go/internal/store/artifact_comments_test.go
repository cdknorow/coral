package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	artA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	artB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func newCommentStore(t *testing.T) *SessionStore {
	t.Helper()
	return NewSessionStore(openTestDB(t))
}

func mk(art, board, body string, parent *int64) ArtifactComment {
	return ArtifactComment{ArtifactID: art, Board: board, ParentID: parent, AnchorJSON: `{"quote":"q"}`, Body: body, AuthorIssuer: "local", AuthorSubject: "operator", AuthorKind: "owner"}
}

func TestArtifactCommentsCreateListAndOneLevelOfReplies(t *testing.T) {
	ctx := context.Background()
	s := newCommentStore(t)
	top, err := s.CreateArtifactComment(ctx, mk(artA, "team-a", "  first  ", nil))
	require.NoError(t, err)
	assert.Equal(t, "first", top.Body, "the body is trimmed")
	assert.NotEmpty(t, top.CreatedAt)
	assert.Nil(t, top.ResolvedAt)
	reply, err := s.CreateArtifactComment(ctx, mk(artA, "team-a", "a reply", &top.ID))
	require.NoError(t, err)
	_, err = s.CreateArtifactComment(ctx, mk(artA, "team-a", "a reply to a reply", &reply.ID))
	assert.ErrorIs(t, err, ErrBadParent, "replies go one level deep")
	_, err = s.CreateArtifactComment(ctx, mk(artB, "team-a", "a reply on another artifact", &top.ID))
	assert.ErrorIs(t, err, ErrBadParent, "a reply stays on its parent's artifact")
	_, err = s.CreateArtifactComment(ctx, mk(artA, "team-a", "no such parent", func() *int64 { n := int64(9999); return &n }()))
	assert.ErrorIs(t, err, ErrBadParent)
	got, err := s.ListArtifactComments(ctx, artA, "", nil)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []int64{top.ID, reply.ID}, []int64{got[0].ID, got[1].ID}, "oldest first, a thread's reply after its comment")
	other, err := s.ListArtifactComments(ctx, artB, "", nil)
	require.NoError(t, err)
	assert.Empty(t, other, "another artifact has none")
}

func TestArtifactCommentsRefuseEmptyAndOversizedText(t *testing.T) {
	ctx := context.Background()
	s := newCommentStore(t)
	_, err := s.CreateArtifactComment(ctx, mk(artA, "", "   \n ", nil))
	assert.ErrorIs(t, err, ErrCommentEmpty)
	_, err = s.CreateArtifactComment(ctx, mk(artA, "", strings.Repeat("x", MaxArtifactCommentBody+1), nil))
	assert.ErrorIs(t, err, ErrCommentTooLong)
	ok, err := s.CreateArtifactComment(ctx, mk(artA, "", strings.Repeat("x", MaxArtifactCommentBody), nil))
	require.NoError(t, err, "exactly the limit is fine")
	c := mk(artA, "", "body", nil)
	c.AnchorJSON = strings.Repeat("a", MaxArtifactCommentAnchor+1)
	_, err = s.CreateArtifactComment(ctx, c)
	assert.ErrorIs(t, err, ErrCommentTooLong, "an anchor over its limit")
	_, _, err = s.EditArtifactComment(ctx, artA, ok.ID, strings.Repeat("y", MaxArtifactCommentBody+1))
	assert.ErrorIs(t, err, ErrCommentTooLong)
	_, _, err = s.EditArtifactComment(ctx, artA, ok.ID, " ")
	assert.ErrorIs(t, err, ErrCommentEmpty)
}

func TestArtifactCommentsAreCappedPerArtifact(t *testing.T) {
	ctx := context.Background()
	s := newCommentStore(t)
	var first ArtifactComment
	for i := 0; i < MaxArtifactComments; i++ {
		c, err := s.CreateArtifactComment(ctx, mk(artA, "", "c", nil))
		require.NoError(t, err)
		if i == 0 {
			first = c
		}
	}
	_, err := s.CreateArtifactComment(ctx, mk(artA, "", "one too many", nil))
	assert.ErrorIs(t, err, ErrTooManyComments)
	_, err = s.CreateArtifactComment(ctx, mk(artB, "", "another artifact is not affected", nil))
	assert.NoError(t, err)
	ok, err := s.DeleteArtifactComment(ctx, artA, first.ID)
	require.NoError(t, err)
	assert.True(t, ok)
	_, err = s.CreateArtifactComment(ctx, mk(artA, "", "room again once one is deleted", nil))
	assert.NoError(t, err, "a deleted comment no longer counts")
}

func TestArtifactCommentsEditResolveAndTombstone(t *testing.T) {
	ctx := context.Background()
	s := newCommentStore(t)
	top, _ := s.CreateArtifactComment(ctx, mk(artA, "", "top", nil))
	reply, _ := s.CreateArtifactComment(ctx, mk(artA, "", "reply", &top.ID))
	edited, ok, err := s.EditArtifactComment(ctx, artA, top.ID, "top, edited")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "top, edited", edited.Body)
	assert.NotNil(t, edited.EditedAt)
	_, ok, _ = s.EditArtifactComment(ctx, artB, top.ID, "wrong artifact")
	assert.False(t, ok, "a comment is edited only through its own artifact")
	res, ok, err := s.SetArtifactCommentResolved(ctx, artA, top.ID, true, "coral-hub", "hub-user-3f2a")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, res.ResolvedAt)
	assert.Equal(t, "hub-user-3f2a", *res.ResolvedSubject)
	assert.Equal(t, "coral-hub", *res.ResolvedIssuer)
	_, ok, _ = s.SetArtifactCommentResolved(ctx, artA, reply.ID, true, "x", "y")
	assert.False(t, ok, "only a thread's top comment is resolved")
	open, _ := s.ListArtifactComments(ctx, artA, "open", nil)
	done, _ := s.ListArtifactComments(ctx, artA, "resolved", nil)
	assert.Empty(t, open, "a resolved thread leaves the open list, replies with it")
	assert.Len(t, done, 2)
	reopened, _, _ := s.SetArtifactCommentResolved(ctx, artA, top.ID, false, "local", "operator")
	assert.Nil(t, reopened.ResolvedAt)
	assert.Nil(t, reopened.ResolvedSubject)

	// Deleting the top comment keeps the thread: a tombstone with no text, and the reply stays.
	ok, err = s.DeleteArtifactComment(ctx, artA, top.ID)
	require.NoError(t, err)
	require.True(t, ok)
	got, _ := s.ListArtifactComments(ctx, artA, "", nil)
	require.Len(t, got, 2)
	assert.Equal(t, "", got[0].Body)
	assert.NotNil(t, got[0].DeletedAt)
	assert.Equal(t, "", got[0].AnchorJSON, "the anchor's quote goes with the text")
	assert.Equal(t, "reply", got[1].Body)
	_, err = s.CreateArtifactComment(ctx, mk(artA, "", "reply to a deleted comment", &top.ID))
	assert.ErrorIs(t, err, ErrBadParent)
	ok, _ = s.DeleteArtifactComment(ctx, artA, top.ID)
	assert.False(t, ok, "deleting twice does nothing")
	_, ok, _ = s.EditArtifactComment(ctx, artA, top.ID, "back from the dead")
	assert.False(t, ok, "a tombstone is not edited")
	// A deleted comment with no replies disappears from the list.
	_, _ = s.DeleteArtifactComment(ctx, artA, reply.ID)
	got, _ = s.ListArtifactComments(ctx, artA, "", nil)
	assert.Empty(t, got)
}

func TestArtifactCommentsListByBoard(t *testing.T) {
	ctx := context.Background()
	s := newCommentStore(t)
	_, _ = s.CreateArtifactComment(ctx, mk(artA, "team-a", "on a", nil))
	_, _ = s.CreateArtifactComment(ctx, mk(artA, "team-b", "on b", nil))
	_, _ = s.CreateArtifactComment(ctx, mk(artA, "", "on none", nil))
	a, _ := s.ListArtifactComments(ctx, artA, "", []string{"team-a"})
	require.Len(t, a, 1)
	assert.Equal(t, "on a", a[0].Body)
	ab, _ := s.ListArtifactComments(ctx, artA, "", []string{"team-a", "team-b"})
	assert.Len(t, ab, 2)
	none, _ := s.ListArtifactComments(ctx, artA, "", []string{})
	assert.Empty(t, none, "an empty list of boards sees nothing, not everything")
	all, _ := s.ListArtifactComments(ctx, artA, "", nil)
	assert.Len(t, all, 3, "nil boards is everything (the owner)")
}

func TestArtifactCommentLocksAndCascade(t *testing.T) {
	ctx := context.Background()
	s := newCommentStore(t)
	locked, err := s.ArtifactCommentsLocked(ctx, artA, "team-a")
	require.NoError(t, err)
	assert.False(t, locked)
	require.NoError(t, s.SetArtifactCommentsLocked(ctx, "artifact", artA, true))
	locked, _ = s.ArtifactCommentsLocked(ctx, artA, "team-a")
	assert.True(t, locked)
	other, _ := s.ArtifactCommentsLocked(ctx, artB, "team-a")
	assert.False(t, other, "the lock is that artifact's")
	require.NoError(t, s.SetArtifactCommentsLocked(ctx, "artifact", artA, false))
	require.NoError(t, s.SetArtifactCommentsLocked(ctx, "board", "team-a", true))
	locked, _ = s.ArtifactCommentsLocked(ctx, artB, "team-a")
	assert.True(t, locked, "a board lock covers every artifact commented on that board")
	noBoard, _ := s.ArtifactCommentsLocked(ctx, artB, "")
	assert.False(t, noBoard, "and not a comment with no board")
	assert.Error(t, s.SetArtifactCommentsLocked(ctx, "everything", "x", true))

	require.NoError(t, s.SetArtifactCommentsLocked(ctx, "artifact", artA, true))
	_, _ = s.CreateArtifactComment(ctx, mk(artA, "", "one", nil))
	_, _ = s.CreateArtifactComment(ctx, mk(artA, "", "two", nil))
	_, _ = s.CreateArtifactComment(ctx, mk(artB, "", "stays", nil))
	n, err := s.DeleteArtifactComments(ctx, artA)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	gone, _ := s.ListArtifactComments(ctx, artA, "", nil)
	assert.Empty(t, gone)
	kept, _ := s.ListArtifactComments(ctx, artB, "", nil)
	assert.Len(t, kept, 1, "the other artifact's comments stay")
	locked, _ = s.ArtifactCommentsLocked(ctx, artA, "")
	assert.False(t, locked, "deleting an artifact's comments drops its own lock")
}

// The list is in the order the comments were written, whatever their created_at text sorts as: RFC3339Nano drops trailing zeros, so a comment at 120 ms has a later-sorting string than one at 123 ms.
func TestArtifactCommentsListInWriteOrderWhateverTheirTimestampsSortAs(t *testing.T) {
	ctx := context.Background()
	s := newCommentStore(t)
	first, _ := s.CreateArtifactComment(ctx, mk(artA, "", "first", nil))
	second, _ := s.CreateArtifactComment(ctx, mk(artA, "", "second", nil))
	_, err := s.DB().ExecContext(ctx, `UPDATE artifact_comments SET created_at = ? WHERE id = ?`, "2026-10-22T20:00:00.12Z", first.ID)
	require.NoError(t, err)
	_, err = s.DB().ExecContext(ctx, `UPDATE artifact_comments SET created_at = ? WHERE id = ?`, "2026-10-22T20:00:00.123Z", second.ID)
	require.NoError(t, err)
	got, err := s.ListArtifactComments(ctx, artA, "", nil)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []string{"first", "second"}, []string{got[0].Body, got[1].Body})
}
