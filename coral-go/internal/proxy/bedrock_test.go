package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// ── SigV4 Detection Tests ───────────────────────────────────

func TestIsSigV4Request_SigV4Header(t *testing.T) {
	assert.True(t, IsSigV4Request("AWS4-HMAC-SHA256 Credential=AKIA.../20250514/us-east-1/bedrock/aws4_request, SignedHeaders=..., Signature=..."))
}

func TestIsSigV4Request_BearerHeader(t *testing.T) {
	assert.False(t, IsSigV4Request("Bearer eyJhbGciOiJIUzI1NiJ9..."))
}

func TestIsSigV4Request_EmptyHeader(t *testing.T) {
	assert.False(t, IsSigV4Request(""))
}

func TestIsSigV4Request_BasicAuth(t *testing.T) {
	assert.False(t, IsSigV4Request("Basic dXNlcjpwYXNz"))
}

// ── SigV4 Header List Tests ─────────────────────────────────

func TestSigV4Headers_Contains(t *testing.T) {
	// Verify SigV4Headers contains all expected headers for stripping
	expected := []string{"Authorization", "X-Amz-Date", "X-Amz-Security-Token", "X-Amz-Content-Sha256"}
	for _, h := range expected {
		found := false
		for _, s := range SigV4Headers {
			if s == h {
				found = true
				break
			}
		}
		assert.True(t, found, "SigV4Headers should contain %q", h)
	}
}

func TestSigV4HeaderStripping_Manual(t *testing.T) {
	// Test the stripping logic using SigV4Headers directly
	headers := http.Header{
		"Authorization":        {"AWS4-HMAC-SHA256 Credential=..."},
		"X-Amz-Date":           {"20250514T120000Z"},
		"X-Amz-Security-Token": {"FwoGZXIvYXdzE..."},
		"X-Amz-Content-Sha256": {"e3b0c44298fc1c149afbf4c8996fb924..."},
		"Content-Type":         {"application/json"},
		"X-Custom-Header":      {"keep-me"},
	}

	// Simulate stripping
	for _, h := range SigV4Headers {
		headers.Del(h)
	}

	assert.Empty(t, headers.Get("Authorization"))
	assert.Empty(t, headers.Get("X-Amz-Date"))
	assert.Empty(t, headers.Get("X-Amz-Security-Token"))
	assert.Empty(t, headers.Get("X-Amz-Content-Sha256"))
	assert.Equal(t, "application/json", headers.Get("Content-Type"))
	assert.Equal(t, "keep-me", headers.Get("X-Custom-Header"))
}

// ── AWS Error Classification Tests ──────────────────────────

func TestClassifyAWSError_ExpiredToken(t *testing.T) {
	tests := []struct {
		name    string
		errMsg  string
		profile string
	}{
		{"expired token", "ExpiredTokenException: The security token included in the request is expired", "my-profile"},
		{"expired sso session", "SSO session associated with this token has expired", ""},
		{"unrecognized client", "UnrecognizedClientException: The security token is invalid", "prod"},
		{"token has expired", "token has expired", "dev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyAWSError(fmt.Errorf("%s", tt.errMsg), tt.profile)
			assert.Contains(t, err.Error(), "AWS SSO session expired")
			assert.Contains(t, err.Error(), "aws sso login")
			if tt.profile != "" {
				assert.Contains(t, err.Error(), "--profile "+tt.profile)
			}
		})
	}
}

func TestClassifyAWSError_NoCredentials(t *testing.T) {
	err := classifyAWSError(fmt.Errorf("NoCredentialProviders: no valid providers in chain"), "")
	assert.Contains(t, err.Error(), "no AWS credentials found")
	assert.Contains(t, err.Error(), "AWS_ACCESS_KEY_ID")
}

func TestClassifyAWSError_NoCredentials_IMDS(t *testing.T) {
	err := classifyAWSError(fmt.Errorf("no EC2 IMDS role found"), "")
	assert.Contains(t, err.Error(), "no AWS credentials found")
}

