package dbcrypt

import "net/url"

// EncryptedDriver is the database/sql driver for encrypted databases: the SQLCipher
// driver, registered with a connect hook for the settings its DSN has no key for
// (temp_store, cache_size). It exists only in the encrypted build; RequireAvailable
// says so before anything opens it.
const EncryptedDriver = "sqlite3_coral"

// DSNOptions are the per-database settings of an encrypted DSN.
type DSNOptions struct {
	ForeignKeys bool // PRAGMA foreign_keys = ON (the sessions database)
	ReadOnly    bool // PRAGMA query_only = ON, and no journal-mode change (a read pool, a verification)
}

// EncryptedDSN is the DSN for an encrypted database at path (#3454). The SQLCipher
// driver reads only its own keys (_busy_timeout, _journal_mode, ...) and silently
// ignores the modernc-style _pragma_ keys Coral used before, so encrypted databases
// ran in rollback-journal mode, without foreign keys and with a 5 s busy timeout.
func EncryptedDSN(path, key string, o DSNOptions) string {
	q := url.Values{}
	q.Set("_key", encryptedKeyHex(key))
	q.Set("_busy_timeout", "30000")
	q.Set("_synchronous", "NORMAL")
	if o.ForeignKeys {
		q.Set("_foreign_keys", "1")
	}
	if o.ReadOnly {
		q.Set("_query_only", "1")
	} else {
		q.Set("_journal_mode", "WAL")
	}
	return path + "?" + q.Encode()
}
