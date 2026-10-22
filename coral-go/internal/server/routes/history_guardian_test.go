package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/jsonl"
	"github.com/subgentic/coral/internal/store"
)

// Real parent/guardian records from Codex 0.163. A worker with different thread
// metadata must survive; old guardian aliases must disappear without touching files.
func TestHistoryGuardianReindexListTranscriptAndSearch(t *testing.T) {
	for _, withFTS := range []bool{true, false} {
		name := "with-fts"
		if !withFTS {
			name = "without-fts"
		}
		t.Run(name, func(t *testing.T) {
			_, h, ss, home, _, _ := setupHistoryVerificationServer(t)
			dir := filepath.Join(home, "sessions", "2026", "10", "22")
			require.NoError(t, os.MkdirAll(dir, 0700))
			data, err := os.ReadFile(filepath.Join("..", "..", "agent", "testdata", "codex-0163-parent.jsonl"))
			require.NoError(t, err)
			guardian, err := os.ReadFile(filepath.Join("..", "..", "agent", "testdata", "codex-0163-guardian.jsonl"))
			require.NoError(t, err)
			parentID := "11111111-1111-4111-8111-111111116081"
			childID := "11111111-1111-4111-8111-111111116082"
			workerID := "11111111-1111-4111-8111-111111116083"
			worker := strings.ReplaceAll(string(data), parentID, workerID)
			worker = strings.ReplaceAll(worker, `"source":"exec","thread_source":"user"`, `"source":{"subagent":{"other":"worker"}},"thread_source":"subagent"`)
			require.Contains(t, worker, `"thread_source":"subagent"`)
			paths := map[string]string{}
			for sid, contents := range map[string][]byte{parentID: data, childID: guardian, workerID: []byte(worker)} {
				paths[sid] = filepath.Join(dir, "rollout-2026-10-22T00-00-00-"+sid+".jsonl")
				require.NoError(t, os.WriteFile(paths[sid], contents, 0600))
			}
			fi, err := os.Stat(paths[childID])
			require.NoError(t, err)
			mtime := float64(fi.ModTime().Unix())
			ctx := context.Background()
			// The same ordinary verdict text is readable without guardian metadata;
			// keep its real message key for the Inbox saved-message source check.
			rawPath := filepath.Join(dir, "verdict-control.jsonl")
			lines := strings.Split(strings.TrimSpace(string(guardian)), "\n")
			require.NoError(t, os.WriteFile(rawPath, []byte(lines[len(lines)-1]+"\n"), 0600))
			ordinary, _, err := jsonl.ReadFromContext(ctx, rawPath, "codex", 0)
			require.NoError(t, err)
			require.Len(t, ordinary, 1)
			key := withKeys(messageKeySecretFor(h.cfg), ordinary)[0]["key"].(string)

			require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{SessionID: "stale-guardian", SourceType: "codex", SourceFile: paths[childID], FileMtime: mtime, MessageCount: 1, DisplaySummary: "rationale verdict"}))
			require.NoError(t, ss.EnqueueForSummarization(ctx, "stale-guardian"))
			if withFTS {
				require.NoError(t, ss.UpsertFTS(ctx, "stale-guardian", "risk_level low rationale verdict"))
				known, err := ss.GetIndexedMtimes(ctx)
				require.NoError(t, err)
				require.Equal(t, mtime, known[paths[childID]])
			}
			idx := background.NewSessionIndexer(ss, []agent.HistoryScanner{&agent.CodexAgent{}}, time.Hour, 0)
			require.NoError(t, idx.RunOnce(ctx))
			require.NoError(t, idx.RunOnce(ctx))
			stale, err := ss.GetIndexedSession(ctx, "stale-guardian")
			require.NoError(t, err)
			assert.Nil(t, stale)
			call := func(handler http.HandlerFunc, url string) map[string]any {
				rec := httptest.NewRecorder()
				handler(rec, httptest.NewRequest("GET", url, nil))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				var v map[string]any
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v))
				return v
			}
			list := call(h.ListSessions, "/api/sessions/history?type=agent")
			assert.EqualValues(t, 2, list["total"])
			search := call(h.SearchChats, "/api/sessions/history/search?type=agent&q=rationale")
			assert.EqualValues(t, 0, search["total"])
			search = call(h.SearchChats, "/api/sessions/history/search?type=agent&q=GUARDIAN_PROOF_DONE")
			assert.EqualValues(t, 2, search["total"], "real parent and non-guardian worker remain searchable")
			for _, sid := range []string{parentID, workerID} {
				messages, _ := h.sessionTranscript(ctx, sid)
				assert.NotEmpty(t, messages)
			}
			messages, _ := h.sessionTranscript(ctx, childID)
			assert.Empty(t, messages)
			text, found, _ := h.SavedAgentText(ctx, childID, "codex", key)
			assert.Empty(t, text)
			assert.False(t, found, "Inbox cannot expand a guardian saved-message source")
			// Board posts are independent human/agent messages, not rollout records.
			_, err = h.bs.PostMessage(ctx, "guardian-check", "Operator", "rationale guardian_review remains ordinary board text", nil)
			require.NoError(t, err)
			group := call(h.SearchChats, "/api/sessions/history/search?type=group&q=rationale")
			assert.EqualValues(t, 1, group["total"], "no text-based filtering of board messages")

			after, err := os.ReadFile(paths[childID])
			require.NoError(t, err)
			assert.Equal(t, guardian, after)
			fi, err = os.Stat(paths[childID])
			require.NoError(t, err)
			assert.Equal(t, mtime, float64(fi.ModTime().Unix()))
		})
	}
}
