package server

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3763: a handler's panic is answered 500 and logged through the log package (so through
// coral.log's redaction), not printed to stderr; http.ErrAbortHandler is passed on.
func TestRecovererLogsThroughTheLogPackage(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	h := recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("bad input: token=abc123") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/x", nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, buf.String(), "[panic] GET /api/x: bad input")

	abort := recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) }))
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() { abort.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)) })
}
