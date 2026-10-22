package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/clierr"
)

func TestBoardStatusCurrentAndExplicitBoard(t *testing.T) {
	isolateBoardEnv(t)
	var path, query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		path = r.URL.Path
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"board":"routing","agents":[{"subscriber_id":"QA","available":true}],"unassigned_tasks":[{"id":9007199254740993}]}`))
	}))
	defer server.Close()
	serverURL = server.URL
	saveState(&boardState{Project: "routing", JobTitle: "Orchestrator", ServerURL: server.URL})
	var out bytes.Buffer
	require.NoError(t, runBoardStatus(nil, &out))
	require.Equal(t, "/api/board/routing/status", path)
	require.True(t, json.Valid(out.Bytes()))
	require.Contains(t, out.String(), "9007199254740993")
	out.Reset()
	require.NoError(t, runBoardStatus([]string{"--board", "other board"}, &out))
	require.Equal(t, "/api/board/other board/status", path)
	out.Reset()
	require.NoError(t, runBoardStatus([]string{"--activity"}, &out))
	require.Equal(t, "/api/board/routing/status", path)
	require.Equal(t, "activity=1&subscriber_id=Orchestrator", query)
}

func TestBoardStatusErrors(t *testing.T) {
	isolateBoardEnv(t)
	var out bytes.Buffer
	require.ErrorContains(t, runBoardStatus(nil, &out), "not subscribed")
	require.ErrorContains(t, runBoardStatus([]string{"extra"}, &out), "usage")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }))
	defer server.Close()
	serverURL = server.URL
	require.ErrorContains(t, runBoardStatus([]string{"--board", "routing"}, &out), "HTTP 503")
	require.Empty(t, out.String())
}

// #285: a board the server says is not there (exists:false) is not found (exit 6), with nothing on stdout; a board that exists,
// and a server from before the field, print the status as before.
func TestBoardStatusOfAnAbsentBoard(t *testing.T) {
	isolateBoardEnv(t)
	reply := `{"board":"nope","exists":false,"agents":[],"health_report":["No queue health issues detected."]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(reply))
	}))
	defer server.Close()
	serverURL = server.URL
	var out bytes.Buffer
	clierr.Note(clierr.Other)
	defer clierr.Note(clierr.Other)
	require.ErrorContains(t, runBoardStatus([]string{"--board", "nope"}, &out), "Board not found: nope")
	require.Empty(t, out.String())
	require.Equal(t, clierr.ExitNotFound, clierr.Noted().Code())

	for _, r := range []string{`{"board":"yes","exists":true,"agents":[]}`, `{"board":"old","agents":[]}`} {
		reply = r
		out.Reset()
		require.NoError(t, runBoardStatus([]string{"--board", "x"}, &out), r)
		require.True(t, json.Valid(out.Bytes()))
	}
}
