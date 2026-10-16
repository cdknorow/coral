package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// #3640: a board name is one path segment in every wait call. A name with '?' and '#' must
// not end the path early (a query or fragment) or reach another route: register, block (the
// long poll), status and cancel all reach /api/board/<that name>/waits[...].
func TestCmdWait_BoardNameIsEscaped(t *testing.T) {
	isolateBoardEnv(t)
	const board = "team?x=1#frag"

	type call struct{ method, path, query string }
	var calls []call
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, call{r.Method, r.URL.Path, r.URL.RawQuery})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST":
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":1,"status":"active"}`))
		case r.Method == "GET" && r.URL.Path == "/api/board/"+board+"/waits":
			w.Write([]byte(`{"wait":null}`))
		case r.Method == "GET":
			// Any other GET ends a --block poll, wherever it went, so a wrong route fails the
			// test below instead of polling for ever.
			w.Write([]byte(`{"status":"resolved"}`))
		default:
			w.Write([]byte(`{"cancelled":true}`))
		}
	}))
	defer ts.Close()
	oldURL := serverURL
	serverURL = ts.URL
	defer func() { serverURL = oldURL }()
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	for _, args := range [][]string{
		{"--board", board, "--from", "Lead", "--block"},
		{"--board", board, "--status"},
		{"--board", board, "--cancel"},
	} {
		os.Args = append([]string{"coral-board", "wait"}, args...)
		cmdWait()
	}

	want := []call{
		{"POST", "/api/board/" + board + "/waits", ""},
		{"GET", "/api/board/" + board + "/waits/poll", "subscriber_id=coral-board-test-session&timeout=30"},
		{"GET", "/api/board/" + board + "/waits", "subscriber_id=coral-board-test-session"},
		{"DELETE", "/api/board/" + board + "/waits", "subscriber_id=coral-board-test-session"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v, want %+v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
}
