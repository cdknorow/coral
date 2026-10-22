//go:build darwin

package secfile

import (
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// aceRe is one entry of "ls -lde": " 0: group:everyone deny delete", " 1: user:bob allow list,add_file,delete_child,file_inherit".
var aceRe = regexp.MustCompile(`^\s*\d+: (?:(user|group):)?(.+) (allow|deny) ([a-z_,]+)\s*$`)

// writeRights are the access-list rights that let someone replace or change what is there (a folder's add_file, add_subdirectory and delete_child, a file's write, append and delete,
// and writesecurity and chown, which let them grant themselves the rest).
var writeRights = map[string]bool{"write": true, "append": true, "delete": true, "add_file": true, "add_subdirectory": true, "delete_child": true, "writesecurity": true, "chown": true}

// pathACLWriters reads path's access list with ls -lde (macOS has no other way without cgo) and returns the principals an ALLOW entry lets write, other than the user and root;
// a deny entry grants nothing and is ignored; an entry that only applies to what is inherited (only_inherit) is not about path itself. A line it can't read is an error: a path
// whose list is not understood is not shown safe.
func pathACLWriters(path string, uid int, name string) ([]string, error) {
	out, err := exec.Command("/bin/ls", "-lde", "--", path).Output()
	if err != nil {
		return nil, err
	}
	var who []string
	lines := bytes.Split(bytes.TrimRight(out, "\n"), []byte("\n"))
	if len(lines) == 0 || len(lines[0]) == 0 {
		return nil, fmt.Errorf("ls printed nothing")
	}
	for _, line := range lines[1:] { // the first line is the file's own
		m := aceRe.FindSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("an access list entry that is not understood: %q", line)
		}
		kind, principal, verdict, rights := string(m[1]), string(m[2]), string(m[3]), strings.Split(string(m[4]), ",")
		if verdict != "allow" {
			continue
		}
		writes, onlyInherit := false, false
		for _, r := range rights {
			if writeRights[r] {
				writes = true
			}
			if r == "only_inherit" {
				onlyInherit = true
			}
		}
		if !writes || onlyInherit {
			continue
		}
		if kind == "user" && (principal == name || principal == "root") {
			continue
		}
		who = append(who, strings.TrimPrefix(kind+":"+principal, ":"))
	}
	return who, nil
}
