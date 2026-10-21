// Package boardtest gives tests a ready board database without parsing the whole schema every time: the schema is made once per test process and each test starts from a copy of
// the result (the same idea as storetest for the sessions database). Only tests import this package.
package boardtest

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/subgentic/coral/internal/board"
)

var (
	once  sync.Once
	image []byte
	err   error
)

// templateImage is the bytes of a freshly migrated, closed board database. Nothing is left on disk.
func templateImage() ([]byte, error) {
	once.Do(func() {
		dir, e := os.MkdirTemp("", "coral-boardtest-*")
		if e != nil {
			err = e
			return
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "template.db")
		s, e := board.NewStoreWithKey(path, "")
		if e != nil {
			err = fmt.Errorf("boardtest: migrate template: %w", e)
			return
		}
		if e := s.Close(); e != nil { // the last connection closing checkpoints the write-ahead log into the file
			err = fmt.Errorf("boardtest: close template: %w", e)
			return
		}
		if _, e := os.Stat(path + "-wal"); e == nil {
			if fi, _ := os.Stat(path + "-wal"); fi != nil && fi.Size() > 0 {
				err = fmt.Errorf("boardtest: the template still has a write-ahead log")
				return
			}
		}
		image, err = os.ReadFile(path)
	})
	return image, err
}

// Open is board.NewStoreWithKey(path, "") for tests: when path does not exist yet it is first written from the migrated template and opened without running the schema again;
// an existing file (a test reopening its own database, or one it made old on purpose) is opened as it is, through the normal path.
func Open(path string) (*board.Store, error) {
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		img, err := templateImage()
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, img, 0o644); err != nil {
			return nil, err
		}
		return board.NewStorePrepared(path)
	}
	return board.NewStoreWithKey(path, "")
}
