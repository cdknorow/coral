package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAutoAcceptOffAll(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.Path + " " + r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stopped":3}`))
	}))
	defer srv.Close()
	var out, errb bytes.Buffer
	assert.Equal(t, 0, runAutoAccept([]string{"off", "--all", "--url", srv.URL}, &out, &errb), errb.String())
	assert.Equal(t, "POST /api/auto-accept/disable-all application/json", got)
	assert.Contains(t, out.String(), "3 turned off")

	t.Setenv("CORAL_URL", srv.URL)
	out.Reset()
	assert.Equal(t, 0, runAutoAccept([]string{"off", "--all"}, &out, &errb), "CORAL_URL is the default server")

	for _, bad := range [][]string{nil, {"on"}, {"off"}, {"off", "--all", "extra"}} {
		assert.Equal(t, 2, runAutoAccept(bad, &out, &errb), "%v", bad)
	}
	srv.Close()
	assert.Equal(t, 1, runAutoAccept([]string{"off", "--all", "--url", srv.URL}, &out, &errb), "server down")

	code, ok := subcommand([]string{"--port", "8450"})
	assert.False(t, ok, "flags still start the server")
	assert.Zero(t, code)
}
