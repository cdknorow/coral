package httputil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #3434: a per-action limit is marked, so the dashboard doesn't take it for an overloaded server.
func TestWriteActionLimit(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteActionLimit(rec, 30, "too_many_runs", "5 runs already")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get(ActionLimitHeader) != "action" || rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("got %d %v", rec.Code, rec.Header())
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["code"] != "too_many_runs" || out["error"] != "5 runs already" {
		t.Fatalf("body %v", out)
	}
	rec = httptest.NewRecorder()
	WriteActionLimit(rec, 0, "too_many_attempts", "x")
	if rec.Header().Get("Retry-After") != "" {
		t.Fatal("no Retry-After when none is known")
	}
}
