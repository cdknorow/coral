package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Comments on an artifact (#5176, phase 1: comments and permissions; sending a comment to an agent is phase 2 and adds its own table keyed by the comment id).

// Limits the store enforces whatever calls it: a comment is text, bounded.
const (
	MaxArtifactCommentBody   = 8 << 10 // bytes
	MaxArtifactCommentAnchor = 4 << 10 // bytes of anchor_json (the route caps the quote at 2 KB inside it)
	MaxArtifactComments      = 500     // live comments (replies included) on one artifact
)

var (
	// ErrCommentTooLong is a body or an anchor over its limit.
	ErrCommentTooLong = errors.New("comment too long")
	// ErrCommentEmpty is a body with nothing in it.
	ErrCommentEmpty = errors.New("comment is empty")
	// ErrTooManyComments is the artifact already holding MaxArtifactComments.
	ErrTooManyComments = errors.New("too many comments on this artifact")
	// ErrBadParent is a reply to a comment that does not exist on this artifact, is deleted, or is itself a reply (one level only).
	ErrBadParent = errors.New("cannot reply to that comment")
)

// ArtifactComment is one comment (or reply) on an artifact.
type ArtifactComment struct {
	ID         int64  `db:"id" json:"id"`
	ArtifactID string `db:"artifact_id" json:"artifact_id"`
	Board      string `db:"board" json:"board"`
	ParentID   *int64 `db:"parent_id" json:"parent_id"`
	AnchorJSON string `db:"anchor_json" json:"-"`
	Body       string `db:"body" json:"body"`
	// The author is (AuthorIssuer, AuthorSubject); ownership is a comparison of the two. AuthorName is for display only.
	AuthorIssuer    string  `db:"author_issuer" json:"-"`
	AuthorSubject   string  `db:"author_subject" json:"-"`
	AuthorName      string  `db:"author_name" json:"-"`
	AuthorKind      string  `db:"author_kind" json:"-"`
	CreatedAt       string  `db:"created_at" json:"created_at"`
	EditedAt        *string `db:"edited_at" json:"edited_at"`
	ResolvedIssuer  *string `db:"resolved_by_issuer" json:"-"`
	ResolvedSubject *string `db:"resolved_by_subject" json:"-"`
	ResolvedAt      *string `db:"resolved_at" json:"resolved_at"`
	DeletedAt       *string `db:"deleted_at" json:"deleted_at"`
}

const artifactCommentCols = "id, artifact_id, board, parent_id, anchor_json, body, author_issuer, author_subject, author_name, author_kind, created_at, edited_at, resolved_by_issuer, resolved_by_subject, resolved_at, deleted_at"

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// CreateArtifactComment adds a comment, or a reply when c.ParentID is set. The id, the time and the cleared state are the store's; the caller decides the author and the board.
func (s *SessionStore) CreateArtifactComment(ctx context.Context, c ArtifactComment) (ArtifactComment, error) {
	body := strings.TrimSpace(c.Body)
	if body == "" {
		return ArtifactComment{}, ErrCommentEmpty
	}
	if len(body) > MaxArtifactCommentBody || len(c.AnchorJSON) > MaxArtifactCommentAnchor {
		return ArtifactComment{}, ErrCommentTooLong
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return ArtifactComment{}, err
	}
	defer tx.Rollback() //nolint:errcheck // a committed transaction has nothing to roll back
	if c.ParentID != nil {
		var p struct {
			Parent  *int64  `db:"parent_id"`
			Deleted *string `db:"deleted_at"`
		}
		err := tx.GetContext(ctx, &p, "SELECT parent_id, deleted_at FROM artifact_comments WHERE id = ? AND artifact_id = ?", *c.ParentID, c.ArtifactID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (p.Parent != nil || p.Deleted != nil)) {
			return ArtifactComment{}, ErrBadParent
		}
		if err != nil {
			return ArtifactComment{}, err
		}
	}
	var n int
	if err := tx.GetContext(ctx, &n, "SELECT COUNT(*) FROM artifact_comments WHERE artifact_id = ? AND deleted_at IS NULL", c.ArtifactID); err != nil {
		return ArtifactComment{}, err
	}
	if n >= MaxArtifactComments {
		return ArtifactComment{}, ErrTooManyComments
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO artifact_comments (artifact_id, board, parent_id, anchor_json, body, author_issuer, author_subject, author_name, author_kind, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ArtifactID, c.Board, c.ParentID, c.AnchorJSON, body, c.AuthorIssuer, c.AuthorSubject, c.AuthorName, c.AuthorKind, nowStamp())
	if err != nil {
		return ArtifactComment{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ArtifactComment{}, err
	}
	var out ArtifactComment
	if err := tx.GetContext(ctx, &out, "SELECT "+artifactCommentCols+" FROM artifact_comments WHERE id = ?", id); err != nil {
		return ArtifactComment{}, err
	}
	return out, tx.Commit()
}

// GetArtifactComment returns one comment of an artifact (deleted ones included, so a route can answer "gone"); ok is false when there is none.
func (s *SessionStore) GetArtifactComment(ctx context.Context, artifactID string, id int64) (ArtifactComment, bool, error) {
	var out ArtifactComment
	err := s.reader().GetContext(ctx, &out, "SELECT "+artifactCommentCols+" FROM artifact_comments WHERE id = ? AND artifact_id = ?", id, artifactID)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactComment{}, false, nil
	}
	return out, err == nil, err
}

// ListArtifactComments returns an artifact's comments oldest first. state is "open", "resolved" or "" (all): it picks threads by their top comment, and a thread's replies come with it.
// A deleted comment is listed only as a tombstone (empty body) when it still has replies. boards, when not nil, keeps only comments made on those boards (an agent sees its own).
func (s *SessionStore) ListArtifactComments(ctx context.Context, artifactID, state string, boards []string) ([]ArtifactComment, error) {
	q := "SELECT " + artifactCommentCols + " FROM artifact_comments WHERE artifact_id = ?"
	args := []any{artifactID}
	if boards != nil {
		if len(boards) == 0 {
			return []ArtifactComment{}, nil
		}
		q += " AND board IN (" + strings.TrimSuffix(strings.Repeat("?,", len(boards)), ",") + ")"
		for _, b := range boards {
			args = append(args, b)
		}
	}
	// In the order they were written. Not by created_at: it is RFC3339Nano text, which drops trailing zeros ("...00.12Z" is 120 ms, "...00.123Z" is 123 ms), so two comments written within a second can
	// sort the wrong way round as strings.
	q += " ORDER BY id"
	all := []ArtifactComment{}
	if err := s.reader().SelectContext(ctx, &all, q, args...); err != nil {
		return nil, err
	}
	replies := map[int64]int{}
	for _, c := range all {
		if c.ParentID != nil && c.DeletedAt == nil {
			replies[*c.ParentID]++
		}
	}
	top := map[int64]*ArtifactComment{}
	for i := range all {
		if all[i].ParentID == nil {
			top[all[i].ID] = &all[i]
		}
	}
	out := []ArtifactComment{}
	for _, c := range all {
		root := c
		if c.ParentID != nil {
			t, ok := top[*c.ParentID]
			if !ok {
				continue
			}
			root = *t
		}
		switch state {
		case "open":
			if root.ResolvedAt != nil {
				continue
			}
		case "resolved":
			if root.ResolvedAt == nil {
				continue
			}
		}
		if c.DeletedAt != nil {
			if c.ParentID != nil || replies[c.ID] == 0 {
				continue
			}
			c.Body = ""
		}
		out = append(out, c)
	}
	return out, nil
}

// EditArtifactComment replaces the body of a live comment and stamps edited_at; ok is false when there is no such live comment.
func (s *SessionStore) EditArtifactComment(ctx context.Context, artifactID string, id int64, body string) (ArtifactComment, bool, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return ArtifactComment{}, false, ErrCommentEmpty
	}
	if len(body) > MaxArtifactCommentBody {
		return ArtifactComment{}, false, ErrCommentTooLong
	}
	res, err := s.db.ExecContext(ctx, "UPDATE artifact_comments SET body = ?, edited_at = ? WHERE id = ? AND artifact_id = ? AND deleted_at IS NULL", body, nowStamp(), id, artifactID)
	if err != nil {
		return ArtifactComment{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ArtifactComment{}, false, nil
	}
	out, ok, err := s.GetArtifactComment(ctx, artifactID, id)
	return out, ok, err
}

// DeleteArtifactComment turns a live comment into a tombstone (its text is erased, its replies stay); ok is false when there is no such live comment.
func (s *SessionStore) DeleteArtifactComment(ctx context.Context, artifactID string, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE artifact_comments SET body = '', anchor_json = '', deleted_at = ? WHERE id = ? AND artifact_id = ? AND deleted_at IS NULL", nowStamp(), id, artifactID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetArtifactCommentResolved resolves a thread (its top comment) or opens it again; byIssuer and bySubject name who resolved it. ok is false for a missing, deleted or reply comment.
func (s *SessionStore) SetArtifactCommentResolved(ctx context.Context, artifactID string, id int64, resolved bool, byIssuer, bySubject string) (ArtifactComment, bool, error) {
	var i2, s2, at any
	if resolved {
		i2, s2, at = byIssuer, bySubject, nowStamp()
	}
	res, err := s.db.ExecContext(ctx, "UPDATE artifact_comments SET resolved_by_issuer = ?, resolved_by_subject = ?, resolved_at = ? WHERE id = ? AND artifact_id = ? AND parent_id IS NULL AND deleted_at IS NULL", i2, s2, at, id, artifactID)
	if err != nil {
		return ArtifactComment{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ArtifactComment{}, false, nil
	}
	return s.GetArtifactComment(ctx, artifactID, id)
}

// DeleteArtifactComments removes every comment of an artifact (the artifact itself was deleted, #3553) and its lock; it returns how many rows went.
func (s *SessionStore) DeleteArtifactComments(ctx context.Context, artifactID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM artifact_comments WHERE artifact_id = ?", artifactID)
	if err != nil {
		return 0, err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM artifact_comment_locks WHERE scope = 'artifact' AND key = ?", artifactID); err != nil {
		return 0, err
	}
	if err := s.DeleteAsksOfArtifact(ctx, artifactID); err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// SetArtifactCommentsLocked turns the owner's lock on or off for one artifact (scope "artifact", key its id) or a whole board (scope "board", key its name).
func (s *SessionStore) SetArtifactCommentsLocked(ctx context.Context, scope, key string, locked bool) error {
	if scope != "artifact" && scope != "board" {
		return errors.New("lock scope must be artifact or board")
	}
	var err error
	if locked {
		_, err = s.db.ExecContext(ctx, "INSERT OR IGNORE INTO artifact_comment_locks (scope, key) VALUES (?, ?)", scope, key)
	} else {
		_, err = s.db.ExecContext(ctx, "DELETE FROM artifact_comment_locks WHERE scope = ? AND key = ?", scope, key)
	}
	return err
}

// ArtifactCommentsLocked reports whether comments on this artifact are read only: the artifact is locked, or the board it is being commented on (board may be "").
func (s *SessionStore) ArtifactCommentsLocked(ctx context.Context, artifactID, board string) (bool, error) {
	var n int
	err := s.reader().GetContext(ctx, &n, "SELECT COUNT(*) FROM artifact_comment_locks WHERE (scope = 'artifact' AND key = ?) OR (scope = 'board' AND key = ? AND ? <> '')", artifactID, board, board)
	return n > 0, err
}
