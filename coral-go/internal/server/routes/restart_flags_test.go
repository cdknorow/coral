package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A restarted agent's flags (#4599 item 2: split out of Restart, behaviour unchanged).
func TestRestartFlags(t *testing.T) {
	for _, c := range []struct {
		name         string
		stored       []string
		model, extra string
		want         []string
	}{
		{"nothing", nil, "", "", nil},
		{"kept", []string{"--verbose"}, "", "", []string{"--verbose"}},
		{"the model replaces a stored --model and its value", []string{"--model", "old", "--verbose"}, "new", "", []string{"--verbose", "--model", "new"}},
		{"-m and --model= and -m= go too", []string{"-m", "a", "--model=b", "-m=c", "--x"}, "", "", []string{"--x"}},
		{"a cleared model leaves none", []string{"--model", "old"}, "", "", nil},
		{"permission flags go (capabilities decide them)", []string{"--dangerously-skip-permissions", "--full-auto", "--verbose"}, "", "", []string{"--verbose"}},
		{"extra flags last, split on white space", []string{"--verbose"}, "m1", " --a  --b ", []string{"--verbose", "--model", "m1", "--a", "--b"}},
	} {
		assert.Equal(t, c.want, restartFlags(c.stored, c.model, c.extra), c.name)
	}
}
