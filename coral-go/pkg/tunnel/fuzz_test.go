package tunnel

import (
	"bytes"
	"encoding/binary"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

// Fuzz targets for what the Hub tunnel reads from a peer (#4147). A plain `go test` runs the seeds
// only; `go test -fuzz FuzzDecodeHeaderLimits -fuzztime 10m ./pkg/tunnel/` fuzzes. A crasher goes into
// testdata/fuzz/<target>/ as a regression seed (go test does that for you).

// FuzzDecodeHeaderLimits (beside the basic FuzzDecodeHeader): the stream Header a peer sends. It never panics, never accepts what its own
// limits refuse, and what it accepts passes validation again.
func FuzzDecodeHeaderLimits(f *testing.F) {
	for _, s := range []string{
		`{"kind":"http","method":"GET","path":"/api/sessions/live"}`,
		`{"kind":"http","method":"POST","path":"/api/board/x/messages","query":"a=b","content_length":12,"hub_user":"hub-user-0a1b2c3d","hub_tier":"interact","headers":{"Content-Type":["application/json"]}}`,
		`{"kind":"ws","path":"/ws/terminal/claude-1","protocols":["a","b"]}`,
		`{"kind":"http","method":"GET","path":"/a\u0000b"}`,
		`{"kind":"http","method":"G ET","path":"/x"}`,
		`{"kind":"http","method":"GET","path":"/x","content_length":-5}`,
		`{"kind":"http","method":"GET","path":"/x","hub_user":"hub-user-ZZZZZZZZ"}`,
		`{"kind":"http","method":"GET","path":"/x","headers":{"Bad Name":["v"]}}`,
		`{"kind":"nope"}`, `{}`, `[]`, `null`, ``, `{"kind":"http","path":"/x","path":"/y","method":"GET"}`,
		`{"kind":"http","method":"GET","path":"/é😀"}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		l := Limits{}.withDefaults()
		h, err := decodeHeader(b, Limits{})
		if err != nil {
			if len(b) > l.MaxHeaderBytes && err != ErrHeaderTooLarge {
				t.Fatalf("over-long input answered %v, want ErrHeaderTooLarge", err)
			}
			return
		}
		if len(b) > l.MaxHeaderBytes {
			t.Fatalf("accepted %d bytes over the %d limit", len(b), l.MaxHeaderBytes)
		}
		if h.Kind != KindHTTP && h.Kind != KindWS {
			t.Fatalf("accepted kind %q", h.Kind)
		}
		if err := h.validate(l); err != nil {
			t.Fatalf("an accepted header fails validation again: %v", err)
		}
		if !utf8.ValidString(h.Path) || !strings.HasPrefix(h.Path, "/") && h.Path == "" {
			t.Fatalf("path %q", h.Path)
		}
		for _, s := range append([]string{h.Method, h.Path, h.Query}, h.Protocols...) {
			for i := 0; i < len(s); i++ {
				if s[i] < 0x20 || s[i] == 0x7f {
					t.Fatalf("a control byte in an accepted field %q", s)
				}
			}
		}
		if len(h.Headers) > l.MaxHeaders {
			t.Fatalf("%d headers", len(h.Headers))
		}
		for k, vs := range h.Headers {
			if strings.ContainsAny(k, " \t:") || k == "" {
				t.Fatalf("header name %q", k)
			}
			for _, v := range vs {
				if len(v) > l.MaxHeaderValue {
					t.Fatalf("a %d byte header value", len(v))
				}
			}
		}
		if h.HubUser != "" && !ValidHubUser(h.HubUser) {
			t.Fatalf("hub user %q", h.HubUser)
		}
		if h.ContentLength < -1 {
			t.Fatalf("content length %d", h.ContentLength)
		}
	})
}

// FuzzReadFrame: the length-prefixed frame reader. A length above the limit is refused before
// anything that size is allocated; what it returns is no longer than the limit and is exactly what
// was sent; a short read is an error, never a panic or a hang.
func FuzzReadFrame(f *testing.F) {
	frame := func(n uint32, body string) []byte {
		b := make([]byte, 4, 4+len(body))
		binary.BigEndian.PutUint32(b, n)
		return append(b, body...)
	}
	f.Add(frame(2, "{}"), 64)
	f.Add(frame(0, ""), 64)
	f.Add(frame(5, "ab"), 64)            // short body
	f.Add(frame(0xFFFFFFFF, "x"), 1<<20) // huge length, tiny body
	f.Add(frame(65, strings.Repeat("a", 65)), 64)
	f.Add([]byte{0, 0}, 64) // short prefix
	f.Add([]byte{}, 0)
	f.Fuzz(func(t *testing.T, in []byte, max int) {
		if max < 0 || max > 1<<20 {
			max %= 1 << 20
			if max < 0 {
				max = -max
			}
		}
		got, err := readFrame(bytes.NewReader(in), max)
		if err != nil {
			return
		}
		if len(got) > max {
			t.Fatalf("returned %d bytes over the limit %d", len(got), max)
		}
		if len(in) < 4 || int(binary.BigEndian.Uint32(in[:4])) != len(got) || !bytes.Equal(got, in[4:4+len(got)]) {
			t.Fatalf("returned bytes are not the frame that was sent")
		}
	})
}

// FuzzClassifyTable (beside FuzzClassifyNeverExposesAbsoluteReads): the connector's allowlist for a tunnelled request. Whatever the method, path and
// query, it never panics, answers a real tier, agrees with RuleFor, and never lets through what the
// table's own rules refuse: a path that isn't clean, an auth, settings or cloud route, or a query
// that names a credential.
func FuzzClassifyTable(f *testing.F) {
	for _, s := range [][3]string{
		{"GET", "/api/sessions/live", ""},
		{"GET", "/api/sessions/live", "api_key=x"},
		{"POST", "/api/sessions/live/claude-1/send", ""},
		{"GET", "/api/auth/password", ""},
		{"GET", "/auth", "login_token=x"},
		{"PUT", "/api/settings", ""},
		{"GET", "/api/cloud/status", ""},
		{"GET", "/api/files/read", "path=../../etc/passwd"},
		{"GET", "/api/files/read", "path=%2e%2e%2fsecret"},
		{"GET", "//api/sessions/live", ""},
		{"GET", "/api/sessions/./live", ""},
		{"GET", "/api/sessions/../auth", ""},
		{"GET", "/api\\sessions", ""},
		{"get", "/api/sessions/live", ""},
		{"HEAD", "/api/sessions/live", ""},
		{"GET", "/api/system/api-key", ""},
		{"GET", "/api/artifacts/" + strings.Repeat("a", 64), ""},
		{"", "", ""},
	} {
		f.Add(s[0], s[1], s[2])
	}
	f.Fuzz(func(t *testing.T, method, path, query string) {
		tier := Classify(method, path, query)
		if tier != TierNever && tier != TierView && tier != TierInteract && tier != TierLaunch {
			t.Fatalf("tier %d", tier)
		}
		if tier == TierNever {
			return
		}
		// Allowed: the path is clean and the rule table agrees.
		segs, ok := splitSegments(path)
		if !ok {
			t.Fatalf("allowed (%v) an unclean path %q", tier, path)
		}
		for _, s := range segs {
			if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "\\\x00") {
				t.Fatalf("allowed a segment %q in %q", s, path)
			}
		}
		pat, rt, ok := RuleFor(method, path)
		if !ok || rt != tier {
			t.Fatalf("Classify says %v for %q but RuleFor says %v %v (%s)", tier, path, rt, ok, pat)
		}
		if strings.HasPrefix(path, "/api/auth") || path == "/auth" || strings.HasPrefix(path, "/auth/") || strings.HasPrefix(path, "/api/settings") || strings.HasPrefix(path, "/api/cloud") || path == "/api/system/api-key" {
			t.Fatalf("allowed the never-routes path %q as %v", path, tier)
		}
		if query != "" {
			q, err := url.ParseQuery(query)
			if err != nil {
				t.Fatalf("allowed an unparseable query %q", query)
			}
			for name := range q {
				switch strings.ToLower(name) {
				case "api_key", "login_token", "token", "key", "password", "authorization", "session_token":
					t.Fatalf("allowed a credential parameter %q in %q", name, query)
				}
			}
		}
		if got := Classify(method, path, query); got != tier {
			t.Fatalf("not deterministic: %v then %v", tier, got)
		}
	})
}
