//go:build !(sqlcipher && cgo)

package dbcrypt

// FeatureAvailable reports whether this binary can open encrypted databases.
// It is false in the standard build, and also when the sqlcipher tag is set
// without cgo, because the SQLCipher driver cannot work without cgo.
func FeatureAvailable() bool { return false }
