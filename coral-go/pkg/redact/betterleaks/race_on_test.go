//go:build race

package betterleaks

// raceEnabled: the tests run under -race, where timing bounds don't hold.
const raceEnabled = true
