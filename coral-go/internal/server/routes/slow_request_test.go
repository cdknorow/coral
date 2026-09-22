package routes

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDebugRequestLogger_LogsSlowAPIRequests(t *testing.T) {
	old := slowRequestThreshold
	slowRequestThreshold = time.Millisecond
	defer func() { slowRequestThreshold = old }()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	slow := DebugRequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
	}))

	for _, tc := range []struct {
		path   string
		logged bool
	}{
		{"/api/sessions/live/x/files", true},
		{"/ws/terminal", false},   // long-lived by design
		{"/static/app.js", false}, // not an API call
	} {
		buf.Reset()
		slow.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", tc.path, nil))
		assert.Equal(t, tc.logged, bytes.Contains(buf.Bytes(), []byte("slow request")), "path %s", tc.path)
	}
}
