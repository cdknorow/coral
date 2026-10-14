//go:build sqlcipher && cgo

package dbcrypt

// The SQLCipher driver (cgo, linked against OpenSSL libcrypto) is compiled in
// only for the opt-in encrypted build: go build -tags sqlcipher,fts5 with
// CGO_ENABLED=1. Standard builds use the pure-Go driver and never link it.
import (
	"database/sql"

	sqlite3 "github.com/0xCarbon/go-sqlite3"
)

// FeatureAvailable reports whether this binary can open encrypted databases.
func FeatureAvailable() bool { return true }

const buildNote = ""

// The settings the driver's DSN has no key for, applied on every new connection
// (after the key and the DSN's own settings).
func init() {
	sql.Register(EncryptedDriver, &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		_, err := c.Exec("PRAGMA temp_store = MEMORY; PRAGMA cache_size = -8000;", nil)
		return err
	}})
}
