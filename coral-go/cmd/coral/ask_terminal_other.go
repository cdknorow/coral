//go:build !unix

package main

import "os"

// inForeground: Windows has no background jobs on a console; the stdout check in
// canAskAtTerminal covers a console nobody watches.
func inForeground(*os.File) bool { return true }
