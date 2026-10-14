package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3418: coral-agent notify replaces a hook's curl to /api/notifications, so it carries
// the agent's identity and works with strict local auth.
func TestNotify(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/notifications", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()
	prev := serverURL
	serverURL = srv.URL
	defer func() { serverURL = prev }()

	var out bytes.Buffer
	require.NoError(t, cmdNotify([]string{"--title", "Done", "--message", "tests pass", "--level", "success", "--alert"}, &out))
	assert.Equal(t, map[string]string{"title": "Done", "message": "tests pass", "level": "success", "type": "alert"}, got)
	assert.Contains(t, out.String(), "sent")

	assert.Error(t, cmdNotify([]string{"--message", "no title"}, &out))
	assert.Error(t, cmdNotify([]string{"--title", "x", "--level", "loud"}, &out))
	assert.Error(t, cmdNotify([]string{"--title", "x", "stray"}, &out))
}
