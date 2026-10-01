package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCLIHistorySearch_MockServer(t *testing.T) {
	// Setup mock server
	var requestedQuery, requestedRoles string
	var requestedLimit, requestedOffset string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/history/search" {
			q := r.URL.Query()
			requestedQuery = q.Get("query")
			requestedRoles = q.Get("roles")
			requestedLimit = q.Get("limit")
			requestedOffset = q.Get("offset")

			resp := map[string]any{
				"query":         requestedQuery,
				"status":        "complete",
				"total_matches": 1,
				"has_more":      false,
				"limit":         10,
				"offset":        0,
				"results": []map[string]any{
					{
						"session_id":    "test-sess-uuid",
						"is_current":    true,
						"message_index": 5,
						"role":          "assistant",
						"timestamp":     "2026-09-30T12:00:00Z",
						"excerpt":       "...I configured port 8420...",
					},
				},
				"sessions_searched": []map[string]any{
					{
						"session_id":        "test-sess-uuid",
						"role":              "current",
						"status":            "ready",
						"messages_searched": 12,
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	// Point serverURL to test server
	origURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = origURL }()

	// Mock session env var
	t.Setenv("CORAL_SESSION_NAME", "claude-test-sess-uuid")

	// Verify cmdHistorySearch with flags
	cmdHistorySearch([]string{"port 8420", "--roles", "assistant", "--limit", "5", "--offset", "2"})

	assert.Equal(t, "port 8420", requestedQuery)
	assert.Equal(t, "assistant", requestedRoles)
	assert.Equal(t, "5", requestedLimit)
	assert.Equal(t, "2", requestedOffset)
}

func TestCLIHistoryContext_MockServer(t *testing.T) {
	var requestedSession string
	var requestedIndex string
	var requestedWindow string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/history/context" {
			q := r.URL.Query()
			requestedSession = q.Get("target_session_id")
			requestedIndex = q.Get("message_index")
			requestedWindow = q.Get("window")

			resp := map[string]any{
				"session_id":     requestedSession,
				"target_index":   5,
				"total_messages": 10,
				"window":         2,
				"messages": []map[string]any{
					{
						"message_index": 4,
						"role":          "user",
						"timestamp":     "2026-09-30T11:59:00Z",
						"content":       "What is the port?",
						"is_target":     false,
					},
					{
						"message_index": 5,
						"role":          "assistant",
						"timestamp":     "2026-09-30T12:00:00Z",
						"content":       "The port is 8420.",
						"is_target":     true,
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	origURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = origURL }()

	t.Setenv("CORAL_SESSION_NAME", "claude-test-sess-uuid")

	// Call context with explicit session and index
	cmdHistoryContext([]string{"custom-ancestor-uuid", "5", "--window", "3"})

	assert.Equal(t, "custom-ancestor-uuid", requestedSession)
	assert.Equal(t, "5", requestedIndex)
	assert.Equal(t, "3", requestedWindow)
}
