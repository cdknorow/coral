//go:build linux

// unhealthy-coral is a stand-in for coral in a rehearsal package (#5401): it answers --version as the real one does and ends at once for anything else, so
// the install that holds it passes every check the updater makes before the swap and does not stay up after it. It is never released.
package main

import (
	"fmt"
	"os"
)

var version = "0.0.0"

func main() {
	for _, a := range os.Args[1:] {
		if a == "--version" {
			fmt.Println("coral " + version)
			return
		}
	}
	os.Exit(1)
}
