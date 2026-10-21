//go:build windows

package routes

import (
	"testing"
	"unicode"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/winshell"
)

// promptReceived is the prompt a launched CLI gets from a prompt file the launch line reads with
// "$(Get-Content ...)". Under Windows PowerShell 5.1 the file holds the prompt escaped by
// psquote.NativeArg (#29): 5.1 passes it as it is, adding double quotes when it has whitespace
// outside the quotes it counts, and the CLI's argv parser reads it back. pwsh passes the text
// itself.
func promptReceived(t *testing.T, content string) string {
	t.Helper()
	if !winshell.LegacyNativeArgs(winshell.PowerShell()) {
		return content
	}
	line := content
	quotes := 0
	for _, r := range content {
		if r == '"' {
			quotes++
		} else if unicode.IsSpace(r) && quotes%2 == 0 {
			line = `"` + content + `"`
			break
		}
	}
	argv, err := windows.DecomposeCommandLine("cli " + line)
	require.NoError(t, err)
	require.Len(t, argv, 2, "the prompt reaches the CLI as one argument: %q", content)
	return argv[1]
}
