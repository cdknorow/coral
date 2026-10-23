package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitPermissionFlagsFindsEverySpelling(t *testing.T) {
	for name, tc := range map[string]struct{ in, permission, rest []string }{
		"none":              {[]string{"--model", "x"}, nil, []string{"--model", "x"}},
		"sandbox and -a":    {[]string{"--sandbox", "read-only", "-a", "on-request", "--search"}, []string{"--sandbox", "read-only", "-a", "on-request"}, []string{"--search"}},
		"equals":            {[]string{"--sandbox=read-only", "--ask-for-approval=never"}, []string{"--sandbox=read-only", "--ask-for-approval=never"}, nil},
		"short and config":  {[]string{"-s", "read-only", "-c", "approval_policy=never", "-c", "model=x"}, []string{"-s", "read-only", "-c", "approval_policy=never"}, []string{"-c", "model=x"}},
		"config equals":     {[]string{"--config=sandbox_mode=read-only"}, []string{"--config=sandbox_mode=read-only"}, nil},
		"bypass and claude": {[]string{"--dangerously-bypass-approvals-and-sandbox", "--permission-mode", "plan"}, []string{"--dangerously-bypass-approvals-and-sandbox", "--permission-mode", "plan"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			p, r := SplitPermissionFlags(tc.in)
			assert.Equal(t, tc.permission, p)
			assert.Equal(t, tc.rest, r)
		})
	}
}

// A Codex pair read back as a mode is never wider than the flags say; a pair it does not know is no mode (the launch then stores Default, never Auto).
func TestPermissionModeFromCodexSandboxFlags(t *testing.T) {
	for _, tc := range []struct {
		flags []string
		want  string
	}{
		{[]string{"--sandbox", "read-only", "-a", "on-request"}, "plan"},
		{[]string{"--sandbox", "workspace-write", "-a", "on-request"}, ""},
		{[]string{"--sandbox", "workspace-write", "-a", "never"}, ""},
		{[]string{"--sandbox", "read-only", "-a", "never"}, ""},
		{[]string{"--sandbox", "danger-full-access", "-a", "never"}, ""},
		{[]string{"--sandbox", "read-only"}, ""},
	} {
		assert.Equal(t, tc.want, PermissionModeFromFlags(tc.flags), "%v", tc.flags)
	}
}