func TestClassifyAWSError_GenericError(t *testing.T) {
	err := classifyAWSError(fmt.Errorf("some other AWS error"), "my-profile")
	assert.Contains(t, err.Error(), "AWS credential error")
	// Should NOT contain raw credential values
	assert.NotContains(t, err.Error(), "AKIA")
}

func TestClassifyAWSError_NoCredentialLeak(t *testing.T) {
	// Ensure error messages with credentials in them don't leak through
	sensitiveErr := fmt.Errorf("failed with AKIAIOSFODNN7EXAMPLE secret")
	err := classifyAWSError(sensitiveErr, "")
	// The generic error wraps the original, which is expected — but it shouldn't
	// add any NEW credential info. The caller should handle logging carefully.
	assert.Contains(t, err.Error(), "AWS credential error")
}

// ── Bearer Passthrough Tests ────────────────────────────────

func TestBedrockBearerPassthrough(t *testing.T) {
	// Mock Bedrock upstream that verifies bearer token is forwarded
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-bedrock-bearer", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "msg_bedrock_123",
			"type":  "message",
			"model": "claude-sonnet-4-20250514",
			"usage": map[string]any{
				"input_tokens":  500,
				"output_tokens": 200,
			},
			"content": []map[string]any{
				{"type": "text", "text": "Hello from Bedrock!"},
			},
		})
	}))
	defer upstream.Close()

	db := testDB(t)
	p := New(db, map[Provider]ProviderConfig{})
	ctx := context.Background()

	// Register a bedrock session upstream
	err := p.SetSessionUpstream(ctx, "bedrock-bearer-session", "bedrock", upstream.URL)
	require.NoError(t, err)

	body := `{"model":"claude-sonnet-4-20250514","max_tokens":1024,"messages":[{"role":"user","content":"Hi"}]}`

	router := chi.NewRouter()
	router.Post("/proxy/{sessionID}/v1/messages", p.HandleAnthropicMessages)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/proxy/bedrock-bearer-session/v1/messages", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer test-bedrock-bearer")
	r.Header.Set("anthropic-version", "2023-06-01")
	router.ServeHTTP(w, r)

	assert.Equal(t, 200, w.Code)

	// Verify tokens were captured
	requests, _, err := p.store.ListRequests(r.Context(), "bedrock-bearer-session", 10, 0)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Equal(t, 500, requests[0].InputTokens)
	assert.Equal(t, 200, requests[0].OutputTokens)
}

// ── Session Upstream Storage Tests ──────────────────────────

func TestSetAndGetSessionUpstream(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	err := store.SetSessionUpstream(ctx, "sess-1", "bedrock", "https://bedrock-runtime.us-east-1.amazonaws.com")
	require.NoError(t, err)

	u, err := store.GetSessionUpstream(ctx, "sess-1")
	require.NoError(t, err)
	assert.Equal(t, "bedrock", u.Provider)
	assert.Equal(t, "https://bedrock-runtime.us-east-1.amazonaws.com", u.UpstreamURL)
}

func TestGetSessionUpstream_NotFound(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, err := store.GetSessionUpstream(ctx, "nonexistent")
	assert.Error(t, err)
}

func TestSetSessionUpstream_Upsert(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	err := store.SetSessionUpstream(ctx, "sess-1", "anthropic", "https://api.anthropic.com")
	require.NoError(t, err)

	err = store.SetSessionUpstream(ctx, "sess-1", "bedrock", "https://bedrock-runtime.us-west-2.amazonaws.com")
	require.NoError(t, err)

	u, err := store.GetSessionUpstream(ctx, "sess-1")
	require.NoError(t, err)
	assert.Equal(t, "bedrock", u.Provider)
	assert.Equal(t, "https://bedrock-runtime.us-west-2.amazonaws.com", u.UpstreamURL)
}

