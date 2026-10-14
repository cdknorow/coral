package cloud

import (
	"bytes"

	"golang.org/x/sys/unix"
)

// macOS has no /run convention: no auto-discovery, --bootstrap-file only.
const wellKnownSupported = false

func fdOnTmpfs(fd int) bool {
	var st unix.Statfs_t
	if unix.Fstatfs(fd, &st) != nil {
		return false
	}
	name := st.Fstypename[:]
	if i := bytes.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	return string(name) == "tmpfs"
}
