package main

import (
	"bytes"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/config"
)

// #235: `coral help`, `coral --help` and `coral -h` list the commands first, then the main flags, and exit 0; the full server
// flag list is not dumped (it is --help-all).
func TestHelpListsTheCommandsFirst(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}, {"-help"}} {
		var out, errb bytes.Buffer
		code, ok := helpRequest(args, &out, &errb)
		assert.True(t, ok, args)
		assert.Equal(t, 0, code, args)
		s := out.String()
		assert.Contains(t, s, "Commands:", args)
		for _, c := range []string{"open", "api-key", "set-password", "reset-password", "setup-code", "cloud", "auto-accept", "artifacts", "db", "version"} {
			assert.Contains(t, s, "\n  "+c+" ", "%v lists %s", args, c)
		}
		assert.Contains(t, s, "--port N")
		assert.Contains(t, s, "coral --help-all")
		assert.NotContains(t, s, "agent-priority", "%v: not every server flag", args)
		assert.Less(t, len(bytes.Split(out.Bytes(), []byte("\n"))), 32, "%v: short", args)
		assert.Empty(t, errb.String())
	}
}

// `coral help <command>` and `coral <command> ... --help` print that command's usage and exit 0, with this platform's default
// address (8450 on Windows), never 8420 on Windows.
func TestEachCommandHasItsHelpWithExitZero(t *testing.T) {
	want := "http://localhost:" + strconv.Itoa(config.DefaultPort)
	for _, c := range commands() {
		for _, args := range [][]string{{"help", c.name}, {c.name, "--help"}, {c.name, "-h"}} {
			if c.name == "help" && args[0] != "help" {
				continue
			}
			var out, errb bytes.Buffer
			code, ok := helpRequest(args, &out, &errb)
			assert.True(t, ok, args)
			assert.Equal(t, 0, code, args)
			assert.Contains(t, out.String(), "coral "+c.name, args)
		}
	}
	var out, errb bytes.Buffer
	_, _ = helpRequest([]string{"cloud", "connect", "--help"}, &out, &errb)
	assert.Contains(t, out.String(), "coral cloud connect", "a nested command's --help")
	assert.Contains(t, out.String(), want, "the platform's default address")
}

// An unknown command is reported as one, with a hint where a person meant something else, the list of commands, and exit 2;
// no server flag is dumped.
func TestAnUnknownCommandSaysSoWithAHint(t *testing.T) {
	var errb bytes.Buffer
	assert.Equal(t, 2, unknownCommand(&errb, "stop"))
	s := errb.String()
	assert.Contains(t, s, `unknown command "stop"`)
	assert.Contains(t, s, "Ctrl+C")
	assert.Contains(t, s, "Commands: open,")
	assert.NotContains(t, s, "agent-priority")

	errb.Reset()
	code, ok := helpRequest([]string{"help", "bogus"}, &bytes.Buffer{}, &errb)
	assert.True(t, ok)
	assert.Equal(t, 2, code)
	assert.Contains(t, errb.String(), `unknown command "bogus"`)

	for _, w := range []string{"stop", "status", "bogus"} {
		code, ok := subcommand([]string{w})
		assert.True(t, ok, "%q is answered, the server does not start", w)
		assert.Equal(t, 2, code, w)
	}
	_, ok = subcommand([]string{"--port", "1"})
	assert.False(t, ok, "a flag still goes to the server's parser")
}

// The commands' default address is this platform's port.
func TestTheDefaultAddressIsThisPlatformsPort(t *testing.T) {
	assert.Equal(t, "http://localhost:"+strconv.Itoa(config.DefaultPort), defaultCoralURL())
}
