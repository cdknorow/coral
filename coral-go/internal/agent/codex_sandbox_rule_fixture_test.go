package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6419: the sandbox rule of the Codex translation (a capability set -> bypass, workspace-write, read-only, and whether it asks) is held to one list of cases that the dashboard's form reads too
// (tests/frontend/preset_enforcement.test.mjs against preset_enforcement.js codexEnforced), so the two cannot drift: the form tells the person what a launch will do.
// The file is written from the rule, not by the code under test, and nothing here rewrites it.
type codexRuleCase struct {
	Name   string   `json:"name"`
	Allow  []string `json:"allow"`
	Deny   []string `json:"deny"`
	Expect *struct {
		Sandbox  string `json:"sandbox"`
		Approval string `json:"approval"`
		Search   bool   `json:"search"`
	} `json:"expect"`
	Flags []string `json:"flags"`
}

func TestCodexSandboxRuleFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex_sandbox_rule.json")
	require.NoError(t, err)
	var doc struct {
		Cases []codexRuleCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.GreaterOrEqual(t, len(doc.Cases), 60, "the fixture covers every subset of the five capabilities with and without a deny list, and the cases around them")
	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			caps := &Capabilities{Allow: c.Allow, Deny: c.Deny}
			perms := TranslateToCodexPermissions(caps)
			flags, bypass, applied := codexCapabilityFlags(nil, caps, "")
			if c.Expect == nil {
				assert.Nil(t, perms, "no capabilities: no translation")
				assert.False(t, applied)
				assert.Empty(t, flags)
				return
			}
			require.NotNil(t, perms)
			sandbox, approval := perms.SandboxMode, perms.ApprovalPolicy
			switch {
			case perms.FullAuto:
				sandbox, approval = "workspace-write", "never"
			}
			assert.Equal(t, c.Expect.Sandbox, sandbox, "sandbox")
			assert.Equal(t, c.Expect.Approval, approval, "approval")
			assert.Equal(t, c.Expect.Search, perms.Search, "search")
			assert.False(t, bypass, "capabilities alone are never the no-sandbox case (#6422)")
			assert.True(t, applied)
			assert.Equal(t, c.Flags, flags, "the exact flags a launch passes: %s", strings.Join(flags, " "))
		})
	}
}
