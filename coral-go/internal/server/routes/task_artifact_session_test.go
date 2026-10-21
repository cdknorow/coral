package routes

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/subgentic/coral/internal/board"
)

// #4098: a task with no agent session (completed by the API key, the command line or the
// dashboard) has no changes to persist: the writer isn't called and nothing is logged as a
// warning. A task with a session is still written, and a real failure still warns.
func TestTaskChangesOnlyForAgentSessions(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := &BoardHandler{}
	calls := 0
	h.SetTaskArtifactWriter(t.TempDir(), func(context.Context, *board.Task, string) error {
		calls++
		return errors.New("could not determine working directory")
	})
	empty := ""
	for _, task := range []*board.Task{{ID: 1}, {ID: 2, SessionID: &empty}} {
		h.persistTaskArtifact(context.Background(), task)
	}
	if calls != 0 || strings.Contains(logged.String(), "level=WARN") {
		t.Fatalf("no session: writer called %d times, log %q", calls, logged.String())
	}
	sid := "aaaaaaaa-0000-4000-8000-000000004098"
	h.persistTaskArtifact(context.Background(), &board.Task{ID: 3, SessionID: &sid})
	if calls != 1 || !strings.Contains(logged.String(), "persist task changes artifact failed") {
		t.Fatalf("with a session: writer called %d times, log %q", calls, logged.String())
	}
}
