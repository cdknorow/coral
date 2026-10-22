// Package logsafe writes facts about the environment into logs without a person's profile folder in them (#6018, #6004 W9): a Windows or macOS PATH names the user's home folder in several entries,
// and a log is what a bug report carries. The PATH is logged once, when the server starts, with the home folder written as "~"; everywhere else only the entry count is.
package logsafe

import (
	"os"
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

// PathSummary is the line's PATH fields: the entry count and the folders with the home folder as "~". Used once, at server start.
func PathSummary() string {
	es := Entries()
	shown := make([]string, len(es))
	homes := homeFolders()
	for i, e := range es {
		shown[i] = e
		for _, h := range homes {
			if r := HomeRelative(e, h); r != e {
				shown[i] = r
				break
			}
		}
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
