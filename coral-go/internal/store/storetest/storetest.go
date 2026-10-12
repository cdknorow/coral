// Package storetest gives tests a ready sessions database without paying for
// the migrations every time. store.Open on an empty file runs the whole
// migration chain, which under -race (pure-Go SQLite, fully instrumented)
// costs ~0.7s per test; hundreds of tests did it. Here the chain runs once per
// test process and each test starts from a copy of the result.
//
// Only tests import this package.
package storetest

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cdknorow/coral/internal/store"
)

var (
	once  sync.Once
	image []byte
	err   error
)

// templateImage returns the bytes of a freshly migrated, checkpointed
// database. Nothing is left on disk.
func templateImage() ([]byte, error) {
	once.Do(func() {
		dir, e := os.MkdirTemp("", "coral-storetest-*")
		if e != nil {
			err = e
			return
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "template.db")
		db, e := store.Open(path)
		if e != nil {
			err = fmt.Errorf("storetest: migrate template: %w", e)
			return
		}
		if _, e := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); e != nil {
			db.Close()
			err = fmt.Errorf("storetest: checkpoint template: %w", e)
			return
		}
		if e := db.Close(); e != nil {
			err = fmt.Errorf("storetest: close template: %w", e)
			return
		}
		image, err = os.ReadFile(path)
	})
	return image, err
}

// Open is store.Open for tests: when path does not exist yet it is first
// written from the migrated template, so Open only has to confirm the schema
// is current. An existing file (a test reopening its own database) is opened
// as it is.
func Open(tb testing.TB, path string) (*store.DB, error) {
	tb.Helper()
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
	}
	return store.Open(path)
}
