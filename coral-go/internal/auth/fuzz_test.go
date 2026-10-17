package auth

import (
	"net"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Fuzz targets for what the sign-in code reads from a person, a file or a header (#4147). A plain
// go test runs the seeds; go test -fuzz FuzzParsePHC -fuzztime 10m ./internal/auth/ fuzzes.

// FuzzNormalizeSetupCode: what a person types for the setup code. Cleaning it again changes nothing,
// no separator or look-alike letter is left (O, I and L become 0 and 1), and a real code written
// with dashes, spaces or lower case normalises back to itself.
func FuzzNormalizeSetupCode(f *testing.F) {
	for _, s := range []string{"ABCD-EFGH", "abcd efgh", "0123 4567", "OIL-oil", "", "-", "\t \t", "ABCDEFGH\x00", "ſſſſ-ıııı", "KK"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n := normalizeSetupCode(s)
		if again := normalizeSetupCode(n); again != n {
			t.Fatalf("not idempotent: %q then %q (from %q)", n, again, s)
		}
		if strings.ContainsAny(n, "- \tOIL") {
			t.Fatalf("separator or look-alike left in %q (from %q)", n, s)
		}
		// A real code survives the ways a person writes it.
		if len(s) == setupCodeLen && strings.Trim(s, setupAlphabet) == "" {
			if got := normalizeSetupCode(FormatSetupCode(s)); got != s {
				t.Fatalf("code %q formatted and cleaned is %q", s, got)
			}
			if got := normalizeSetupCode(strings.ToLower(s)); got != s {
				t.Fatalf("code %q in lower case cleaned is %q", s, got)
			}
		}
	})
}

// FuzzParsePHC: the password file's hash is read from disk, where it can be anything. It never
// panics, and what it accepts has costs inside the stated range and a salt and key of sane length,
// so a tampered file can't ask for more memory or time than the bound.
func FuzzParsePHC(f *testing.F) {
	for _, s := range []string{
		"$argon2id$v=19$m=65536,t=3,p=1$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=1048576,t=20,p=16$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=4294967295,t=1,p=1$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=65536,t=3,p=256$c2FsdA$a2V5",
		"$argon2i$v=19$m=65536,t=3,p=1$c2FsdA$a2V5",
		"$argon2id$v=18$m=65536,t=3,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m,t=3,p=1$c2FsdA$a2V5", "", "$", "$$$$$$", "\n$argon2id$v=19$m=65536,t=3,p=1$AAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAA\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, phc string) {
		p, err := parsePHC(phc)
		if err != nil {
			return
		}
		if p.memory < 8*1024 || p.memory > 1024*1024 || p.time < 1 || p.time > 20 || p.threads < 1 || p.threads > 16 {
			t.Fatalf("accepted costs outside the bound: m=%d t=%d p=%d", p.memory, p.time, p.threads)
		}
		if len(p.salt) < 8 || len(p.key) < 16 || len(p.key) > 64 {
			t.Fatalf("accepted salt %d and key %d bytes", len(p.salt), len(p.key))
		}
	})
}

// FuzzNormalizePassword: the password as hashed is NFKC and bounded. Whatever it accepts is
// normalised, not empty, not over the maximum, and normalising it again changes nothing.
func FuzzNormalizePassword(f *testing.F) {
	for _, s := range []string{"correct horse", "", " ", "é", "é", "ＡＢＣ", "ﬁ", strings.Repeat("a", MaxPasswordBytes), strings.Repeat("a", MaxPasswordBytes+1), "\x00", "\xff\xfe", strings.Repeat("ﬃ", 2000)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, pw string) {
		b, err := normalizePassword(pw)
		if err != nil {
			if pw == "" && err != ErrPasswordEmpty {
				t.Fatalf("empty password gave %v", err)
			}
			return
		}
		if len(b) == 0 || len(b) > MaxPasswordBytes {
			t.Fatalf("accepted %d bytes", len(b))
		}
		if !norm.NFKC.IsNormal(b) {
			t.Fatalf("not NFKC: %q", b)
		}
		again, err := normalizePassword(string(b))
		if err != nil || string(again) != string(b) {
			t.Fatalf("not idempotent: %q then %q (%v)", b, again, err)
		}
	})
}

