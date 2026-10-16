package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3717: --cloud-max-mode wins over CORAL_CLOUD_MAX_MODE (an empty flag clears an inherited
// one); either, invalid, stops Coral from starting.
func TestResolveCloudMaxMode(t *testing.T) {
	for _, c := range []struct {
		flagGiven       bool
		flag, env, want string
		bad             bool
	}{
		{false, "", "", "", false},
		{false, "", "interact", "interact", false},
		{true, "launch", "view", "launch", false},
		{true, " VIEW ", "launch", "view", false},
		{true, "", "launch", "", false},
		{false, "", "root", "", true},
		{true, "admin", "view", "", true},
		{true, "launch", "nonsense", "launch", false},
	} {
		got, err := resolveCloudMaxMode(c.flagGiven, c.flag, c.env)
		if c.bad {
			assert.Error(t, err, "%+v", c)
			continue
		}
		assert.NoError(t, err, "%+v", c)
		assert.Equal(t, c.want, got, "%+v", c)
	}
}
