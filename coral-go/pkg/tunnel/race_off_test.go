//go:build !race

package tunnel

// raceEnabled: the tests run under -race, where timing bounds don't hold.
const raceEnabled = false
