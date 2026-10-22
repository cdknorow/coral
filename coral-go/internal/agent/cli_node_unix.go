//go:build !windows

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/subgentic/coral/internal/secfile"
)

// pathWriters is who besides the user and root could change what a program started at a path would run (a variable so a test can answer for a path it can't build).
var pathWriters = secfile.PathWriters

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
		return fmt.Errorf("it could not be checked: %v", err)
	}
	if len(who) > 0 {
		return fmt.Errorf("it can be written by others: %s", strings.Join(who, "; "))
	}
	return nil
}
