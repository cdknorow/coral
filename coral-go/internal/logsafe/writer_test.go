package logsafe

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRedactorLines(t *testing.T) {
	r := newRedactor([]string{"/Users/ann", `C:\Users\Ann Lee`}, "Annes-MacBook-Pro")
	cases := []struct{ in, want string }{
		{"opening /Users/ann/.coral/sessions.db", "opening ~/.coral/sessions.db"},
		{"home is /Users/ann", "home is ~"},
		{"/Users/ann: permission denied", "~: permission denied"},
		{"not ann: /Users/annabel/x", "not ann: /Users/<user>/x"}, // another user's folder: still a name
		{"/Users/annabel2 and /Users/ann", "/Users/<user> and ~"}, // a longer name is not the home folder
		{"read /home/bob/.config/x failed", "read /home/<user>/.config/x failed"},
		{`read C:\Users\Bob\AppData\x`, `read C:\Users\<user>\AppData\x`},
		{"read C:/Users/Bob/AppData/x", "read C:/Users/<user>/AppData/x"},
		{`read c:\users\RUNNER~1\x`, `read c:\users\<user>\x`}, // the 8.3 form of a name
		{`in C:\Users\Ann Lee\Documents`, `in ~\Documents`},    // a home folder with a space
		{"host Annes-MacBook-Pro is up", "host <host> is up"},
		{"ANNES-MACBOOK-PRO", "<host>"},
		{"Annes-MacBook-Pro-2 stays", "Annes-MacBook-Pro-2 stays"}, // only the whole name
		{"no paths here", "no paths here"},
		{"", ""},
		{"/usr/local/bin/tmux and /opt/homebrew/bin", "/usr/local/bin/tmux and /opt/homebrew/bin"},
		{"~/.coral already", "~/.coral already"},
		{"/Users/<user>/x", "/Users/<user>/x"},
	}
	for _, c := range cases {
		if got := string(r.redact([]byte(c.in))); got != c.want {
			t.Errorf("redact(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRedactorShortHostIsLeftAlone(t *testing.T) {
	for _, host := range []string{"mac", "localhost", "pc", ""} {
		r := newRedactor(nil, host)
		if got := string(r.redact([]byte("the mac and localhost and pc"))); got != "the mac and localhost and pc" {
			t.Errorf("host %q damaged the line: %q", host, got)
		}
	}
}

type recorder struct {
	writes []string
	err    error
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	r.writes = append(r.writes, string(p))
	return len(p), nil
}

// Per write, nothing held back: each Write reaches the sink at once, redacted, and reports the bytes the caller wrote (not the redacted length).
func TestWriterPassesEachWriteThroughAtOnce(t *testing.T) {
	rec := &recorder{}
	w := &redactingWriter{w: rec, r: newRedactor([]string{"/Users/ann"}, "")}
	for _, line := range []string{"a /Users/ann/x\n", "b\n", "/Users/ann"} {
		n, err := w.Write([]byte(line))
		if err != nil || n != len(line) {
			t.Fatalf("Write(%q) = %d, %v; want %d, nil", line, n, err, len(line))
		}
	}
	if got := strings.Join(rec.writes, "|"); got != "a ~/x\n|b\n|~" {
		t.Fatalf("sink saw %q", got)
	}
}

func TestWriterReportsASinkError(t *testing.T) {
	boom := errors.New("disk full")
	w := &redactingWriter{w: &recorder{err: boom}, r: newRedactor(nil, "")}
	if n, err := w.Write([]byte("x")); !errors.Is(err, boom) || n != 0 {
		t.Fatalf("Write = %d, %v", n, err)
	}
}

func TestWriterOnTheRealEnvironment(t *testing.T) {
	t.Setenv("HOME", "/Users/zed")
	t.Setenv("USERPROFILE", "")
	var buf bytes.Buffer
	w := Writer(&buf)
	_, _ = w.Write([]byte("x /Users/zed/y /home/other/z\n"))
	if got := buf.String(); strings.Contains(got, "zed") && !strings.Contains(got, "~") && !strings.Contains(got, "<user>") {
		t.Fatalf("not redacted: %q", got)
	}
	if strings.Contains(buf.String(), "other") {
		t.Fatalf("another user's folder name survived: %q", buf.String())
	}
}

func TestDir(t *testing.T) {
	for in, want := range map[string]string{
		"/Users/ann/work/app":    "app",
		`C:\Users\ann\work\app`:  "app",
		`C:\Users\ann\work\app\`: "app",
		"/Users/ann/work/app/":   "app",
		"app":                    "app",
		"":                       ".",
		"/":                      ".",
		`\\server\share\proj`:    "proj",
	} {
		if got := Dir(in); got != want {
			t.Errorf("Dir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPath(t *testing.T) {
	t.Setenv("HOME", "/Users/zed")
	t.Setenv("USERPROFILE", "")
	if got := Path("/Users/zed/bin/coral"); got != "~/bin/coral" {
		t.Errorf("Path = %q", got)
	}
	if got := Path("/usr/local/bin/tmux"); got != "/usr/local/bin/tmux" {
		t.Errorf("Path = %q", got)
	}
}

// #6069 (Security's follow-ups to #6053).

func TestTheShortComputerNameIsHiddenToo(t *testing.T) {
	r := newRedactor(nil, "build-mac.local")
	for in, want := range map[string]string{
		"host build-mac.local is up":    "host <host> is up",
		"host build-mac is up":          "host <host> is up", // the part before the first dot
		"BUILD-MAC":                     "<host>",
		"build-mac2 stays":              "build-mac2 stays",
		"the mac and localhost stay":    "the mac and localhost stay",
		"build-mac.local:8420 and more": "<host>:8420 and more",
	} {
		if got := string(r.redact([]byte(in))); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
	// a short name before the dot is too short to hide alone ("mac.local"): the whole name still goes
	r = newRedactor(nil, "mac.local")
	if got := string(r.redact([]byte("mac.local and the mac"))); got != "<host> and the mac" {
		t.Errorf("got %q", got)
	}
}

func TestProfileFoldersAreOnlyWhereAPathStarts(t *testing.T) {
	r := newRedactor(nil, "")
	for in, want := range map[string]string{
		"opened /Users/bob/x":                      "opened /Users/<user>/x",
		`path="/home/bob/x"`:                       `path="/home/<user>/x"`,
		"path=/home/bob/x failed":                  "path=/home/<user>/x failed",
		"(/Users/bob/x)":                           "(/Users/<user>/x)",
		"/home/bob/x":                              "/home/<user>/x",
		"file:///Users/other/x.txt":                "file:///Users/<user>/x.txt",
		"GOPATH=/usr/lib:/Users/other/go":          "GOPATH=/usr/lib:/Users/<user>/go",
		"PATH=/usr/bin:/home/bob/bin:/opt/x":       "PATH=/usr/bin:/home/<user>/bin:/opt/x",
		"a|/home/bob/x":                            "a|/home/<user>/x",
		"out>/Users/bob/log":                       "out>/Users/<user>/log",
		"http://h/api/Users/bob/x":                 "http://h/api/Users/bob/x", // still a URL
		"GET /api/home/bob/x":                      "GET /api/home/bob/x",      // a URL, not a profile
		"GET /api/users/bob":                       "GET /api/users/bob",
		"/home/linuxbrew/.linuxbrew/bin/tmux":      "/home/linuxbrew/.linuxbrew/bin/tmux", // a shared account
		"/Users/Shared/x and C:\\Users\\Public\\y": "/Users/Shared/x and C:\\Users\\Public\\y",
		"C:\\Users\\Default\\z":                    "C:\\Users\\Default\\z",
		"see /home/bob/a and /home/linuxbrew/b":    "see /home/<user>/a and /home/linuxbrew/b",
		`opened C:\Users\Bob\AppData`:              `opened C:\Users\<user>\AppData`,
	} {
		if got := string(r.redact([]byte(in))); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriterRedactsSecretsAndFailsClosed(t *testing.T) {
	var rec recorder
	w := Writer(&rec)
	line := "request failed: Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789ABCDEF for /home/bob/x\n"
	if n, err := w.Write([]byte(line)); err != nil || n != len(line) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	got := rec.writes[0]
	for _, leak := range []string{"abcdefghijklmnopqrstuvwxyz0123456789ABCDEF", "/home/bob"} {
		if strings.Contains(got, leak) {
			t.Errorf("the line carries %q: %q", leak, got)
		}
	}
	if !strings.Contains(got, "request failed") {
		t.Errorf("the rest of the line is lost: %q", got)
	}

	// a check that fails withholds the line: nothing unchecked is written
	prev := secretsText
	t.Cleanup(func() { secretsText = prev })
	secretsText = func([]byte) ([]byte, error) { return nil, errors.New("out of budget") }
	rec.writes = nil
	if n, err := w.Write([]byte("password=hunter2hunter2\n")); err != nil || n != len("password=hunter2hunter2\n") {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if len(rec.writes) != 1 || strings.Contains(rec.writes[0], "hunter2") || !strings.Contains(rec.writes[0], "withheld") {
		t.Errorf("an unchecked line was written: %q", rec.writes)
	}

	// PathWriter has no secrets pass: its own sink has one
	rec.writes = nil
	secretsText = func([]byte) ([]byte, error) { t.Error("PathWriter ran the secrets pass"); return nil, nil }
	_, _ = PathWriter(&rec).Write([]byte("/home/bob/x\n"))
	if rec.writes[0] != "/home/<user>/x\n" {
		t.Errorf("PathWriter: %q", rec.writes[0])
	}
}
