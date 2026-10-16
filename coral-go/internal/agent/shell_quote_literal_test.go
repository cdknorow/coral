package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3904 (sweep #3903 item 1): PowerShell also reads U+2018, U+2019, U+201A and U+201B as single
// quotes. The literal quoting used for env values, the token file and prompt files doubles each
// of them, so a value with a curly apostrophe stays one string.
func TestShellQuoteLiteralPowerShellDoublesCurlyQuotes(t *testing.T) {
	t.Setenv("CORAL_SHELL", "pwsh")
	for _, q := range []string{"'", "\u2018", "\u2019", "\u201a", "\u201b"} {
		v := "C:\\Users\\Jane" + q + "s; Remove-Item -Recurse C:\\"
		got := shellQuoteLiteral(v)
		assert.Equal(t, "'C:\\Users\\Jane"+q+q+"s; Remove-Item -Recurse C:\\'", got, "%U", []rune(q)[0])
		assert.Equal(t, "$env:K="+got+";", formatEnvExport("K", v), "%U", []rune(q)[0])
		assert.True(t, strings.HasPrefix(got, "'") && strings.HasSuffix(got, "'"))
	}
}
