package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// read's and working-mode's arguments (#4599 second ranking, item 6: split out of cmdRead and runWorkingMode,
// behaviour unchanged).

func TestParseReadArgsAndPath(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want readArgs
		path string
	}{
		{"unread", []string{"coral-board", "read"}, readArgs{}, "/team%20a/messages?subscriber_id=Dev+1&limit=50"},
		{"all", []string{"coral-board", "read", "-a"}, readArgs{readAll: true}, "/team%20a/messages?subscriber_id=Dev+1&limit=50&all=true"},
		{"the newest", []string{"coral-board", "read", "--last", "5"}, readArgs{useLast: true, lastN: 5}, "/team%20a/messages/all?limit=5"},
		{"by id wins over --last", []string{"coral-board", "read", "--last", "5", "--id", "42"}, readArgs{messageID: 42, useLast: true, lastN: 5}, "/team%20a/messages/all?id=42"},
		{"a later flag overrides", []string{"coral-board", "read", "--id", "1", "--id", "2"}, readArgs{messageID: 2}, "/team%20a/messages/all?id=2"},
		{"a value that is not a number", []string{"coral-board", "read", "--last", "many"}, readArgs{useLast: true}, "/team%20a/messages/all?limit=0"},
		{"--last without a value", []string{"coral-board", "read", "--last"}, readArgs{}, "/team%20a/messages?subscriber_id=Dev+1&limit=50"},
		{"json", []string{"coral-board", "read", "--json", "--all"}, readArgs{readAll: true, asJSON: true}, "/team%20a/messages?subscriber_id=Dev+1&limit=50&all=true"},
	} {
		got := parseReadArgs(c.args)
		assert.Equal(t, c.want, got, c.name)
		assert.Equal(t, c.path, readPath(got, "team a", "Dev 1"), c.name)
	}
}

func TestCheckWorkingModeFlags(t *testing.T) {
	seen := func(names ...string) map[string]bool {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	for _, c := range []struct {
		seen map[string]bool
		want string
	}{
		{seen(), ""},
		{seen("mode", "dependency-guidance"), ""},
		{seen("presets"), ""},
		{seen("create", "instructions"), ""},
		{seen("edit", "instructions-file"), ""},
		{seen("presets", "reset"), "use one preset operation or update the selected working mode"},
		{seen("reset", "mode"), "use one preset operation or update the selected working mode"},
		{seen("name"), "preset content requires --create or --edit"},
		{seen("create", "instructions", "instructions-file"), "use --instructions or --instructions-file, not both"},
		{seen("edit"), "--create and --edit require --instructions or --instructions-file"},
	} {
		err := checkWorkingModeFlags(c.seen)
		if c.want == "" {
			assert.NoError(t, err, "%v", c.seen)
		} else {
			assert.EqualError(t, err, c.want, "%v", c.seen)
		}
	}
}

func TestWorkingModeRequest(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rules.md")
	require.NoError(t, os.WriteFile(file, []byte("from the file"), 0o644))
	for _, c := range []struct {
		name         string
		args         []string
		method, path string
		body         any
	}{
		{"show", nil, "GET", "/team%20a/working-mode", nil},
		{"presets", []string{"--presets"}, "GET", "/team%20a/working-mode/presets", nil},
		{"create", []string{"--create", "x", "--name", "X", "--instructions", "do it"}, "POST", "/team%20a/working-mode/presets", map[string]string{"id": "x", "name": "X", "instructions": "do it"}},
		{"edit from a file", []string{"--edit", "a b", "--instructions-file", file}, "PUT", "/team%20a/working-mode/presets/a%20b", map[string]string{"id": "", "name": "", "instructions": "from the file"}},
		{"reset", []string{"--reset", "review"}, "POST", "/team%20a/working-mode/presets/review/reset", nil},
		{"select", []string{"--mode", "tdd"}, "PUT", "/team%20a/working-mode", nil},
	} {
		f, err := parseWorkingModeFlags(c.args)
		require.NoError(t, err, c.name)
		method, path, body, err := workingModeRequest(f, "team a")
		require.NoError(t, err, c.name)
		assert.Equal(t, []any{c.method, c.path, c.body}, []any{method, path, body}, c.name)
	}
	f, err := parseWorkingModeFlags([]string{"--mode", "tdd", "--dependency-guidance"})
	require.NoError(t, err)
	updated, err := updatedWorkingMode(f, func() ([]byte, error) {
		return []byte(`{"mode":"open","custom_instructions":"keep","dependency_guidance":false,"instructions":"computed"}`), nil
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"mode": "tdd", "custom_instructions": "keep", "dependency_guidance": true}, updated)
}
