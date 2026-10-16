//go:build !windows

package secfile

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// openWithin runs OpenNoFollow and fails the test if it doesn't return within d (a hang).
func openWithin(t *testing.T, d time.Duration, path string, flag int) (*os.File, error) {
	t.Helper()
	type res struct {
		f   *os.File
		err error
	}
	ch := make(chan res, 1)
	go func() {
		f, err := OpenNoFollow(path, flag, 0o600)
		ch <- res{f, err}
	}()
	select {
	case r := <-ch:
		return r.f, r.err
	case <-time.After(d):
		t.Fatalf("OpenNoFollow(%s) hung", path)
		return nil, nil
	}
}

// #3952 (Security's #3920 note): a FIFO planted at a log path never hangs a write open, and is
// refused whether or not something reads it; a plain file still opens, blocking as before.
func TestOpenNoFollowRefusesAFIFOForAWrite(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "coral.log")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	for _, flag := range []int{os.O_WRONLY | os.O_CREATE | os.O_APPEND, os.O_WRONLY | os.O_CREATE | os.O_TRUNC, os.O_RDWR} {
		f, err := openWithin(t, 3*time.Second, fifo, flag)
		if err == nil {
			f.Close()
			t.Fatalf("flag %#x: a FIFO was opened for a write", flag)
		}
		if !errors.Is(err, ErrLink) {
			t.Errorf("flag %#x: want ErrLink, got %v", flag, err)
		}
	}
	// With a reader waiting, the open would succeed: still refused, it isn't a regular file.
	r, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	f, err := openWithin(t, 3*time.Second, fifo, os.O_WRONLY|os.O_APPEND)
	if err == nil {
		f.Close()
		t.Fatal("a FIFO with a reader was opened for a write")
	}
	if !errors.Is(err, ErrLink) {
		t.Errorf("want ErrLink, got %v", err)
	}

	plain := filepath.Join(dir, "plain.log")
	f, err = openWithin(t, 3*time.Second, plain, os.O_WRONLY|os.O_CREATE|os.O_APPEND)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fl, err := fcntlFlags(f)
	if err != nil {
		t.Fatal(err)
	}
	if fl&syscall.O_NONBLOCK != 0 {
		t.Error("a plain file was left non-blocking")
	}
	if _, err := f.WriteString("ok\n"); err != nil {
		t.Fatal(err)
	}
	f2, err := openWithin(t, 3*time.Second, plain, os.O_WRONLY|os.O_APPEND|syscall.O_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	if fl, _ := fcntlFlags(f2); fl&syscall.O_NONBLOCK == 0 {
		t.Error("the caller's O_NONBLOCK was dropped")
	}
}
