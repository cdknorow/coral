package routes

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5262: every error reply is {"error": msg} with an optional "code". The err* helpers and writeError must write the bytes the hand-written literals wrote (encoding/json sorts a map's keys, so the
// order of the keys in a literal never mattered), whatever the status, the text (quotes, HTML characters, unicode) or the map's value type.
func TestErrorHelpersWriteTheEnvelopeTheLiteralsWrote(t *testing.T) {
	texts := []string{"invalid JSON", "", `a "quoted" word`, "<b>&amp; tags</b>", "ünïcode ✓ \u2028", "line\nbreak"}
	type reply struct {
		status int
		ctype  string
		body   string
	}
	capture := func(f func(w http.ResponseWriter)) reply {
		rec := httptest.NewRecorder()
		f(rec)
		return reply{rec.Code, rec.Header().Get("Content-Type"), rec.Body.String()}
	}
	helpers := map[int]func(http.ResponseWriter, string){
		http.StatusBadRequest: errBadRequest, http.StatusNotFound: errNotFound, http.StatusInternalServerError: errInternalServer,
		http.StatusForbidden: errForbidden, http.StatusConflict: errConflict, http.StatusRequestEntityTooLarge: errPayloadTooLarge,
	}
	for _, msg := range texts {
		for status, h := range helpers {
			want := capture(func(w http.ResponseWriter) { writeJSON(w, status, map[string]string{"error": msg}) })
			assert.Equal(t, want, capture(func(w http.ResponseWriter) { h(w, msg) }), "helper for %d, %q", status, msg)
			assert.Equal(t, want, capture(func(w http.ResponseWriter) { writeError(w, status, "", msg) }), "writeError for %d, %q", status, msg)
			// a literal typed map[string]any wrote the same bytes for string values
			assert.Equal(t, want, capture(func(w http.ResponseWriter) { writeJSON(w, status, map[string]any{"error": msg}) }))
		}
		for _, status := range []int{http.StatusBadRequest, http.StatusConflict, http.StatusServiceUnavailable, http.StatusUnauthorized, http.StatusUnsupportedMediaType, http.StatusBadGateway, http.StatusInsufficientStorage} {
			for _, code := range []string{"terminal_gone", "body_too_large", "session_id_required"} {
				want := capture(func(w http.ResponseWriter) { writeJSON(w, status, map[string]string{"error": msg, "code": code}) })
				assert.Equal(t, want, capture(func(w http.ResponseWriter) { writeError(w, status, code, msg) }), "writeError %d %s %q", status, code, msg)
				assert.Equal(t, want, capture(func(w http.ResponseWriter) { writeJSON(w, status, map[string]any{"code": code, "error": msg}) }))
			}
		}
	}
	// the shapes clients see, pinned as text
	assert.Equal(t, reply{400, "application/json", "{\"error\":\"invalid JSON\"}\n"}, capture(func(w http.ResponseWriter) { errBadRequest(w, "invalid JSON") }))
	assert.Equal(t, reply{409, "application/json", "{\"code\":\"terminal_gone\",\"error\":\"gone\"}\n"}, capture(func(w http.ResponseWriter) { writeError(w, 409, "terminal_gone", "gone") }))
}

// The ratchet (#5262): a route answers an error through writeError or an err* helper, and reads a JSON body through decodeJSON, decodeJSONLimit or decodeJSONMax. A new hand-written
// {"error": ...} literal or a raw json.NewDecoder(r.Body) in a handler fails here, so the envelope and the limits stay in one place. A reply with fields beyond error and code
// (a limit, the field at fault) is a literal on purpose and is not matched; a decoder with options (DisallowUnknownFields) is listed below.
var (
	rawEnvelopeLiteral = regexp.MustCompile(`writeJSON\(w, http\.Status[A-Za-z]+, map\[string\](?:string|any|interface\{\})\{"error": [^{}]*?(?:, "code": [^{}]*?)?\}\)`)
	rawBodyDecoder     = regexp.MustCompile(`json\.NewDecoder\((?:r\.Body|io\.LimitReader\(r\.Body|http\.MaxBytesReader\(w, r\.Body)`)
	// files that decode a request body with options, and how many such decoders each has
	decoderWithOptions = map[string]int{"artifact_comments.go": 1, "cloud.go": 1}
)

func TestRoutesUseTheEnvelopeAndBodyHelpers(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	var literals, decoders []string
	counts := map[string]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "helpers.go" {
			continue
		}
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		for i, line := range strings.Split(string(b), "\n") {
			if m := rawEnvelopeLiteral.FindString(line); m != "" && onlyErrorAndCode(m) && !strings.Contains(line, "http.StatusOK") {
				literals = append(literals, fmt.Sprintf("%s:%d", f, i+1))
			}
			if rawBodyDecoder.MatchString(line) {
				counts[f]++
				if counts[f] > decoderWithOptions[f] {
					decoders = append(decoders, fmt.Sprintf("%s:%d", f, i+1))
				}
			}
		}
	}
	assert.Empty(t, literals, "write an error with writeError or an err* helper (helpers.go), not a writeJSON literal")
	assert.Empty(t, decoders, "read a JSON body with decodeJSON, decodeJSONLimit or decodeJSONMax (helpers.go), not json.NewDecoder(r.Body)")
}

// onlyErrorAndCode: the literal has no key but "error" and "code" (the keys are the quoted names before a colon).
func onlyErrorAndCode(lit string) bool {
	for _, m := range extraKeyRe.FindAllStringSubmatch(lit, -1) {
		if m[1] != "code" {
			return false
		}
	}
	return true
}

var extraKeyRe = regexp.MustCompile(`, "([a-z_]+)": `)
