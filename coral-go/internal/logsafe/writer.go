package logsafe

import (
	"github.com/subgentic/coral/internal/secrets"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Writer wraps a log sink so a line never carries the person's profile folder, name or computer name (#6053): the home folder is written as "~", the folder under /Users, /home and
// C:\Users (8.3 short form too) as "<user>", the computer name as "<host>". It rewrites each Write on its own (a log line is one Write), holds nothing back and never changes how many
// bytes the caller wrote, so a terminal or file that is slow stays as it was.
//
// Writer also runs each line through the secrets redactor (internal/secrets), so a token-shaped string in an error or a child's stderr never reaches the file either (#6069); a line that cannot be
// checked (the check failed or ran out of its budget) is withheld, never written as it is. PathWriter is the same without the secrets pass, for a sink that already has one (coral.log's logOutput).
func Writer(w io.Writer) io.Writer {
	return &redactingWriter{w: w, r: newRedactor(homeFolders(), hostName()), secrets: true}
}

// PathWriter hides the home folder, user names and computer name only: for a sink whose own writer already redacts secrets.
func PathWriter(w io.Writer) io.Writer {
	return &redactingWriter{w: w, r: newRedactor(homeFolders(), hostName())}
}

// secretsText redacts secrets in one log line; a variable so a test can stand in for a check that fails.
var secretsText = func(b []byte) ([]byte, error) { return secrets.LogRedactor().Text(b) }

const withheld = "[log line withheld: it could not be checked for secrets]\n"

type redactingWriter struct {
	w       io.Writer
	r       *redactor
	secrets bool
}

func (rw *redactingWriter) Write(p []byte) (int, error) {
	out := rw.r.redact(p)
	if rw.secrets {
		if red, err := secretsText(out); err != nil {
			out = []byte(withheld)
		} else {
			out = red
		}
	}
	if _, err := rw.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// profileFolder matches the folder a profile lives in: /Users/<name>, /home/<name>, C:\Users\<name> (either slash), the name up to the next separator or a character no folder name has.
//
// It matches only where a path starts: at the start of the text or after a space, a quote, "=", "(", "[", "," or ";" (so "/api/home/x" is a URL, not a profile), and it leaves the shared accounts alone
// (/home/linuxbrew, /Users/Shared, C:\Users\Public and Default are not a person).
var profileFolder = regexp.MustCompile(`(?i)(^|[\s"'=(\[,;])((?:[a-z]:)?[\\/](?:users|home)[\\/])([^\\/\s"'<>:|*?]+)`)

var sharedProfiles = map[string]bool{"linuxbrew": true, "shared": true, "public": true, "default": true, "all users": true, "default user": true, "<user>": true, "~": true}

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
	// The computer's name is hidden whole and as its short name (the part before the first dot, "build-mac" for "build-mac.local"). A name like "mac" or "localhost" is a word the log uses on its
	// own: hiding it would damage lines that say nothing about the person.
	var names []string
	for _, n := range []string{host, strings.SplitN(host, ".", 2)[0]} {
		if len(n) >= 4 && !strings.EqualFold(n, "localhost") && !seen["host:"+strings.ToLower(n)] {
			seen["host:"+strings.ToLower(n)] = true
			names = append(names, regexp.QuoteMeta(n))
		}
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	if len(names) > 0 {
		r.host = regexp.MustCompile(`(?i)(^|[^a-z0-9_-])(?:` + strings.Join(names, "|") + `)([^a-z0-9_-]|$)`)
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
		if sharedProfiles[strings.ToLower(sub[3])] {
			return m
		}
		return sub[1] + sub[2] + "<user>"
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
