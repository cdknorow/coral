//go:build !windows

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/subgentic/coral/internal/secfile"
)

// pathWriters is who besides the user and root could change what a program started at a path would run (a variable so a test can answer for a path it can't build). A group write bit
// passes on the user's own folders when the group is the user's alone (groupIsTheUsersAlone: on a Mac, Homebrew makes its folders group-writable for admin, which on a one-person Mac
// is just the user).
var pathWriters = func(path string) ([]secfile.Writer, error) {
	return secfile.PathWritersTrustingGroup(path, func(gid uint32) (bool, error) { return groupAlone(gid) })
}

// groupAlone says whether a group is the user's alone (a variable so a test can answer for a group it can't build).
var groupAlone = groupIsTheUsersAlone

// trustedNodeDir reports whether the node in dir may be run for the user: it runs as the user, so nobody else may be able to replace it. secfile.PathWriters walks the path PATH
// will take (dir/node) hop by hop, every folder, link and file on the way and each link's target, for another owner, a group or other write bit, and an access list that grants a
// write; any doubt or error refuses: a node that cannot be shown safe is not used.
func trustedNodeDir(dir string) error {
	node := filepath.Join(dir, "node")
	if st, err := os.Stat(node); err != nil || !st.Mode().IsRegular() || st.Mode()&0o111 == 0 {
		return fmt.Errorf("it is not an executable file")
	}
	who, err := pathWriters(node)
	if err != nil {
		return &nodeRefusal{Reason: fmt.Sprintf("it could not be checked: %v", err), Fix: "Start Coral from a shell whose PATH has node's folder, and Coral uses the node it finds there."}
	}
	if len(who) > 0 {
		text := make([]string, len(who))
		for i, w := range who {
			text[i] = w.Text
		}
		return &nodeRefusal{Reason: "it can be written by others: " + strings.Join(text, "; "), Fix: nodeFix(who)}
	}
	return nil
}

// nodeFix says what to do about the writers found: when every one is only a group's write bit, the chmod that removes it from each folder (the message names the group); otherwise
// the folders have to be fixed by whoever owns them. Either way the other way out is a node already on the PATH Coral starts with.
func nodeFix(who []secfile.Writer) string {
	seen := map[string]bool{}
	var paths []string
	for _, w := range who {
		if !w.Group {
			return "Ask whoever owns those folders to make them writable by you alone, or start Coral from a shell whose PATH has node's folder (Coral then uses the node it finds there)."
		}
		if !seen[w.Path] {
			seen[w.Path] = true
			paths = append(paths, singleQuote(w.Path))
		}
	}
	return "To use it, take the group's write permission off: chmod g-w " + strings.Join(paths, " ") + " or start Coral from a shell whose PATH has node's folder (Coral then uses the node it finds there)."
}
