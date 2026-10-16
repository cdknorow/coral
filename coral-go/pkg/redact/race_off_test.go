//go:build !race

package redact

// raceEnabled: the tests run under -race, where timing bounds don't hold.
const raceEnabled = false
