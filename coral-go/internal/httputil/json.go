// Package httputil provides shared HTTP response helpers.
package httputil

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// WriteJSON encodes v as JSON and writes it to w with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("JSON encode error: %v", err)
	}
}

// gzipMinBytes is the smallest body WriteJSONCompressed gzips; below it the
// header and CPU cost outweigh the saving.
const gzipMinBytes = 8 << 10

// WriteJSONCompressed is WriteJSON for large responses: when the client
// accepts gzip and the body is at least gzipMinBytes it is sent gzipped.
// Transcript JSON shrinks four to five times, which matters when a remote's
// chat history crosses a real network through the hub.
func WriteJSONCompressed(w http.ResponseWriter, r *http.Request, status int, v any) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(v); err != nil {
		log.Printf("JSON encode error: %v", err)
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "encode failed"})
		return
	}
	h := w.Header()
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

// AcceptsGzip reports whether the request's Accept-Encoding allows gzip:
// listed by name, or covered by "*", and not refused with q=0. An explicit
// gzip entry wins over "*".
func AcceptsGzip(r *http.Request) bool {
	gz, star := -1, -1 // -1 not listed, 0 refused, 1 accepted
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		ok := 1
		if q, found := strings.CutPrefix(strings.ReplaceAll(params, " ", ""), "q="); found {
			if v, err := strconv.ParseFloat(q, 64); err == nil && v == 0 {
				ok = 0
			}
		}
		switch strings.ToLower(strings.TrimSpace(coding)) {
		case "gzip", "x-gzip":
			gz = ok
		case "*":
			star = ok
		}
	}
	if gz >= 0 {
		return gz == 1
	}
	return star == 1
}
