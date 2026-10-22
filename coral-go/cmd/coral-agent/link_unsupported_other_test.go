//go:build !windows

package main

import "syscall"

// the errors a file system without hard links (vfat, exfat, some network shares) answers a link with
var unsupportedLinkErrnos = []syscall.Errno{syscall.EPERM, syscall.EXDEV, syscall.EOPNOTSUPP}
