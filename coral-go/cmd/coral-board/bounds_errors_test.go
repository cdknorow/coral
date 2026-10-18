package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #4602 M10: the refusals of the new bounds (a wait count, a reason, a task's size or count) carry numbers and flags beside
// their words; coral-board prints the words, not "HTTP 409: {json}".
func TestCLIPrintsTheWordsOfABoundsRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/waits"):
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":"a subscriber can have at most 50 active waits (one per board): cancel one first","message":"a subscriber can have at most 50 active waits (one per board): cancel one first","code":"too_many_waits","max":50,"not_saved":true}`)
		case strings.HasSuffix(r.URL.Path, "/tasks"):
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			fmt.Fprint(w, `{"error":"a task title is at most 512 bytes (this one is 900): shorten it","message":"a task title is at most 512 bytes (this one is 900): shorten it","code":"title_too_large","max_bytes":512,"not_saved":true}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	dir := subscribedDataDir(t, srv.URL)

	run := runBoard(t, srv.URL, dir, "wait", "--from", "someone")
	assert.NotEqual(t, 0, run.code)
	assert.Contains(t, run.stderr, "Error: a subscriber can have at most 50 active waits")
	assert.NotContains(t, run.stderr, "HTTP 409", "the words, not the status and a JSON blob")

	run = runBoard(t, srv.URL, dir, "task", "add", strings.Repeat("t", 900))
	assert.NotEqual(t, 0, run.code)
	assert.Contains(t, run.stderr, "a task title is at most 512 bytes")
}
