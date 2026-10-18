//go:build !windows

package ptymanager

// enterKey is the byte sequence that submits a command line in the terminal.
// Bare carriage return is required by agent TUIs in raw mode to submit input;
// cooked shells translate \r to \n via ICRNL.
const enterKey = "\r"

// EnterKey is the exported form of enterKey for other packages.
const EnterKey = enterKey
