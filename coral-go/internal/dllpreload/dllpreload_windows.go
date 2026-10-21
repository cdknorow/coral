//go:build windows

package dllpreload

import (
	"log"

	"golang.org/x/sys/windows"
)

func init() {
	for _, err := range load() {
		log.Printf("[security] %v", err)
	}
}

// load loads each of Names from the system folder and returns what could not be loaded: that DLL is then looked up by the webview as before.
func load() (failed []error) {
	for _, name := range Names {
		if err := windows.NewLazySystemDLL(name).Load(); err != nil {
			failed = append(failed, err)
		}
	}
	return failed
}