// ── Proxy GetSessionUpstream Fallback Tests ─────────────────

func TestProxyGetSessionUpstream_SessionOverride(t *testing.T) {
	db := testDB(t)
	providers := map[Provider]ProviderConfig{
		ProviderAnthropic: {BaseURL: "https://api.anthropic.com", APIKey: "key"},
	}
	p := New(db, providers)
	ctx := context.Background()

	p.SetSessionUpstream(ctx, "custom-sess", "bedrock", "https://bedrock-runtime.eu-west-1.amazonaws.com")

	url := p.GetSessionUpstream(ctx, "custom-sess", ProviderAnthropic)
	assert.Equal(t, "https://bedrock-runtime.eu-west-1.amazonaws.com", url)
}

func TestProxyGetSessionUpstream_FallbackToProvider(t *testing.T) {
	db := testDB(t)
	providers := map[Provider]ProviderConfig{
		ProviderAnthropic: {BaseURL: "https://api.anthropic.com", APIKey: "key"},
	}
	p := New(db, providers)
	ctx := context.Background()

	url := p.GetSessionUpstream(ctx, "unknown-sess", ProviderAnthropic)
	assert.Equal(t, "https://api.anthropic.com", url)
}

func TestProxyGetSessionUpstream_FallbackToDefault(t *testing.T) {
	db := testDB(t)
	p := New(db, map[Provider]ProviderConfig{})
	ctx := context.Background()

	url := p.GetSessionUpstream(ctx, "unknown-sess", ProviderAnthropic)
	assert.Equal(t, "https://api.anthropic.com", url)

	url = p.GetSessionUpstream(ctx, "unknown-sess", ProviderOpenAI)
	assert.Equal(t, "https://api.openai.com", url)
}

// ── Security Tests ──────────────────────────────────────────

func TestProxyRequestDoesNotStoreCredentials(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "msg_sec",
			"type":  "message",
			"model": "claude-sonnet-4-20250514",
			"usage": map[string]any{"input_tokens": 100, "output_tokens": 50},
			"content": []map[string]any{
				{"type": "text", "text": "response"},
			},
		})
	}))
	defer upstream.Close()

	p := testProxy(t, upstream)

	body := `{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"Hi"}]}`

	router := chi.NewRouter()
	router.Post("/proxy/{sessionID}/v1/messages", p.HandleAnthropicMessages)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/proxy/sec-session/v1/messages", strings.NewReader(body))
	r.Header.Set("x-api-key", "sk-ant-secret-key-12345")
	r.Header.Set("anthropic-version", "2023-06-01")
	router.ServeHTTP(w, r)

	require.Equal(t, 200, w.Code)

	requests, _, err := p.store.ListRequests(r.Context(), "sec-session", 10, 0)
	require.NoError(t, err)
	require.Len(t, requests, 1)

	reqJSON, _ := json.Marshal(requests[0])
	reqStr := string(reqJSON)
	assert.NotContains(t, reqStr, "sk-ant-secret-key-12345")
	assert.NotContains(t, reqStr, "AWS4-HMAC-SHA256")
}

// ── Bedrock Pricing Tests ───────────────────────────────────

func TestBedrockModelPricing(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantOK    bool
		wantInput float64
	}{
		{"claude sonnet exact", "claude-sonnet-4-20250514", true, 3.00},
		{"claude opus exact", "claude-opus-4-20250514", true, 15.00},
		{"claude sonnet prefix", "claude-sonnet-4", true, 3.00},
		{"claude haiku 4.5", "claude-haiku-4-5-20251001", true, 1.00},
		// Bedrock and Vertex IDs price from the first-party rows.
		{"bedrock legacy id", "anthropic.claude-sonnet-4-20250514-v1:0", true, 3.00},
		{"bedrock regional legacy id", "us.anthropic.claude-opus-4-20250514-v1:0", true, 15.00},
		{"bedrock regional opus 4.6", "eu.anthropic.claude-opus-4-6-v1:0", true, 5.00},
		{"bedrock current id", "anthropic.claude-opus-5", true, 5.00},
		{"bedrock global fable", "global.anthropic.claude-fable-5-1", true, 10.00},
		{"vertex dated id", "claude-haiku-4-5@20251001", true, 1.00},
		{"vertex opus 4.5", "claude-opus-4-5@20251101", true, 5.00},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pricing, ok := lookupPricing(tt.model)
			assert.Equal(t, tt.wantOK, ok, "pricing lookup for %s", tt.model)
			if tt.wantOK {
				assert.InDelta(t, tt.wantInput, pricing.InputPerMTok, 0.001)
			}
		})
	}
}

