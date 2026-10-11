package store

import (
	"context"
	"database/sql"
	"fmt"
)

// CustomView represents a user-created sidebar view.
type CustomView struct {
	ID       int64  `db:"id" json:"id"`
	Name     string `db:"name" json:"name"`
	Prompt   string `db:"prompt" json:"prompt"`
	HTML     string `db:"html" json:"html"`
	TabOrder int    `db:"tab_order" json:"tab_order"`
	Scope    string `db:"scope" json:"scope"`
	// CreatedVia is "ui" when the view was written by the dashboard in a browser on this computer, and "api" for any
	// other caller (an agent, curl, a proxy) or a view of unknown provenance. The dashboard asks before running an "api" view.
	CreatedVia string `db:"created_via" json:"created_via"`
	CreatedAt  string `db:"created_at" json:"created_at"`
	UpdatedAt  string `db:"updated_at" json:"updated_at"`
}

// normalizeCreatedVia keeps only the two known provenance values; anything else (including empty) is "api", the
// cautious one.
func normalizeCreatedVia(v string) string {
	if v == "ui" {
		return "ui"
	}
	return "api"
}

// ViewStore provides custom view CRUD operations.
type ViewStore struct {
	db *DB
}

// NewViewStore creates a new ViewStore.
func NewViewStore(db *DB) *ViewStore {
	return &ViewStore{db: db}
}

// ListViews returns all custom views ordered by tab_order.
func (s *ViewStore) ListViews(ctx context.Context) ([]CustomView, error) {
	var views []CustomView
	err := s.db.SelectContext(ctx, &views,
		"SELECT id, name, prompt, html, tab_order, scope, created_via, created_at, updated_at FROM custom_views ORDER BY tab_order, id")
	return views, err
}

// GetView returns a single view by ID.
func (s *ViewStore) GetView(ctx context.Context, id int64) (*CustomView, error) {
	var v CustomView
	err := s.db.GetContext(ctx, &v,
		"SELECT id, name, prompt, html, tab_order, scope, created_via, created_at, updated_at FROM custom_views WHERE id = ?", id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &v, err
}

// CreateView inserts a new custom view.
func (s *ViewStore) CreateView(ctx context.Context, v *CustomView) (int64, error) {
	ts := nowUTC()
	result, err := s.db.ExecContext(ctx,
		"INSERT INTO custom_views (name, prompt, html, tab_order, scope, created_via, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		v.Name, v.Prompt, v.HTML, v.TabOrder, v.Scope, normalizeCreatedVia(v.CreatedVia), ts, ts)
	if err != nil {
		return 0, fmt.Errorf("insert view: %w", err)
	}
	return result.LastInsertId()
}

// UpdateView updates an existing view.
func (s *ViewStore) UpdateView(ctx context.Context, id int64, v *CustomView) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE custom_views SET name = ?, prompt = ?, html = ?, tab_order = ?, scope = ?, created_via = ?, updated_at = ? WHERE id = ?",
		v.Name, v.Prompt, v.HTML, v.TabOrder, v.Scope, normalizeCreatedVia(v.CreatedVia), nowUTC(), id)
	return err
}

// DeleteView removes a view by ID.
func (s *ViewStore) DeleteView(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM custom_views WHERE id = ?", id)
	return err
}
