package dbcrypt

import "testing"

// #3920 R3: an in-memory SQLite name is not a file, on every OS: the header check finds nothing
// to refuse (on Windows ":memory:" isn't a valid file name, and opening it failed).
func TestInMemoryDatabaseNamesAreNotFiles(t *testing.T) {
	for _, name := range []string{":memory:", " :memory: ", "file::memory:", "file::memory:?cache=shared", "file:x?mode=memory&cache=shared"} {
		if !inMemoryDB(name) {
			t.Errorf("%q is in memory", name)
		}
		if encrypted, err := EncryptedFile(name); err != nil || encrypted {
			t.Errorf("EncryptedFile(%q) = %v, %v", name, encrypted, err)
		}
		if err := RejectEncryptedFile(name); err != nil {
			t.Errorf("RejectEncryptedFile(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"coral.db", "file:coral.db", "memory.db", "/tmp/:memory:x"} {
		if inMemoryDB(name) {
			t.Errorf("%q is a file", name)
		}
	}
}
