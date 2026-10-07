//go:build windows

package ptymanager

// enterKey is the byte sequence that submits a command line in the terminal.
// Windows ConPTY requires a carriage return to submit input.
const enterKey = "\r\n"
