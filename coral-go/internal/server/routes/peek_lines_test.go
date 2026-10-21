package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/ptymanager"
)

// peekScreenTerminal captures like both backends do: `lines` rows of scrollback above a 50-row screen
// whose bottom rows are blank (tmux's capture-pane -S -N; the PTY's rendered ring since #27).
type peekScreenTerminal struct {
	ptymanager.SessionTerminal
}

func (peekScreenTerminal) CaptureOutput(_ context.Context, _ string, lines int, _, _ string) (string, error) {
	var rows []string
	for i := 1; i <= lines+40; i++ {
		rows = append(rows, fmt.Sprintf("row %03d", i))
	}
	for i := 0; i < 10; i++ {
		rows = append(rows, "") // the screen's empty bottom
	}
	return strings.Join(rows, "\n"), nil
}

func TestLastLines(t *testing.T) {
	text := "a\nb\nc\nd\n\n   \n"
	assert.Equal(t, "c\nd", lastLines(text, 2), "the last lines with output, not the blank bottom")
	assert.Equal(t, text, lastLines(text, 30), "within the count: byte for byte what the terminal gave")
	assert.Equal(t, text, lastLines(text, 0), "no count: the whole capture, unchanged")
	assert.Equal(t, "", lastLines("\n\n\n\n\n\n", 2), "over the count and all blank: nothing")
}

// A Unix user's peek is unchanged wherever tmux's capture already fit in the lines asked for (a
// pane of up to N rows with no scrollback): byte for byte, trailing blank rows included. Only a
// capture over the limit is cut.
func TestPeekOfACaptureWithinTheLimitIsUnchanged(t *testing.T) {
	tmuxCapture := "user@host:~/work$ make test\nok  \tpkg\t0.1s\nuser@host:~/work$ \n\n\n\n"
	assert.Equal(t, tmuxCapture, lastLines(tmuxCapture, 30))
	assert.Equal(t, tmuxCapture, lastLines(tmuxCapture, strings.Count(tmuxCapture, "\n")+1), "one line under the limit")
	assert.Equal(t, "ok  \tpkg\t0.1s\nuser@host:~/work$", lastLines(tmuxCapture, 2), "over it: the last lines with output, trailing blanks dropped")
}

// #27: peek --lines N is "the last N lines of their terminal" (agent.go's instructions to an
// orchestrator), on both backends: a capture's N rows of scrollback plus the screen gave N+50.
func TestPeekReturnsAtMostTheLinesAsked(t *testing.T) {
	bs, err := boardtest.Open(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	ctx := context.Background()
	_, err = bs.Subscribe(ctx, "p", "Orchestrator", "Orchestrator", "orch-session", nil, "", true)
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "p", "Dev", "Backend Dev", "dev-session", nil, "")
	require.NoError(t, err)

	h := NewBoardHandler(bs)
	h.SetTerminal(peekScreenTerminal{})
	r := chi.NewRouter()
	r.Get("/api/board/{project}/peek", h.PeekAgent)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/board/p/peek?subscriber_id=Orchestrator&target=Dev&lines=30", nil))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	got := out["output"].(string)
	rows := strings.Split(got, "\n")
	assert.Len(t, rows, 30)
	assert.Equal(t, "row 070", rows[len(rows)-1], "ending at the latest output, not the blank bottom")
	assert.NotContains(t, got, "\x1b")
}
