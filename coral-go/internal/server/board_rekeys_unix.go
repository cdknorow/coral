//go:build !windows

package server

import "syscall"

const noFollowFlag = syscall.O_NOFOLLOW
