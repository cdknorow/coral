package store

import (
	"context"
	"encoding/json"
)

// CustomModel is one custom model as stored (#5480). The caller validates the set (agentmodels.ValidateCustomSet) before it is stored and again after it is read.
type CustomModel struct {
	Family      string
	Value       string
	DisplayName string
	Efforts     []string
}

type customModelRow struct {
	Family      string `db:"family"`
	Value       string `db:"value"`
	DisplayName string `db:"display_name"`
	Efforts     string `db:"efforts"`
}

// ListCustomModels is every custom model, in the order the set was saved (family order is the caller's to apply).
func (s *SessionStore) ListCustomModels(ctx context.Context) ([]CustomModel, error) {
	var rows []customModelRow
	if err := s.reader().SelectContext(ctx, &rows, `SELECT family, value, display_name, efforts FROM custom_models ORDER BY position, family, value`); err != nil {
		return nil, err
	}
	out := make([]CustomModel, 0, len(rows))
	for _, r := range rows {
		efforts := []string{}
		if json.Unmarshal([]byte(r.Efforts), &efforts) != nil || efforts == nil {
			efforts = []string{}
		}
		out = append(out, CustomModel{Family: r.Family, Value: r.Value, DisplayName: r.DisplayName, Efforts: efforts})
	}
	return out, nil
}

// ReplaceCustomModels stores models as the whole set, in one transaction: either every row is stored or nothing changes.
func (s *SessionStore) ReplaceCustomModels(ctx context.Context, models []CustomModel) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM custom_models`); err != nil {
		return err
	}
	for i, m := range models {
		efforts, err := json.Marshal(append([]string{}, m.Efforts...))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO custom_models (family, value, display_name, efforts, position) VALUES (?, ?, ?, ?, ?)`,
			m.Family, m.Value, m.DisplayName, string(efforts), i); err != nil {
			return err
		}
	}
	return tx.Commit()
}
