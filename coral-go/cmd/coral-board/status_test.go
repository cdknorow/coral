package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBoardStatusCurrentAndExplicitBoard(t *testing.T) {
	isolateBoardEnv(t)
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"board":"routing","agents":[{"subscriber_id":"QA","available":true}],"unassigned_tasks":[{"id":9007199254740993}]}`))
	}))
	defer server.Close()
	serverURL = server.URL
	saveState(&boardState{Project: "routing", ServerURL: server.URL})
	var out bytes.Buffer
	require.NoError(t, runBoardStatus(nil, &out))
	require.Equal(t, "/api/board/routing/status", path)
	require.True(t, json.Valid(out.Bytes()))
	require.Contains(t, out.String(), "9007199254740993")
	out.Reset()
	require.NoError(t, runBoardStatus([]string{"--board", "other board"}, &out))
	require.Equal(t, "/api/board/other board/status", path)
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
