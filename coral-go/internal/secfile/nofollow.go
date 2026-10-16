package secfile

import (
	"errors"
	"os"
)

// ErrLink: OpenNoFollow found a symbolic link, junction or other reparse point (on Windows),
// or a folder, at the path, or, for a write, a file with a second name (a hard link).
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
