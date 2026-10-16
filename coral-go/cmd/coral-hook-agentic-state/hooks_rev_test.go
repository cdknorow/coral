package main

import "testing"

// #3655 F3: the hook reads its set's revision from the command Coral installed.
func TestParseArgsHooksRev(t *testing.T) {
	for _, c := range []struct {
		args []string
		want hookArgs
	}{
		{nil, hookArgs{}},
		{[]string{"--hooks-rev", "2"}, hookArgs{hooksRev: 2}},
		{[]string{"--hooks-rev", "x"}, hookArgs{}},
		{[]string{"--hooks-rev", "-1"}, hookArgs{}},
		{[]string{"--hooks-rev"}, hookArgs{}},
		{[]string{"--session-clear", "--hooks-rev", "3"}, hookArgs{sessionClear: true, hooksRev: 3}},
		{[]string{"--agy", "PreToolUse"}, hookArgs{agyEvent: "PreToolUse"}},
	} {
		if got := parseArgs(c.args); got != c.want {
			t.Errorf("parseArgs(%q) = %+v, want %+v", c.args, got, c.want)
		}
	}
}
