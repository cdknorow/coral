package cliauth

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// FuzzCLIVerify: the X-Coral-CLI-Auth header is read from any local program. Whatever it holds,
// Verify never panics and never accepts a header that is not the exact signature of this request
// for the nonce it issued; a wrong guess at the signature (one that reads as base64) uses up its nonce (once, right or wrong);
// and a header that names a nonce the server never issued does not touch another client's pending
// one. A plain go test runs the seeds; go test -fuzz FuzzCLIVerify -fuzztime 10m ./internal/cliauth/.
func FuzzCLIVerify(f *testing.F) {
	for _, s := range []string{"", ".", "a.b", "AAAA.BBBB", strings.Repeat("A", 43) + "." + strings.Repeat("B", 43), "x.y.z", ".mac", "nonce.", "%%%.%%%", "\x00.\x00", "A=.B=", strings.Repeat("A", 100000)} {
		f.Add(s, "GET", "/api/cloud/status", "", []byte(nil))
	}
	f.Add("a.b", "POST", "/api/cloud/max-mode", "mode=view", []byte(`{"mode":"view"}`))
	f.Fuzz(func(t *testing.T, header, method, path, rawQuery string, body []byte) {
		if !httpMethod(method) || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, " \r\n\x00?#") || strings.ContainsAny(rawQuery, "\r\n\x00 #") {
			t.Skip()
		}
		const token = "fuzz-token"
		a := NewCLIAuth(token)
		cn := nonce32()
		sn, _, err := a.Hello(b64.EncodeToString(cn))
		if err != nil {
			t.Fatal(err)
		}
		req := func(h string) *http.Request {
			// Built by hand: httptest.NewRequest parses the target and panics on a path like "/%".
			r := &http.Request{Method: method, URL: &url.URL{Scheme: "http", Host: "localhost", Path: path, RawQuery: rawQuery}, Header: http.Header{}}
			if h != "" {
				r.Header.Set(CLIAuthHeader, h)
			}
			return r
		}
		valid, err := SignCLIRequest(token, b64.EncodeToString(cn), sn, method, path, rawQuery, body, "")
		if err != nil {
			t.Fatal(err)
		}
		if a.Verify(req(header), body) && header != valid {
			t.Fatalf("accepted a header that is not this request's signature: %q", header)
		}
		// A guess at the signature for the real nonce: refused, and the nonce is used up.
		a2 := NewCLIAuth(token)
		sn2, _, _ := a2.Hello(b64.EncodeToString(cn))
		valid2, _ := SignCLIRequest(token, b64.EncodeToString(cn), sn2, method, path, rawQuery, body, "")
		if guess := sn2 + "." + header; guess != valid2 && a2.Verify(req(guess), body) {
			t.Fatalf("accepted a guessed signature %q", guess)
		}
		// A guess whose signature part reads as base64 uses the nonce up (once, right or wrong); one that
		// does not read is refused before the nonce is looked at, so it leaves it.
		_, readable := b64.DecodeString(header)
		if guessUsed := a2.Verify(req(valid2), body); guessUsed && readable == nil {
			t.Fatalf("a nonce was used twice (after the readable guess %q)", header)
		}
		// A header for a nonce this server never issued leaves the real client's nonce alone.
		a3 := NewCLIAuth(token)
		sn3, _, _ := a3.Hello(b64.EncodeToString(cn))
		valid3, _ := SignCLIRequest(token, b64.EncodeToString(cn), sn3, method, path, rawQuery, body, "")
		if header != "" && !strings.HasPrefix(header, sn3+".") {
			_ = a3.Verify(req(header), body)
			if !a3.Verify(req(valid3), body) {
				t.Fatalf("a header %q for another nonce burned the real client's", header)
			}
		}
	})
}

func nonce32() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i*7 + 3)
	}
	return b
}

// httpMethod is a method httptest.NewRequest accepts: letters only (the target is the header, not the method).
func httpMethod(m string) bool {
	if m == "" || len(m) > 16 {
		return false
	}
	for i := 0; i < len(m); i++ {
		if c := m[i]; !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}
