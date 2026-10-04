package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// UIRequest is a durable record of an operator's panel-generation request sent
// to an agent. It makes delivery idempotent per (session, request_id): a retry
// of an already delivered request is never typed into the terminal twice.
type UIRequest struct {
	SessionID    string `db:"session_id"`
	RequestID    string `db:"request_id"`
	Request      string `db:"request"`
	Notification string `db:"notification"`
	Status       string `db:"status"` // pending | delivered | failed
	Error        string `db:"error"`
	CreatedAt    string `db:"created_at"`
	UpdatedAt    string `db:"updated_at"`
}

// UIRequestOutcome says what the caller must do after BeginUIRequest.
type UIRequestOutcome int

const (
	UIRequestSend       UIRequestOutcome = iota // caller owns delivery and must call FinishUIRequest
	UIRequestDuplicate                          // already delivered; do not resend
	UIRequestInProgress                         // another delivery is pending
	UIRequestConflict                           // request_id reused with different text
	UIRequestUnknown                            // a send may have partly reached the agent; never resend
)

// uiRequestUnknownPrefix marks a pending row whose delivery outcome is unknown.
// The row keeps status "pending" (the table's CHECK constraint predates this
// state), so every retry stays blocked.
const uiRequestUnknownPrefix = "delivery_unknown: "

const uiRequestRetention = 30 * 24 * time.Hour

const uiRequestSchema = `CREATE TABLE IF NOT EXISTS agent_ui_requests (
	session_id   TEXT NOT NULL,
	request_id   TEXT NOT NULL,
	request      TEXT NOT NULL,
	notification TEXT NOT NULL,
	status       TEXT NOT NULL CHECK (status IN ('pending','delivered','failed')),
	error        TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL,
	PRIMARY KEY (session_id, request_id)
)`

// BeginUIRequest atomically claims delivery of a request. A new request, or a
// retry of a failed one, returns UIRequestSend with the row marked pending.
func (d *DB) BeginUIRequest(ctx context.Context, sessionID, requestID, request, notification string) (*UIRequest, UIRequestOutcome, error) {
	if _, err := d.ExecContext(ctx, uiRequestSchema); err != nil {
		return nil, 0, err
	}
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	// Bounded housekeeping so the table cannot grow without limit.
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_ui_requests WHERE created_at < ?`, now.Add(-uiRequestRetention).Format(time.RFC3339Nano)); err != nil {
		return nil, 0, err
	}
	var row UIRequest
	err = tx.GetContext(ctx, &row, `SELECT * FROM agent_ui_requests WHERE session_id=? AND request_id=?`, sessionID, requestID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		row = UIRequest{SessionID: sessionID, RequestID: requestID, Request: request, Notification: notification, Status: "pending", CreatedAt: stamp, UpdatedAt: stamp}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_ui_requests (session_id,request_id,request,notification,status,error,created_at,updated_at) VALUES (?,?,?,?, 'pending','',?,?)`,
			sessionID, requestID, request, notification, stamp, stamp); err != nil {
			return nil, 0, err
		}
		return &row, UIRequestSend, tx.Commit()
	case err != nil:
		return nil, 0, err
	}
	if row.Request != request {
		return &row, UIRequestConflict, nil
	}
	switch row.Status {
	case "delivered":
		return &row, UIRequestDuplicate, nil
	case "pending":
		if strings.HasPrefix(row.Error, uiRequestUnknownPrefix) {
			return &row, UIRequestUnknown, nil
		}
		return &row, UIRequestInProgress, nil
	}
	// failed: the caller may retry delivery with the same request_id.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_ui_requests SET status='pending', error='', notification=?, updated_at=? WHERE session_id=? AND request_id=?`,
		notification, stamp, sessionID, requestID); err != nil {
		return nil, 0, err
	}
	row.Status, row.Error, row.Notification, row.UpdatedAt = "pending", "", notification, stamp
	return &row, UIRequestSend, tx.Commit()
}

// FinishUIRequest records the delivery result. A nil sendErr marks the request
// delivered; a delivery-unknown error is persisted as unknown (never retried);
// any other error is recorded as failed so it can be retried.
func (d *DB) FinishUIRequest(ctx context.Context, sessionID, requestID string, sendErr error) error {
	status, msg := "delivered", ""
	if sendErr != nil {
		status, msg = "failed", sendErr.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		// An error after input may already have reached the agent is not a
		// plain failure: persist it as unknown (still "pending") so the same
		// request is never resent and appended to a half-typed input line.
		var unknown interface{ DeliveryUnknown() bool }
		if errors.As(sendErr, &unknown) && unknown.DeliveryUnknown() {
			status, msg = "pending", uiRequestUnknownPrefix+msg
		}
	}
	_, err := d.ExecContext(ctx, `UPDATE agent_ui_requests SET status=?, error=?, updated_at=? WHERE session_id=? AND request_id=?`,
		status, msg, time.Now().UTC().Format(time.RFC3339Nano), sessionID, requestID)
	return err
}
