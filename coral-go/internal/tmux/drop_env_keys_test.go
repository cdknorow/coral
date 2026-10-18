package tmux

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #4598: the identity variables are dropped in one pass: only entries named exactly by a key go,
// in order, and the input is not changed.
func TestDropEnvKeys(t *testing.T) {
	env := []string{"PATH=/bin", "CORAL_SESSION_NAME=a", "CORAL_SESSION_NAME_X=keep", "CORAL_SESSION_ID=b", "CORAL_SESSION_IDX=keep", "NOEQUALS", "CORAL_SESSION_TOKEN=", "HOME=/h", "CORAL_SESSION_NAME=dup"}
	orig := append([]string(nil), env...)
	got := dropEnvKeys(env, []string{"CORAL_SESSION_NAME", "CORAL_SESSION_ID", "CORAL_SUBSCRIBER_ID", "CORAL_SESSION_TOKEN"})
	assert.Equal(t, []string{"PATH=/bin", "CORAL_SESSION_NAME_X=keep", "CORAL_SESSION_IDX=keep", "NOEQUALS", "HOME=/h"}, got)
	assert.Equal(t, orig, env, "the input is untouched")
	assert.Equal(t, orig, dropEnvKeys(orig, nil))
	assert.Empty(t, dropEnvKeys(nil, []string{"A"}))
}
