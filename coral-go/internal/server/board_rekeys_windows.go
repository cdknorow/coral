//go:build windows

package server

// noFollowFlag: Windows has no O_NOFOLLOW; rekeyStateFiles skips anything Lstat doesn't
// report as a regular file.
const noFollowFlag = 0
