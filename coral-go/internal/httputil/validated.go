package httputil

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// ValidatorHeader carries the same validator as ETag. Coral Hub drops ETag (with every caching
// header, and sets no-store: one account's content on a shared origin must never sit in a browser
// cache), so through the hub the dashboard reads the validator from here and sends it back in
// If-None-Match itself. The browser's cache ignores this header; the value lives only in the
// page's memory, for the next poll of the same thing.
const ValidatorHeader = "X-Coral-Validator"

// WriteJSONValidated is WriteJSONCompressed for a hot poll whose answer is often unchanged
// (#3439): the response carries a weak ETag (and ValidatorHeader), a hash of its JSON, and
// Cache-Control: no-cache, and a request whose If-None-Match names it gets 304 Not Modified with
// no body. Weak, because the same JSON goes out gzipped or not. Only a 200 is validated.
func WriteJSONValidated(w http.ResponseWriter, r *http.Request, status int, v any) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(v); err != nil {
		log.Printf("JSON encode error: %v", err)
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "encode failed"})
		return
	}
	h := w.Header()
	if status == http.StatusOK {
		sum := sha256.Sum256(body.Bytes())
		etag := `W/"` + hex.EncodeToString(sum[:12]) + `"`
		h.Set("ETag", etag)
		h.Set(ValidatorHeader, etag)
		h.Set("Cache-Control", "no-cache")
		if ETagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	h.Set("Content-Type", "application/json")
	h.Add("Vary", "Accept-Encoding")
	if body.Len() < gzipMinBytes || !AcceptsGzip(r) {
		h.Set("Content-Length", strconv.Itoa(body.Len()))
		w.WriteHeader(status)
		_, _ = w.Write(body.Bytes())
		return
	}
	var gz bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&gz, gzip.BestSpeed)
	_, _ = zw.Write(body.Bytes())
	_ = zw.Close()
	h.Set("Content-Encoding", "gzip")
	h.Set("Content-Length", strconv.Itoa(gz.Len()))
	w.WriteHeader(status)
	_, _ = w.Write(gz.Bytes())
}

// ETagMatches reports whether an If-None-Match header names etag, by weak comparison
// (RFC 9110 13.1.2): "*", or any listed tag equal to it once a W/ prefix is ignored.
func ETagMatches(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" || etag == "" {
		return false
	}
	want := strings.TrimPrefix(etag, "W/")
	for _, part := range strings.Split(ifNoneMatch, ",") {
		tag := strings.TrimSpace(part)
		if tag == "*" || strings.TrimPrefix(tag, "W/") == want {
			return true
		}
	}
	return false
}
