package config

import "strings"

// PlainDrivePath is a data folder given with the extended-length prefix (//?/D:/coral, \\?\D:\coral: Git Bash's cygpath -m and MSYS's environment conversion write the first form for a long path)
// written as the plain drive path (D:\coral). SQLite cannot open a database under either prefixed form (unable to open database file (14), Windows proof #6722), and the plain one works. Any other
// path, a plain long one, a UNC or a device path, is returned as it is (#6717 item 5, redone).
func PlainDrivePath(dir string) string {
	rest, ok := strings.CutPrefix(dir, "//?/")
	if !ok {
		if rest, ok = strings.CutPrefix(dir, `\\?\`); !ok {
			return dir
		}
	}
	if len(rest) < 2 || rest[1] != ':' || !(rest[0] >= 'A' && rest[0] <= 'Z' || rest[0] >= 'a' && rest[0] <= 'z') {
		return dir // the UNC form and device paths are not rewritten
	}
	return strings.ReplaceAll(rest, "/", `\`)
}
