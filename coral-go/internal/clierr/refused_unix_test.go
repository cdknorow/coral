//go:build !windows

package clierr

import "syscall"

const refusedErrnoForTest = syscall.ECONNREFUSED
