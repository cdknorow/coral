package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/store"
)

// A warm, isolated 26-agent request with 200 historical sessions. It exercises
// the actual List handler without a live server, network listener or user data.
func BenchmarkLiveRefreshList(b *testing.B) {
	root := b.TempDir()
	b.Setenv("CLAUDE_PROJECTS_DIR", root)
	db, err := store.Open(filepath.Join(root, "sessions.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	bs, err := board.NewStore(filepath.Join(root, "board.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer bs.Close()
	terminal := newMockTerminal()
	h := NewSessionsHandler(db, &config.Config{LogDir: root}, nil, terminal, bs)
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 26; i++ {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
		terminal.addSession("claude-"+id, "/fixture")
		if err := h.ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentType: "claude", AgentName: "fixture", WorkingDir: "/fixture"}); err != nil {
			b.Fatal(err)
		}
		logPath := naming.LogFile(root, "claude", id)
		if err := os.WriteFile(logPath, []byte("log line fixture\n"), 0600); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(project, id+".jsonl"), []byte(`{"type":"user","message":{"content":"fixture"}}`+"\n"), 0600); err != nil {
			b.Fatal(err)
		}
		h.cachedLogStatus(logPath)
		h.jsonl.FirstUserPrompt(id, "/fixture", "claude")
		h.transcriptWarm[id] = true
	}
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO agent_events(agent_name,session_id,event_type,tool_name,summary,detail_json,created_at) VALUES('fixture',?,'tool_use','Edit','',?,'2026-10-02')`)
	if err != nil {
		b.Fatal(err)
	}
	for session := 0; session < 226; session++ {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", session)
		for event := 0; event < 100; event++ {
			if _, err := stmt.Exec(id, fmt.Sprintf(`{"file_path":"file-%d.go"}`, event%20)); err != nil {
				b.Fatal(err)
			}
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/sessions/live", nil)
	initial := httptest.NewRecorder()
	h.List(initial, request)
	if initial.Code != 200 {
		b.Fatal(initial.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(initial.Body.Bytes(), &rows); err != nil {
		b.Fatal(err)
	}
	if len(rows) != 26 {
		b.Fatalf("lost agent coverage: %d", len(rows))
	}
	for _, row := range rows {
		if row["changed_file_count"] != float64(20) || row["first_prompt"] != "fixture" {
			b.Fatal("incomplete live enrichment")
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		response := httptest.NewRecorder()
		h.List(response, request)
		if response.Code != 200 {
			b.Fatal(response.Body.String())
		}
	}
}
