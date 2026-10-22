package logsafe

import (
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Writer wraps a log sink so a line never carries the person's profile folder, name or computer name (#6053): the home folder is written as "~", the folder under /Users, /home and
// C:\Users (8.3 short form too) as "<user>", the computer name as "<host>". It rewrites each Write on its own (a log line is one Write), holds nothing back and never changes how many
// bytes the caller wrote, so a terminal or file that is slow stays as it was.
func Writer(w io.Writer) io.Writer {
	return &redactingWriter{w: w, r: newRedactor(homeFolders(), hostName())}
}

type redactingWriter struct {
	w io.Writer
	r *redactor
}

func (rw *redactingWriter) Write(p []byte) (int, error) {
	out := rw.r.redact(p)
	if _, err := rw.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// profileFolder matches the folder a profile lives in: /Users/<name>, /home/<name>, C:\Users\<name> (either slash), the name up to the next separator or a character no folder name has.
var profileFolder = regexp.MustCompile(`(?i)((?:[a-z]:)?[\\/](?:users|home)[\\/])([^\\/\s"'<>:|*?]+)`)

type redactor struct {
	homes []string       // longest first, each as the system writes it and with the other slash
	host  *regexp.Regexp // nil when the computer name is too short to hide without damaging the line
}

func newRedactor(homes []string, host string) *redactor {
	seen := map[string]bool{}
	var hs []string
	add := func(h string) {
		h = strings.TrimRight(h, `\/`)
		if len(h) < 2 || h == "/" || seen[strings.ToLower(h)] {
			return
		}
		seen[strings.ToLower(h)] = true
		hs = append(hs, h)
	}
	for _, h := range homes {
		add(h)
		add(strings.ReplaceAll(h, `\`, `/`))
		add(strings.ReplaceAll(h, `/`, `\`))
	}
	sort.Slice(hs, func(i, j int) bool { return len(hs[i]) > len(hs[j]) })
	r := &redactor{homes: hs}
	// A name like "mac" or "localhost" is a word the log uses on its own: hiding it would damage lines that say nothing about the person.
	if len(host) >= 4 && !strings.EqualFold(host, "localhost") {
		r.host = regexp.MustCompile(`(?i)(^|[^a-z0-9_-])` + regexp.QuoteMeta(host) + `([^a-z0-9_-]|$)`)
	}
	return r
}

func (r *redactor) redact(p []byte) []byte {
	s := string(p)
	for _, h := range r.homes {
		s = replaceFolder(s, h, "~")
	}
	s = profileFolder.ReplaceAllStringFunc(s, func(m string) string {
		sub := profileFolder.FindStringSubmatch(m)
		if sub[2] == "~" || sub[2] == "<user>" {
			return m
		}
		return sub[1] + "<user>"
	})
	if r.host != nil {
		s = r.host.ReplaceAllString(s, "${1}<host>${2}")
	}
	return []byte(s)
}

// replaceFolder writes folder as with wherever it starts a path (at the start, or after a character a path cannot continue), case-blind on Windows-style names, and only as a whole folder
// ("/home/ann" is not "/home/annabel").
func replaceFolder(s, folder, with string) string {
	var b strings.Builder
	ls, lf := strings.ToLower(s), strings.ToLower(folder)
	i := 0
	for {
		j := strings.Index(ls[i:], lf)
		if j < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		j += i
		end := j + len(folder)
		okAfter := end == len(s) || s[end] == '/' || s[end] == '\\' || !isNameByte(s[end])
		okBefore := j == 0 || !isNameByte(s[j-1])
		if okAfter && okBefore {
			b.WriteString(s[i:j])
			b.WriteString(with)
		} else {
			b.WriteString(s[i:end])
		}
		i = end
	}
}

func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.'
}

func hostName() string {
	h, _ := os.Hostname()
	return h
}

// Dir is a folder as a log names it: the last folder's name only, whatever separator the path uses ("/Users/x/work/app" and `C:\Users\x\work\app` are both "app"), never the way to it (#6053).
func Dir(p string) string {
	p = strings.TrimRight(p, `\/`)
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		p = p[i+1:]
	}
	if p == "" {
		return "."
	}
	return p
}

// Path is a path as a log shows it where more than the last name helps (an executable, the tmux that was found): the home folder as "~" and the user's name as "<user>" wherever it is a folder's name.
func Path(p string) string {
	if p == "" {
		return p
	}
	for _, h := range homeFolders() {
		if r := HomeRelative(p, h); r != p {
			p = r
			break
		}
	}
	return HideUserName(p, userNames())
}
