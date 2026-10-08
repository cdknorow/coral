//go:build windows

package ptymanager

// enterKey is the byte sequence that submits a command line in the terminal.
// Windows ConPTY requires a bare carriage return; "\n" or "\r\n" is read by
// agent TUIs as a newline in the input box rather than a submit.
const enterKey = "\r"

// EnterKey is the exported form of enterKey for other packages.
const EnterKey = enterKey
