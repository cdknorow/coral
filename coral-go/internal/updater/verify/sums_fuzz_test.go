package verify

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

// #5372: SHA256SUMS.txt and promote.json come from the network, and are read before and after their signatures are checked. Whatever the bytes: no
// panic; what is accepted is exactly what the format says, and reads back the same after it is written again.
//
//	go test ./internal/updater/verify -run '^$' -fuzz FuzzParseSums -fuzztime 10m
func FuzzParseSums(f *testing.F) {
	h := strings.Repeat("ab", 32)
	for _, s := range []string{
		h + "  Coral.dmg\n", h + " *Coral.dmg\n", h + "  a\n" + h + "  b\n", "", "\n", h + "  \n", h + "  a/b\n", h + "  a\\b\n", h + "  ..\n", h + "  a\n" + h + "  a\n",
		strings.ToUpper(h) + "  a\n", h + "  a\r\n", h + "\t\ta\n", h[:63] + "  a\n", h + "  a\x00b\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := ParseSums(data)
		if err != nil {
			return
		}
		if len(got) == 0 {
			t.Fatalf("ParseSums accepted %q with no entry", data)
		}
		names := make([]string, 0, len(got))
		for name, sum := range got {
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00\n") {
				t.Fatalf("ParseSums accepted the file name %q from %q", name, data)
			}
			if !hex64.MatchString(sum) {
				t.Fatalf("ParseSums accepted the digest %q for %q", sum, name)
			}
			names = append(names, name)
		}
		sort.Strings(names)
		var b strings.Builder
		for _, n := range names {
			fmt.Fprintf(&b, "%s  %s\n", got[n], n)
		}
		again, err := ParseSums([]byte(b.String()))
		if err != nil || len(again) != len(got) {
			t.Fatalf("what ParseSums accepted does not read back: %v (%d entries then %d)", err, len(got), len(again))
		}
		for n, s := range got {
			if again[n] != s {
				t.Fatalf("%q read back as %q, was %q", n, again[n], s)
			}
		}
		// The stable-name rule never panics and never fails on a file that lists neither name.
		_ = CheckStableAliases(got, "1.4.130")
	})
}

func FuzzParseManifest(f *testing.F) {
	valid := `{"schema":1,"repo":"cdknorow/coral","tag":"v1.4.130","version":"1.4.130","prerelease":false,"source_sha":"` + strings.Repeat("a", 40) + `","sha256sums_sha256":"` + strings.Repeat("b", 64) + `","sequence":7,"promoted_at":"2026-10-20T10:00:00Z","expires_at":"2026-11-20T10:00:00Z","revoked":["1.4.99"]}`
	f.Add([]byte(valid))
	f.Add([]byte(strings.Replace(valid, `"schema":1`, `"schema":2`, 1)))
	f.Add([]byte(strings.Replace(valid, `"revoked":["1.4.99"]`, `"revoked":null`, 1)))
	f.Add([]byte(valid + "x"))
	f.Add([]byte(valid + valid))
	f.Add([]byte(strings.Replace(valid, `"tag":"v1.4.130"`, `"tag":"v1.4.131"`, 1)))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(strings.Replace(valid, `"sequence":7`, `"sequence":-1`, 1)))
	f.Add([]byte(strings.Replace(valid, `"version":"1.4.130"`, `"version":"01.4.130"`, 1)))
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := ParseManifest(data)
		if err != nil {
			return
		}
		if m.Schema != 1 || m.Tag != "v"+m.Version || m.Sequence < 1 || !hex40.MatchString(m.SourceSHA) || !hex64.MatchString(m.SHA256SumsSHA) || len(m.Revoked) > maxRevoked {
			t.Fatalf("ParseManifest accepted a document that breaks its own rules: %+v", *m)
		}
		if _, err := ParseVersion(m.Version); err != nil {
			t.Fatalf("ParseManifest accepted the version %q: %v", m.Version, err)
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		m2, err := ParseManifest(b)
		if err != nil {
			t.Fatalf("an accepted manifest does not read back: %v\n%s", err, b)
		}
		if m2.Version != m.Version || m2.Sequence != m.Sequence || m2.SHA256SumsSHA != m.SHA256SumsSHA || m2.SourceSHA != m.SourceSHA {
			t.Fatalf("a manifest read back differently: %+v then %+v", *m, *m2)
		}
		// The rules against the installed version never panic, whatever the manifest says.
		_ = CheckManifest(m, DefaultPins(), State{}, "1.4.129", time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	})
}
