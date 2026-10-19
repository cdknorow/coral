//go:build !dev

package tracking

// SinkAvailable is false outside a dev build: a release binary cannot be redirected.
func SinkAvailable() bool { return false }
