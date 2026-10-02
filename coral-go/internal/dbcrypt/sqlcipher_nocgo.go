//go:build sqlcipher && !cgo

package dbcrypt

const buildNote = " (this binary was built with the sqlcipher tag but without cgo; rebuild with CGO_ENABLED=1)"
