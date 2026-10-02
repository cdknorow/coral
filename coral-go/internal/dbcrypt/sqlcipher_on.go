//go:build sqlcipher && cgo

package dbcrypt

// The SQLCipher driver (cgo, linked against OpenSSL libcrypto) is compiled in
// only for the opt-in encrypted build: go build -tags sqlcipher,fts5 with
// CGO_ENABLED=1. Standard builds use the pure-Go driver and never link it.
import _ "github.com/0xCarbon/go-sqlite3"

// FeatureAvailable reports whether this binary can open encrypted databases.
func FeatureAvailable() bool { return true }

const buildNote = ""
