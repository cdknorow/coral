package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// wait's arguments (#4599 second ranking, item 3: split out of cmdWait, behaviour unchanged).
func TestParseWaitArgs(t *testing.T) {
	for _, c := range []struct {
		name    string
		args    []string
		want    waitArgs
		problem string
	}{
		{"nothing: any message on the joined board", nil, waitArgs{project: "joined"}, ""},
		{"from someone, with a reason", []string{"--from", "QA", "the review"}, waitArgs{project: "joined", from: "QA", reason: "the review"}, ""},
		{"another board", []string{"--board", "other", "--task", "12"}, waitArgs{project: "other", task: "12"}, ""},
		{"--project too", []string{"--project", "other"}, waitArgs{project: "other"}, ""},
		{"--reason after a positional reason replaces it", []string{"first", "--reason", "second"}, waitArgs{project: "joined", reason: "second"}, ""},
		{"operator, timeout, block", []string{"--operator", "ship it?", "--timeout", "30m", "--block"}, waitArgs{project: "joined", operator: "ship it?", timeout: "30m", block: true}, ""},
		{"status", []string{"--status"}, waitArgs{project: "joined", status: true}, ""},
		{"a flag without its value", []string{"--from"}, waitArgs{project: "joined"}, "--from requires a value"},
		{"a flag followed by a flag", []string{"--task", "--block"}, waitArgs{project: "joined"}, "--task requires a value"},
		{"an unknown option", []string{"--nope"}, waitArgs{project: "joined"}, "unknown option --nope"},
		{"a timeout that is not a duration", []string{"--timeout", "not-a-duration"}, waitArgs{project: "joined", timeout: "not-a-duration"}, `--timeout needs a duration such as 30m or 2h, got "not-a-duration"`},
		{"a timeout with no unit", []string{"--timeout", "30"}, waitArgs{project: "joined", timeout: "30"}, `--timeout needs a duration such as 30m or 2h, got "30"`},
		{"a zero timeout", []string{"--timeout", "0s"}, waitArgs{project: "joined", timeout: "0s"}, `--timeout needs a duration such as 30m or 2h, got "0s"`},
		{"a negative timeout", []string{"--timeout", "-5m"}, waitArgs{project: "joined"}, "--timeout requires a value"},
		{"a second reason", []string{"one", "two"}, waitArgs{project: "joined", reason: "one"}, `unexpected argument "two"`},
		{"two targets", []string{"--from", "QA", "--task", "1"}, waitArgs{project: "joined", from: "QA", task: "1"}, "choose only one of --from, --task, --commit, or --operator"},
		{"status with a target", []string{"--status", "--from", "QA"}, waitArgs{project: "joined", status: true, from: "QA"}, "--status and --cancel cannot be combined with wait registration options"},
		{"cancel with a reason", []string{"--cancel", "why"}, waitArgs{project: "joined", cancel: true, reason: "why"}, "--status and --cancel cannot be combined with wait registration options"},
		{"status and cancel", []string{"--status", "--cancel"}, waitArgs{project: "joined", status: true, cancel: true}, "--status and --cancel are mutually exclusive"},
	} {
		got, problem := parseWaitArgs(c.args, "joined")
		assert.Equal(t, c.problem, problem, c.name)
		if c.problem == "" {
			assert.Equal(t, c.want, got, c.name)
		}
	}
}

func TestWaitTarget(t *testing.T) {
	for _, c := range []struct {
		w                       waitArgs
		typ, target, wantReason string
	}{
		{waitArgs{reason: "r"}, "message", "", "r"},
		{waitArgs{from: "QA", reason: "r"}, "message", "QA", "r"},
		{waitArgs{task: "7"}, "task", "7", ""},
		{waitArgs{commit: "abc"}, "commit", "abc", ""},
		{waitArgs{operator: "ok to ship?", reason: "ignored"}, "operator", "Operator", "ok to ship?"},
	} {
		typ, target, reason := c.w.target()
		assert.Equal(t, []string{c.typ, c.target, c.wantReason}, []string{typ, target, reason})
	}
}
