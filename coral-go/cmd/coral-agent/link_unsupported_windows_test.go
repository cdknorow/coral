//go:build windows

package main

import "syscall"

// the errors a Windows file system without hard links (FAT, exFAT, some shares) answers a link with
var unsupportedLinkErrnos = []syscall.Errno{1, 17, 50}
