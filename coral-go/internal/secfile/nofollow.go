package secfile

import (
	"errors"
	"os"
)

// ErrLink: OpenNoFollow found a symbolic link, junction or other reparse point (on Windows),
// or a folder, at the path, or, for a write, a file with a second name (a hard link) or one
// that isn't a regular file (a FIFO or a device, on Unix).
//
// OpenNoFollow protects the LAST name of the path only. A folder further up that an agent can
// write to, when Coral writes into it by a name that varies (an agent's id, a task, a file the
// request names), can itself be swapped for a link between the check and the open: open
// those through safefs.Root, which holds the folder open and resolves every name under it
// (#3952, Security's #3920 note).
var ErrLink = errors.New("the path is a link, a junction or a folder, not a plain file")

// WriteFileNoFollow is os.WriteFile through OpenNoFollow: it refuses a link, junction or folder
// at path, and a file with a second name, instead of writing through it.
func WriteFileNoFollow(path string, data []byte, perm os.FileMode) error {
	f, err := OpenNoFollow(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
