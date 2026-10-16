package secfile

import "errors"

// ErrLink: OpenNoFollow found a symbolic link, junction or other reparse point (on Windows),
// or a folder, at the path, or, for a write, a file with a second name (a hard link).
var ErrLink = errors.New("the path is a link, a junction or a folder, not a plain file")
