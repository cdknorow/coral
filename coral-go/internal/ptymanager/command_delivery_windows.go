//go:build windows

package ptymanager

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/secfile"
)

// inlineCommandLimit stays below the small canonical input buffer used by a
// shell while it is still starting. Longer commands are delivered through a
// temporary script so the shell only has to receive a short path.
const inlineCommandLimit = 900

// commandInvocation returns a short PowerShell command that executes the
// complete command from a temporary script. The script removes itself before
// running the command; callers should remove it on delivery failure.
func commandInvocation(command string) (invocation string, cleanup func(), err error) {
	// In the launch temp folder (private to this user and SYSTEM, #4381), not the shared %TEMP%, and created
	// private: OpenNoFollow gives the new file this user and SYSTEM only, on the handle, before anything is
	// written, and refuses a planted link at the path (#25 G2B). The script holds the launch line (paths,
	// flags, the session token file's path).
	dir := agent.LaunchTempDir()
	for attempt := 0; ; attempt++ {
		var rnd [8]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return "", func() {}, fmt.Errorf("create command handoff: %w", err)
		}
		// Digits only: cmdhandoff.Sweep removes the ones a shell never ran by that name.
		path := filepath.Join(dir, "coral-command-"+strconv.FormatUint(binary.BigEndian.Uint64(rnd[:]), 10)+".ps1")
		file, err := secfile.OpenNoFollow(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			if os.IsExist(err) && attempt < 5 {
				continue
			}
			return "", func() {}, fmt.Errorf("create command handoff: %w", err)
		}
		content, invocation := psHandoff(path, command)
		if _, err := file.WriteString(content); err != nil {
			file.Close()
			os.Remove(path)
			return "", func() {}, fmt.Errorf("write command handoff: %w", err)
		}
		if err := file.Close(); err != nil {
			os.Remove(path)
			return "", func() {}, fmt.Errorf("close command handoff: %w", err)
		}
		return invocation, func() { _ = os.Remove(path) }, nil
	}
}
