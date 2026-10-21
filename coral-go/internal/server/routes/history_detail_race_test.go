package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/store"
)

// TestHistoryDetail_ConcurrentReadsEachGetTheWholeTranscript (#3651): many requests for the same
// history session at once each answer 200 with every message. The handler used to read through
// one shared incremental reader, so a request could find the transcript already consumed by
// another (no "new" messages: 404) or see only the part read after another request cleared it.
func TestHistoryDetail_ConcurrentReadsEachGetTheWholeTranscript(t *testing.T) {
	_, h, ss, codexHome, _, _ := setupHistoryVerificationServer(t)
	ctx := context.Background()

	dayDir := filepath.Join(codexHome, "sessions", "2026", "10", "15")
	require.NoError(t, os.MkdirAll(dayDir, 0755))
	sid := "e5f6a7b8-c9d0-1234-ef01-23456789abcd"
	const userMessages = 200
	var b strings.Builder
	b.WriteString(`{"timestamp":"2026-10-15T10:00:00.000Z","type":"session_meta","payload":{"id":"rollout-race","cwd":"/repo"}}` + "\n")
	fmt.Fprintf(&b, `{"timestamp":"2026-10-15T10:00:00.100Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"Coral session metadata:\n%s %s\n"}]}}`+"\n", at.CoralSessionMarkerPrefix, sid)
	for i := 0; i < userMessages; i++ {
		fmt.Fprintf(&b, `{"timestamp":"2026-10-15T10:00:%02d.000Z","type":"event_msg","payload":{"type":"user_message","message":"message %d","images":[]}}`+"\n", i%60, i)
	}
	rollout := filepath.Join(dayDir, "rollout-race.jsonl")
	require.NoError(t, os.WriteFile(rollout, []byte(b.String()), 0644))
	require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{SessionID: sid, SourceType: "codex", SourceFile: rollout}))

	// The handler is driven directly, so the requests overlap as much as the scheduler allows.
	r := chi.NewRouter()
	r.Get("/api/sessions/history/{sessionID}", h.GetSessionDetail)

	for round := 0; round < oracleN(50); round++ {
		const parallel = 64
		var wg sync.WaitGroup
		errs := make(chan string, parallel)
		start := make(chan struct{})
		for i := 0; i < parallel; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/history/"+sid, nil))
				if rec.Code != http.StatusOK {
					errs <- fmt.Sprintf("status %d", rec.Code)
					return
				}
				var body struct {
					Messages []map[string]any `json:"messages"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					errs <- err.Error()
					return
				}
				if len(body.Messages) != userMessages {
					errs <- fmt.Sprintf("%d messages, want %d", len(body.Messages), userMessages)
				}
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Errorf("round %d: %s", round, e)
		}
		if t.Failed() {
			return
		}
	}
}
