package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

const usageModelSID = "00000000-0000-0000-0000-0000000000f1"

// postTokenUsage sends one report to the hook-facing endpoint and returns the
// model stored on the resulting row (nil for NULL).
func postTokenUsage(t *testing.T, body string) *string {
	t.Helper()
	_, h, _, ss := setupSessionsTestServer(t)
	h.SetTokenStore(store.NewTokenUsageStore(h.db))
	model := "claude-opus-5[1m]" // the session's CONFIGURED model
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID: usageModelSID, AgentType: "claude", AgentName: "coral-go", WorkingDir: "/repo", Model: &model}))

	r := chi.NewRouter()
	r.Post("/api/sessions/live/{name}/token-usage", h.RecordTokenUsage)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/live/coral-go/token-usage", strings.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var stored *string
	require.NoError(t, h.db.GetContext(context.Background(), &stored,
		"SELECT model FROM token_usage WHERE session_id = ?", usageModelSID))
	return stored
}

func TestRecordTokenUsage_StoresReportedModel(t *testing.T) {
	got := postTokenUsage(t, `{"session_id":"`+usageModelSID+`","input_tokens":100,"output_tokens":50,"model":"claude-fable-5-1"}`)
	require.NotNil(t, got)
	assert.Equal(t, "claude-fable-5-1", *got)
}

// The session has a configured model, but it is not substituted: it may not be
// the model that produced these tokens, and the column must not hold a guess.
func TestRecordTokenUsage_WithoutModelStoresNullNotTheSessionModel(t *testing.T) {
	got := postTokenUsage(t, `{"session_id":"`+usageModelSID+`","input_tokens":100,"output_tokens":50}`)
	assert.Nil(t, got)
}
