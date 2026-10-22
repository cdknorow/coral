//go:build windows

package hooks

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// #279: each refusal says what was refused, why, and what to do next, in whole sentences, and never the old
// "Is another account's program on Coral's port?" for a program of this user's own.
func TestEachRefusalSaysWhyAndWhatToDo(t *testing.T) {
	me, err := currentUserSID()
	require.NoError(t, err)
	other, err := windows.StringToSid("S-1-5-21-1-2-3-1001")
	require.NoError(t, err)
	cases := []struct {
		name  string
		owner func(uint32) (*windows.SID, string, error)
		want  []string
	}{
		{"another program of this user", func(uint32) (*windows.SID, string, error) { return me, `C:\tools\node.exe`, nil },
			[]string{`The program on that port is C:\tools\node.exe (PID 4242), not Coral's coral.exe or coral-tray.exe.`,
				"Start Coral, or stop the program that holds its port.", "dev or test build", "'make dev'"}},
		{"another account", func(uint32) (*windows.SID, string, error) { return other, `C:\Coral\coral.exe`, nil },
			[]string{"(PID 4242) runs as another account (S-1-5-21-1-2-3-1001), not as you or the owner of Coral's data folder.",
				"Start Coral as yourself, or stop that program."}},
		{"an owner that can't be read", func(uint32) (*windows.SID, string, error) { return nil, "", windows.ERROR_ACCESS_DENIED },
			[]string{"Coral couldn't check who runs the program on that port (PID 4242):", "Check what is listening on port 8450, or start Coral."}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetListenerCheck(t, func(string, int) ([]uint32, error) { return []uint32{4242}, nil }, c.owner)
			coralFolders = func() []string { return []string{`C:\Coral`} }
			err := checkListener("127.0.0.1", 8450)
			var foreign *ForeignListenerError
			require.True(t, errors.As(err, &foreign), "refused: %v", err)
			msg := err.Error()
			assert.Contains(t, msg, "Refused: the agent's token was not sent to port 8450 on this computer. ")
			for _, w := range c.want {
				assert.Contains(t, msg, w)
			}
			assert.NotContains(t, msg, "Is another account's program")
			assert.NotContains(t, msg, "coral: ", "no tool prefix in the middle of a CLI's line")
		})
	}
}
