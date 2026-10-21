package ptymanager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWindowsEnvBlock(t *testing.T) {
	got := windowsEnvBlock([]string{
		"Path=C:\\Windows",
		"=C:=C:\\work",
		"TERM=dumb",
		"SystemRoot=C:\\Windows",
		"term=xterm-256color", // the session's, later: replaces the server's, whatever the case
		"EMPTY=",
		"A=x=y",
	})
	assert.Equal(t, []string{
		"=C:=C:\\work",
		"A=x=y",
		"EMPTY=",
		"Path=C:\\Windows",
		"SystemRoot=C:\\Windows",
		"term=xterm-256color",
	}, got)
	assert.Empty(t, windowsEnvBlock(nil))
	assert.Equal(t, "=C:", envName("=C:=C:\\x"))
	assert.Equal(t, "A", envName("A=x=y"))
	assert.Equal(t, "", envName(""))
}
