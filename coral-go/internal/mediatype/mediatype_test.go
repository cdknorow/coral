package mediatype

import "testing"

func TestResolve(t *testing.T) {
	md := "text/markdown; charset=utf-8"
	cases := []struct {
		declared string
		names    []string
		want     string
	}{
		{"", []string{"report.md"}, md},
		{"application/octet-stream", []string{"report.MD"}, md},
		{"Application/Octet-Stream; x=y", []string{"a.markdown"}, md},
		{"binary/octet-stream", []string{"a.md"}, md},
		{"text/markdown", []string{"x.bin"}, "text/markdown"},
		{"image/png", []string{"a.md"}, "image/png"},
		{"text/plain", []string{"a.md"}, "text/plain"},
		{"", []string{"noext", "later.md"}, md},
		{"", []string{"blob.unknownext123"}, ""},
		{"application/octet-stream", []string{"blob.unknownext123"}, "application/octet-stream"},
		{"", []string{"README"}, ""},
	}
	for _, c := range cases {
		if got := Resolve(c.declared, c.names...); got != c.want {
			t.Errorf("Resolve(%q,%v)=%q want %q", c.declared, c.names, got, c.want)
		}
	}
}

func TestEnsureExtension(t *testing.T) {
	cases := [][3]string{
		{"media-latency-audit-2026-10-05", "/tmp/x/audit.md", "media-latency-audit-2026-10-05.md"},
		{"report.md", "/tmp/a.txt", "report.md"},
		{"v1.2 report", "a.md", "v1.2 report.md"},
		{"name", "noext", "name"},
		{"name", "a.bad ext", "name"},
	}
	for _, c := range cases {
		if got := EnsureExtension(c[0], c[1]); got != c[2] {
			t.Errorf("EnsureExtension(%q,%q)=%q want %q", c[0], c[1], got, c[2])
		}
	}
}
