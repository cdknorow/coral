package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sqlx.DB {
	t.Helper()
	db, err := sqlx.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func testProxy(t *testing.T, upstream *httptest.Server) *Proxy {
	t.Helper()
	db := testDB(t)
	providers := map[Provider]ProviderConfig{
		ProviderAnthropic: {BaseURL: upstream.URL, APIKey: "test-key"},
		ProviderOpenAI:    {BaseURL: upstream.URL, APIKey: "test-key"},
	}
	return New(db, providers)
}

func TestHealthEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	p := testProxy(t, upstream)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/proxy/health", nil)
	p.Health(w, r)

	assert.Equal(t, 200, w.Code)
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "ok", resp["status"])
	assert.NotNil(t, resp["uptime_seconds"])
}

func TestAnthropicJSONForwarding(t *testing.T) {
	// Mock upstream that returns a valid Anthropic response
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
		assert.Equal(t, "application/json", r.Header.Get("content-type"))
		assert.Equal(t, "/v1/messages", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "msg_123",
			"type":  "message",
			"model": "claude-sonnet-4-20250514",
			"usage": map[string]any{
				"input_tokens":  1000,
				"output_tokens": 500,
			},
			"content": []map[string]any{
				{"type": "text", "text": "Hello!"},
			},
		})
	}))
	defer upstream.Close()

	p := testProxy(t, upstream)

	body := `{"model":"claude-sonnet-4-20250514","max_tokens":1024,"messages":[{"role":"user","content":"Hi"}]}`

	// Use chi router to properly set URL params
	router := chi.NewRouter()
	router.Post("/proxy/{sessionID}/v1/messages", p.HandleAnthropicMessages)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/proxy/test-session-123/v1/messages", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("anthropic-version", "2023-06-01")
	router.ServeHTTP(w, r)

	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "true", w.Header().Get("X-Coral-Proxy"))
	assert.NotEmpty(t, w.Header().Get("X-Coral-Request-Id"))
	assert.Equal(t, "test-session-123", w.Header().Get("X-Coral-Session-Id"))

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "msg_123", resp["id"])

	// Verify cost was recorded
	requests, total, err := p.store.ListRequests(r.Context(), "test-session-123", 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Equal(t, "success", requests[0].Status)
	assert.Equal(t, 1000, requests[0].InputTokens)
	assert.Equal(t, 500, requests[0].OutputTokens)
	assert.Greater(t, requests[0].CostUSD, 0.0)
}

func TestAnthropicSSEStreaming(t *testing.T) {
	// Mock upstream that returns SSE events
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)

		events := []string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":0}}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","usage":{"output_tokens":50}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}
		for _, line := range events {
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	p := testProxy(t, upstream)

	body := `{"model":"claude-sonnet-4-20250514","stream":true,"messages":[{"role":"user","content":"Hi"}]}`

	router := chi.NewRouter()
	router.Post("/proxy/{sessionID}/v1/messages", p.HandleAnthropicMessages)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/proxy/sse-session/v1/messages", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, r)

	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	assert.Equal(t, "true", w.Header().Get("X-Coral-Proxy"))

	// Verify the SSE content was forwarded
	respBody := w.Body.String()
	assert.Contains(t, respBody, "message_start")
	assert.Contains(t, respBody, "content_block_delta")
	assert.Contains(t, respBody, "message_stop")

	// Verify tokens were extracted and cost recorded
	requests, _, err := p.store.ListRequests(r.Context(), "sse-session", 10, 0)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Equal(t, "success", requests[0].Status)
	assert.Equal(t, 100, requests[0].InputTokens)
	assert.Equal(t, 50, requests[0].OutputTokens)
	assert.Equal(t, 1, requests[0].IsStreaming)
}

func TestOpenAIJSONForwarding(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl-123",
			"model": "gpt-4o",
			"usage": map[string]any{
				"prompt_tokens":     800,
				"completion_tokens": 200,
			},
			"choices": []map[string]any{
				{"message": map[string]any{"content": "Hello!"}},
			},
		})
	}))
	defer upstream.Close()

	p := testProxy(t, upstream)

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}]}`

	router := chi.NewRouter()
	router.Post("/proxy/{sessionID}/v1/chat/completions", p.HandleOpenAIChatCompletions)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/proxy/openai-session/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, r)

	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "true", w.Header().Get("X-Coral-Proxy"))

	requests, _, err := p.store.ListRequests(r.Context(), "openai-session", 10, 0)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Equal(t, 800, requests[0].InputTokens)
	assert.Equal(t, 200, requests[0].OutputTokens)
	assert.Equal(t, "openai", requests[0].Provider)
}

func TestUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"Rate limited"}}`))
	}))
	defer upstream.Close()

	p := testProxy(t, upstream)

	body := `{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"Hi"}]}`

	router := chi.NewRouter()
	router.Post("/proxy/{sessionID}/v1/messages", p.HandleAnthropicMessages)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/proxy/error-session/v1/messages", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, r)

	assert.Equal(t, 429, w.Code)

	requests, _, err := p.store.ListRequests(r.Context(), "error-session", 10, 0)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Equal(t, "error", requests[0].Status)
	assert.Equal(t, 429, *requests[0].HTTPStatus)
}

