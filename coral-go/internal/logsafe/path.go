// Package logsafe writes facts about the environment into logs without a person's profile folder in them (#6018, #6004 W9): a Windows or macOS PATH names the user's home folder in several entries,
// and a log is what a bug report carries. The PATH is logged once, when the server starts, with the home folder written as "~"; everywhere else only the entry count is.
package logsafe

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// Entries are the PATH's folders, in order, empty entries dropped.
func Entries() []string {
	var out []string
	for _, e := range filepath.SplitList(os.Getenv("PATH")) {
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

// Count is the number of folders on the PATH.
func Count() int { return len(Entries()) }

// HomeRelative writes p with the home folder at its start as "~" (a folder under it keeps its path after it). On Windows the comparison ignores case, as the file system does.
func HomeRelative(p, home string) string {
	if home == "" || p == "" {
		return p
	}
	eq := func(a, b string) bool { return a == b }
	if filepath.Separator == '\\' {
		eq = strings.EqualFold
	}
	if len(p) < len(home) || !eq(p[:len(home)], home) {
		return p
	}
	rest := p[len(home):]
	if rest == "" || rest[0] == '/' || rest[0] == '\\' {
		return "~" + rest
	}
	return p
}

// HomeFolders are the folders to hide: the user's home folder as the system names it and as the environment does (they can differ in case or form).
func homeFolders() []string {
	var h []string
	if d, err := os.UserHomeDir(); err == nil && d != "" {
		h = append(h, d)
	}
	for _, k := range []string{"USERPROFILE", "HOME"} {
		if v := os.Getenv(k); v != "" {
			h = append(h, v)
		}
	}
	return h
}

// userNames are the names this person is known by: the account name as the system and the environment give it (a Windows one can be "DOMAIN\\name"), and the home folder's own name.
func userNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if i := strings.LastIndexAny(n, `\/`); i >= 0 {
			n = n[i+1:]
		}
		n = strings.TrimSpace(n)
		if n != "" && !seen[strings.ToLower(n)] {
			seen[strings.ToLower(n)] = true
			out = append(out, n)
		}
	}
	if u, err := user.Current(); err == nil {
		add(u.Username)
	}
	for _, k := range []string{"USERNAME", "USER", "LOGNAME"} {
		add(os.Getenv(k))
	}
	for _, h := range homeFolders() {
		add(filepath.Base(h))
	}
	return out
}

// isShortFormOf reports whether comp is the DOS 8.3 short form of name: its first six letters (spaces and dots left out, upper case), "~" and a number, as "RUNNER~1" is for "runneradmin". A
// path can name the same folder by either, so the user's name is hidden in both.
func isShortFormOf(comp, name string) bool {
	i := strings.LastIndexByte(comp, '~')
	if i <= 0 || i == len(comp)-1 {
		return false
	}
	for _, c := range comp[i+1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	base := strings.ToUpper(strings.NewReplacer(" ", "", ".", "").Replace(name))
	if len(base) > 6 {
		base = base[:6]
	}
	return base != "" && strings.EqualFold(comp[:i], base)
}

// HideUserName writes any folder of p that is the user's name, or its 8.3 short form, as "<user>": the home folder is only one of the places a path can carry it (a PATH entry can name the
// profile by its short form, and an installer can put a folder named for the user anywhere).
func HideUserName(p string, names []string) string {
	if p == "" || len(names) == 0 {
		return p
	}
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return p
	}
	out := p
	for _, part := range parts {
		for _, n := range names {
			if strings.EqualFold(part, n) || isShortFormOf(part, n) {
				out = replaceComponent(out, part, "<user>")
				break
			}
		}
	}
	return out
}

// replaceComponent replaces whole folder names equal to comp (case-blind) in p.
func replaceComponent(p, comp, with string) string {
	var b strings.Builder
	start := 0
	flush := func(end int) {
		seg := p[start:end]
		if strings.EqualFold(seg, comp) {
			b.WriteString(with)
		} else {
			b.WriteString(seg)
		}
	}
	for i := 0; i < len(p); i++ {
		if p[i] == '/' || p[i] == '\\' {
			flush(i)
			b.WriteByte(p[i])
			start = i + 1
		}
	}
	flush(len(p))
	return b.String()
}

// PathSummary is the line's PATH fields: the entry count and the folders with the home folder as "~" and the user's name (and its 8.3 short form) as "<user>" wherever else it is a folder's
// name. Used once, at server start.
func PathSummary() string {
	es := Entries()
	shown := make([]string, len(es))
	homes := homeFolders()
	names := userNames()
	for i, e := range es {
		shown[i] = e
		for _, h := range homes {
			if r := HomeRelative(e, h); r != e {
				shown[i] = r
				break
			}
		}
		shown[i] = HideUserName(shown[i], names)
	}
	return "path_entries=" + itoa(len(es)) + " path=" + strings.Join(shown, string(filepath.ListSeparator))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}
