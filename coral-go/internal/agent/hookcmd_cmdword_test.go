package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// cmdWord is plain string logic: its test runs on every OS, so a Mac run catches a change that would let cmd.exe read an operator ("&", "%", "^") in a hook path (#5661; it lived in the Windows-only file).
func TestCmdWord(t *testing.T) {
	for p, want := range map[string]bool{
		"C:/Coral/coral-hook-agentic-state.exe":                     true,
		"C:/PROGRA~2/Coral/coral-hook-agentic-state.exe":            true,
		"C:/Users/Zoë/AppData/Local/Coral/coral-hook-x.exe":         true, // a non-ASCII letter
		"C:/Users/日本/Coral/x.exe":                                   true,
		"C:/Program Files (x86)/Coral/coral-hook-agentic-state.exe": false,
		"C:/a&b/x.exe": false, "C:/a^b/x.exe": false, "C:/%TEMP%/x.exe": false, "C:/a!b/x.exe": false,
		"C:/a(b/x.exe": false, "C:/a;b/x.exe": false, "C:/a,b/x.exe": false, "C:/a=b/x.exe": false,
		"C:/a|b/x.exe": false, "C:/a<b/x.exe": false, `C:/a"b/x.exe`: false, "C:/a\tb/x.exe": false,
		"C:/a@b/x.exe": false, "C:/O'Brien/x.exe": false, "C:/a+b/x.exe": false, "C:/a`b/x.exe": false,
		"C:/a\u00a0b/x.exe":          false, // a no-break space is not a letter
		"//server/share/Coral/x.exe": false, // UNC: fail closed (#21)
		"":                           false,
	} {
		assert.Equal(t, want, cmdWord(p), p)
	}
}