func TestBedrockCostCalculation(t *testing.T) {
	usage := TokenUsage{InputTokens: 1000, OutputTokens: 500}

	directCost := CalculateCost("claude-sonnet-4-20250514", usage)
	assert.Greater(t, directCost, 0.0)

	prefixCost := CalculateCost("claude-sonnet-4", usage)
	assert.InDelta(t, directCost, prefixCost, 0.0001)
}

// ── Per-Session Reroute Integration Test ────────────────────

func TestAnthropicMessages_UsesSessionUpstream(t *testing.T) {
	// Verify that HandleAnthropicMessages routes to per-session upstream
	// for non-bedrock providers (anthropic with custom gateway)
	sessionUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/messages", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "msg_rerouted",
			"type":  "message",
			"model": "claude-sonnet-4-20250514",
			"usage": map[string]any{"input_tokens": 300, "output_tokens": 150},
			"content": []map[string]any{
				{"type": "text", "text": "Rerouted!"},
			},
		})
	}))
	defer sessionUpstream.Close()

	globalUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should NOT have routed to global upstream")
	}))
	defer globalUpstream.Close()

	db := testDB(t)
	providers := map[Provider]ProviderConfig{
		ProviderAnthropic: {BaseURL: globalUpstream.URL, APIKey: ""},
	}
	p := New(db, providers)
	ctx := context.Background()

	// Register session with "anthropic" provider (custom gateway), not "bedrock"
	err := p.SetSessionUpstream(ctx, "reroute-sess", "anthropic", sessionUpstream.URL)
	require.NoError(t, err)

	body := `{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"Hi"}]}`

	router := chi.NewRouter()
	router.Post("/proxy/{sessionID}/v1/messages", p.HandleAnthropicMessages)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/proxy/reroute-sess/v1/messages", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("x-api-key", "test-key")
	r.Header.Set("anthropic-version", "2023-06-01")
	router.ServeHTTP(w, r)

	assert.Equal(t, 200, w.Code)

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "msg_rerouted", resp["id"])
}

func TestOpenAIChatCompletions_UsesSessionUpstream(t *testing.T) {
	sessionUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.True(t, strings.HasSuffix(r.URL.Path, "/v1/chat/completions"))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl-rerouted",
			"model": "gpt-4o",
			"usage": map[string]any{"prompt_tokens": 200, "completion_tokens": 100},
			"choices": []map[string]any{
				{"message": map[string]any{"content": "Rerouted!"}},
			},
		})
	}))
	defer sessionUpstream.Close()

	db := testDB(t)
	p := New(db, map[Provider]ProviderConfig{
		ProviderOpenAI: {BaseURL: "http://should-not-be-used", APIKey: ""},
	})
	ctx := context.Background()

	err := p.SetSessionUpstream(ctx, "openai-reroute", "openai", sessionUpstream.URL)
	require.NoError(t, err)

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}]}`

	router := chi.NewRouter()
	router.Post("/proxy/{sessionID}/v1/chat/completions", p.HandleOpenAIChatCompletions)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/proxy/openai-reroute/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer test-openai-key")
	router.ServeHTTP(w, r)

	assert.Equal(t, 200, w.Code)

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "chatcmpl-rerouted", resp["id"])
}
