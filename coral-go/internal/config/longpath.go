package config

import "strings"

// ExtendedPath is a data folder given in the extended-length form with forward slashes (//?/D:/coral, as a shell or a script on Windows may hand it) written the one way Windows accepts: the
// \\?\ prefix with backslashes all through (#6717, Windows finding 5: SQLite could not open a database under //?/D:/..., so a long data folder stopped the server from starting). Any other path,
// including a plain long one, is returned as it is.
func ExtendedPath(dir string) string {
	rest, ok := strings.CutPrefix(dir, "//?/")
	if !ok {
		if rest, ok = strings.CutPrefix(dir, `\\?\`); !ok {
			return dir
		}
	}
	if len(rest) < 2 || rest[1] != ':' || !(rest[0] >= 'A' && rest[0] <= 'Z' || rest[0] >= 'a' && rest[0] <= 'z') {
		return dir // the UNC form and device paths are not rewritten
	}
	return `\\?\` + strings.ReplaceAll(rest, "/", `\`)
}
