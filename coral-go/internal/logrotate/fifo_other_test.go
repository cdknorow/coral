//go:build !windows

package logrotate

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// #3952: a FIFO planted at coral.log's path makes Open fail at once, never hang the server.
func TestOpenRefusesAPlantedFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coral.log")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		w, err := Open(path, 100, 2, 0)
		if err == nil {
			w.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO was opened as the log")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Open hung on a FIFO")
	}
}
