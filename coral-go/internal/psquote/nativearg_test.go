package psquote

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNativeArg(t *testing.T) {
	for s, want := range map[string]string{
		"":                                   "",
		"plain":                              "plain",
		"a b":                                "a b", // 5.1 adds the quotes
		`a"b`:                                `a\"b`,
		`a "b c" d`:                          `a \"b c\" d`,
		`C:\dir with space\`:                 `C:\dir with space\\`, // 5.1's closing quote stays a quote
		`C:\nospace\`:                        `C:\nospace\`,         // no quotes added: nothing to double
		`a\"b`:                               `a\\\"b`,
		`a\\"b`:                              `a\\\\\"b`,
		`x y\\`:                              `x y\\\\`,
		`(x86)`:                              `(x86)`,
		`"`:                                  `\"`,
		`\`:                                  `\`,
		"tab\there":                          "tab\there",
		`a"b c`:                              `"a""b c"`, // 5.1 wouldn't quote it: quoted here
		`ab"c d"`:                            `"ab""c d"""`,
		`x=\"C:/Program Files (x86)/a.exe\"`: `"x=\\""C:/Program Files (x86)/a.exe\\"""`,
		`"a b\"`:                             `"""a b\\"""`,
		// Codex's hook value, the operator's report (#12): each \" in the TOML becomes \\\".
		`hooks.SessionStart=[{hooks=[{type="command",command="\"C:/Program Files (x86)/Coral/coral-hook-agentic-state.exe\""}]}]`: `hooks.SessionStart=[{hooks=[{type=\"command\",command=\"\\\"C:/Program Files (x86)/Coral/coral-hook-agentic-state.exe\\\"\"}]}]`,
	} {
		assert.Equal(t, want, NativeArg(s), "%s", s)
	}
}
