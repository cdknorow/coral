//go:build !windows

package ptymanager

// enterKey is the byte sequence that submits a command line in the terminal.
const enterKey = "\n"

// EnterKey is the exported form of enterKey for other packages.
const EnterKey = enterKey