func TestCostCalculation(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		usage    TokenUsage
		expected float64
	}{
		{
			name:     "exact match",
			model:    "claude-sonnet-4-20250514",
			usage:    TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
			expected: 3.00 + 15.00, // $3/M input + $15/M output
		},
		{
			name:     "exact match openai",
			model:    "gpt-4o",
			usage:    TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
			expected: 2.50 + 10.00,
		},
		{
			name:  "cache tokens",
			model: "claude-sonnet-4-20250514",
			usage: TokenUsage{
				InputTokens:     1000,
				OutputTokens:    500,
				CacheReadTokens: 200,
			},
			expected: 1000*3.00/1_000_000 + 500*15.00/1_000_000 + 200*0.30/1_000_000,
		},
		{
			name:     "unknown model",
			model:    "unknown-model",
			usage:    TokenUsage{InputTokens: 1000},
			expected: 0, // unknown model = no pricing
		},
		// Alias/short name tests — these are the model IDs agents actually send
		{
			name:     "claude-opus-4-6 alias",
			model:    "claude-opus-4-6",
			usage:    TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
			expected: 5.00 + 25.00,
		},
		{
			name:     "claude-sonnet-4-6 alias",
			model:    "claude-sonnet-4-6",
			usage:    TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
			expected: 3.00 + 15.00,
		},
		{
			name:     "claude-haiku-4-5-20251001 alias",
			model:    "claude-haiku-4-5-20251001",
			usage:    TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
			expected: 1.00 + 5.00,
		},
		{
			name:     "claude-opus-4 short prefix",
			model:    "claude-opus-4",
			usage:    TokenUsage{InputTokens: 1_000_000},
			expected: 15.00,
		},
		{
			name:     "claude-sonnet-4-6-20250514 long alias",
			model:    "claude-sonnet-4-6-20250514",
			usage:    TokenUsage{InputTokens: 1_000_000},
			expected: 3.00,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost := CalculateCost(tt.model, tt.usage)
			assert.InDelta(t, tt.expected, cost, 0.0001)
		})
	}
}

func TestLookupPricing(t *testing.T) {
	// Verify all common model aliases resolve correctly
	aliases := map[string]float64{
		"claude-opus-4-20250514":    15.00, // exact
		"claude-opus-4-6":           5.00,  // Opus 4.5+ is $5, unlike Opus 4/4.1
		"claude-opus-4-6-20250514":  5.00,  // dated form of the same model
		"claude-opus-4":             15.00, // family prefix
		"claude-sonnet-4-20250514":  3.00,  // exact
		"claude-sonnet-4-6":         3.00,  // short alias
		"claude-haiku-4-5-20251001": 1.00,  // dated ID
		"claude-3-5-haiku-20241022": 0.80,  // Haiku 3.5
		"gpt-4o":                    2.50,  // exact
		"gemini-2.5-pro":            1.25,  // exact
	}
	for model, expectedInput := range aliases {
		t.Run(model, func(t *testing.T) {
			p, ok := lookupPricing(model)
			assert.True(t, ok, "should find pricing for %s", model)
			assert.InDelta(t, expectedInput, p.InputPerMTok, 0.001)
		})
	}

	// Unknown models should not match
	_, ok := lookupPricing("totally-unknown")
	assert.False(t, ok)
}

func TestAnthropicSSEParsing(t *testing.T) {
	var usage TokenUsage

	// message_start with input tokens
	usage = parseAnthropicSSEChunk(
		`{"type":"message_start","message":{"usage":{"input_tokens":1500,"cache_read_input_tokens":300}}}`,
		usage,
	)
	assert.Equal(t, 1500, usage.InputTokens)
	assert.Equal(t, 300, usage.CacheReadTokens)

	// message_delta with output tokens
	usage = parseAnthropicSSEChunk(
		`{"type":"message_delta","usage":{"output_tokens":800}}`,
		usage,
	)
	assert.Equal(t, 800, usage.OutputTokens)
	assert.Equal(t, 1500, usage.InputTokens) // unchanged

	// [DONE] doesn't change anything
	usage = parseAnthropicSSEChunk("[DONE]", usage)
	assert.Equal(t, 800, usage.OutputTokens)
}

func TestOpenAISSEParsing(t *testing.T) {
	var usage TokenUsage

	// Regular chunk without usage
	usage = parseOpenAISSEChunk(
		`{"choices":[{"delta":{"content":"Hello"}}]}`,
		usage,
	)
	assert.Equal(t, 0, usage.InputTokens)

	// Final chunk with usage
	usage = parseOpenAISSEChunk(
		`{"usage":{"prompt_tokens":500,"completion_tokens":200}}`,
		usage,
	)
	assert.Equal(t, 500, usage.InputTokens)
	assert.Equal(t, 200, usage.OutputTokens)
}

func TestNoAPIKey(t *testing.T) {
	p := &Proxy{
		providers: map[Provider]ProviderConfig{
			ProviderAnthropic: {BaseURL: "http://unused", APIKey: ""},
		},
		client: http.DefaultClient,
	}

	router := chi.NewRouter()
	router.Post("/proxy/{sessionID}/v1/messages", p.HandleAnthropicMessages)

	body := `{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"Hi"}]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/proxy/no-key-session/v1/messages", strings.NewReader(body))
	router.ServeHTTP(w, r)

	assert.Equal(t, 502, w.Code)
}