// FuzzCanonicalOrigin: the return origin offered for a Hub sign-in. What it accepts is only a
// scheme, a host and a port (no user, path, query or fragment), lower case, https to anything or
// http only to localhost or a loopback, private or Tailscale address, with no default port, and
// canonicalising it again changes nothing.
func FuzzCanonicalOrigin(f *testing.F) {
	for _, s := range []string{
		"https://example.com", "https://EXAMPLE.com:443", "http://localhost:8420", "http://127.0.0.1:8420/", "http://192.168.1.5:8420",
		"http://[::1]:8420", "http://100.64.0.1", "http://example.com", "https://user:pw@example.com", "https://example.com/path", "https://example.com?x=1",
		"https://example.com#f", "https://*.example.com", "https://example.com.", "http://[fd7a:115c:a1e0::1]", "http://0x7f.1", "http://127.1", "http://8.8.8.8",
		"ftp://example.com", "https://", "", "  https://example.com  ", "https://exa mple.com", "https://example.com:0", "https://example.com:65536", "http://[::ffff:127.0.0.1]",
		"https://éxample.com", "HTTP://LOCALHOST", "javascript:alert(1)", "//example.com",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, err := CanonicalOrigin(s)
		if err != nil {
			return
		}
		u, perr := url.Parse(out)
		if perr != nil {
			t.Fatalf("output %q does not parse: %v", out, perr)
		}
		if u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Host == "" {
			t.Fatalf("output %q carries more than a scheme, host and port", out)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			t.Fatalf("scheme %q", u.Scheme)
		}
		if out != strings.ToLower(out) && !strings.ContainsAny(out, "[]") {
			// the host part is lower case (a name or an address); nothing else may carry capitals
			if strings.ToLower(u.Host) != u.Host {
				t.Fatalf("host not lower case in %q", out)
			}
		}
		if strings.ContainsAny(u.Host, "*% ") || strings.HasSuffix(u.Hostname(), ".") {
			t.Fatalf("host %q", u.Host)
		}
		if p := u.Port(); p == "0" || (u.Scheme == "https" && p == "443") || (u.Scheme == "http" && p == "80") {
			t.Fatalf("port %q in %q", p, out)
		}
		if u.Scheme == "http" {
			h := u.Hostname()
			a, err := netip.ParseAddr(h)
			private := err == nil && (a.Unmap().IsLoopback() || a.Unmap().IsPrivate() || tailscaleV4.Contains(a.Unmap()) || tailscaleV6.Contains(a.Unmap()))
			if h != "localhost" && !private {
				t.Fatalf("http allowed to %q in %q", h, out)
			}
		}
		if again, err := CanonicalOrigin(out); err != nil || again != out {
			t.Fatalf("not idempotent: %q then %q (%v)", out, again, err)
		}
	})
}

// FuzzIsLoopbackHost: a host the server treats as this machine is localhost or a loopback address,
// nothing that merely starts like one (127.0.0.1.example.com, localhost.evil.test, a zone, a port).
func FuzzIsLoopbackHost(f *testing.F) {
	for _, s := range []string{"localhost", "LocalHost", "127.0.0.1", "127.255.255.255", "::1", "[::1]", "127.0.0.1.example.com", "localhost.evil.test", "localhost:8420", "127.0.0.1:8420",
		"::ffff:127.0.0.1", "::1%eth0", "0.0.0.0", "128.0.0.1", "", " localhost", "localhost ", "127.1", "0177.0.0.1", "2130706433", "Klocalhost"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, h string) {
		if !IsLoopbackHost(h) {
			return
		}
		if strings.EqualFold(h, "localhost") {
			return
		}
		ip := net.ParseIP(h)
		if ip == nil || !ip.IsLoopback() {
			t.Fatalf("accepted %q", h)
		}
		if !utf8.ValidString(h) || strings.ContainsAny(h, " \t\n%/") {
			t.Fatalf("accepted %q with a separator", h)
		}
	})
}
