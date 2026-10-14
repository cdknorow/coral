package cloud

import "golang.org/x/sys/unix"

// The well-known bootstrap location is a Linux convention (/run on a tmpfs).
const wellKnownSupported = true

func fdOnTmpfs(fd int) bool {
	var st unix.Statfs_t
	return unix.Fstatfs(fd, &st) == nil && st.Type == unix.TMPFS_MAGIC
}
