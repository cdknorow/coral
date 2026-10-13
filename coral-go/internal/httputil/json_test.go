package httputil

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcceptsGzip(t *testing.T) {
	cases := map[string]bool{
		"":                      false,
		"gzip":                  true,
		"gzip, deflate, br":     true,
		"br;q=1.0, GZIP;q=0.5":  true,
		"x-gzip":                true,
		"deflate, br":           false,
		"gzip;q=0":              false,
		"gzip; q=0.0, br":       false,
		"*":                     true,
		"*;q=0":                 false,
		"gzip;q=0, *":           false,
		"identity, *;q=0, gzip": true,
	}
	for header, want := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			r.Header.Set("Accept-Encoding", header)
		}
		assert.Equal(t, want, AcceptsGzip(r), "Accept-Encoding %q", header)
	}
}

func TestWriteJSONCompressed(t *testing.T) {
	large := map[string]any{"messages": strings.Repeat("transcript text ", gzipMinBytes/8)}
	small := map[string]any{"messages": "short"}

	write := func(v any, acceptEncoding string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if acceptEncoding != "" {
			r.Header.Set("Accept-Encoding", acceptEncoding)
		}
		w := httptest.NewRecorder()
		WriteJSONCompressed(w, r, http.StatusOK, v)
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
		assert.Equal(t, "Accept-Encoding", w.Header().Get("Vary"))
		assert.Equal(t, strconv.Itoa(w.Body.Len()), w.Header().Get("Content-Length"))
		return w
	}
	decode := func(body io.Reader) map[string]any {
		var got map[string]any
		require.NoError(t, json.NewDecoder(body).Decode(&got))
		return got
	}

	t.Run("large body is gzipped when accepted", func(t *testing.T) {
		w := write(large, "gzip, deflate")
		require.Equal(t, "gzip", w.Header().Get("Content-Encoding"))
		raw, _ := json.Marshal(large)
		assert.Less(t, w.Body.Len(), len(raw)/4, "repetitive JSON compresses well")
		zr, err := gzip.NewReader(w.Body)
		require.NoError(t, err)
		assert.Equal(t, large, decode(zr))
	})
	t.Run("large body is plain when gzip is not accepted", func(t *testing.T) {
		for _, ae := range []string{"", "br", "gzip;q=0"} {
			w := write(large, ae)
			assert.Empty(t, w.Header().Get("Content-Encoding"), ae)
			assert.Equal(t, large, decode(w.Body), ae)
		}
	})
	t.Run("small body is plain even when gzip is accepted", func(t *testing.T) {
		w := write(small, "gzip")
		assert.Empty(t, w.Header().Get("Content-Encoding"))
		assert.Equal(t, small, decode(w.Body))
	})
	t.Run("unencodable value is a 500", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		WriteJSONCompressed(w, r, http.StatusOK, map[string]any{"bad": func() {}})
		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Empty(t, w.Header().Get("Content-Encoding"))
	})
}
