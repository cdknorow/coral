package main

import (
	"strings"
	"testing"
)

// #3536: join reports a name several boards share by listing their ids, a refused new name
// by its reason, and nothing for a success.
func TestJoinProblem(t *testing.T) {
	if got := joinProblem("alpha", map[string]any{"project": "b-abcdefghij", "subscriber_id": "Dev"}); got != "" {
		t.Fatalf("a success has no problem: %q", got)
	}
	amb := joinProblem("alpha", map[string]any{"error": "ambiguous_board", "candidates": []any{
		map[string]any{"board_id": "b-aaaaaaaaaa", "board_label": "alpha", "created_at": "2026-10-12", "members": float64(4)},
		map[string]any{"board_id": "b-bbbbbbbbbb", "board_label": "alpha", "created_at": "2026-10-14", "members": float64(1)},
	}})
	for _, want := range []string{"More than one board is named 'alpha'", "coral-board join b-aaaaaaaaaa", "4 members", "coral-board join b-bbbbbbbbbb"} {
		if !strings.Contains(amb, want) {
			t.Fatalf("missing %q in %q", want, amb)
		}
	}
	bad := joinProblem("Sprint 42", map[string]any{"error": "invalid_board_name", "message": "a board name may use only letters"})
	if !strings.Contains(bad, "a board name may use only letters") {
		t.Fatalf("got %q", bad)
	}
}
