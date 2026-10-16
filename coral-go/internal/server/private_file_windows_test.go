//go:build windows

package server

import (
	"testing"

	"github.com/cdknorow/coral/internal/secfile/secfiletest"
)

// requirePrivateFile: on Windows the mode says nothing (a writable file is 0666); a secret file
// must have its own access list, only this user and SYSTEM (#3914).
func requirePrivateFile(t *testing.T, path string) {
	t.Helper()
	secfiletest.RequirePrivate(t, path)
}
