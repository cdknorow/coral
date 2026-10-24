package agent

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// errorUntrustedMountPoint is Windows' ERROR_UNTRUSTED_MOUNT_POINT (448): "The path cannot be traversed because it contains an untrusted mount point". Windows
// refuses to follow a junction that is not trusted (the redirection guard), so a PATH entry that is a junction to a junction cannot be listed or run from, by any program. An agent CLI
// whose only PATH entry is such a folder is "not found" to Coral and to the agent's shell alike.
const errorUntrustedMountPoint = syscall.Errno(448)

// UntrustedMountDir reports the first folder on PATH that Windows will not open because it is, or goes through, an untrusted mount point, and where binary would
// have been looked for (#340). Coral does not resolve the junctions itself: that would walk around a protection of Windows. It only says why the CLI is not found. Always
// false off Windows.
//
// A variable so a test can stand for the Windows answer.
var UntrustedMountDir = func(binary string) (string, bool) {
	if runtime.GOOS != "windows" {
		return "", false
	}
	return untrustedMountDir(binary, os.Getenv("PATH"), os.Getenv("PATHEXT"), os.Stat)
}

// untrustedMountDir is UntrustedMountDir with the PATH, PATHEXT and the lookup given (a test stands in for Windows' answer).
func untrustedMountDir(binary, pathEnv, pathExt string, stat func(string) (os.FileInfo, error)) (string, bool) {
	if binary == "" || strings.ContainsAny(binary, `/\`) {
		return "", false // a name only: a configured path or a ?binary= value with a separator is not looked up along PATH
	}
	exts := []string{""}
	if pathExt == "" {
		pathExt = ".com;.exe;.bat;.cmd"
	}
	for _, e := range strings.Split(strings.ToLower(pathExt), ";") {
		if e != "" {
			exts = append(exts, e)
		}
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		dir = strings.Trim(dir, `"`)
		if dir == "" || !filepath.IsAbs(dir) {
			continue // the current folder and relative entries are never searched (exec.LookPath refuses them too)
		}
		if _, err := stat(dir); isUntrustedMount(err) {
			return dir, true
		}
		for _, e := range exts {
			if _, err := stat(filepath.Join(dir, binary+e)); isUntrustedMount(err) {
				return dir, true
			}
		}
	}
	return "", false
}

func isUntrustedMount(err error) bool {
	var errno syscall.Errno
	return err != nil && errors.As(err, &errno) && errno == errorUntrustedMountPoint
}

// CLIUntrustedMountError is an agent CLI that is on PATH only through a folder Windows refuses to open (an untrusted mount point, such as Codex's chain of junctions after a Windows
// update). A launch answers it with a 422 and the fixed code cli_untrusted_mount, naming the folder and the fix: the real executable's path in Settings > CLI paths (#340).
type CLIUntrustedMountError struct {
	AgentType string
	Binary    string
	Dir       string
}

func (e *CLIUntrustedMountError) Error() string {
	return e.text(e.Dir)
}

// HubText is the sentence through Coral Hub: no folder of this computer in it.
func (e *CLIUntrustedMountError) HubText() string { return e.text("") }

func (e *CLIUntrustedMountError) text(dir string) string {
	where := "a folder on PATH"
	if dir != "" {
		where = "the PATH folder " + dir
	}
	return e.Binary + " CLI not found: it is on PATH only through " + where + ", which Windows will not open (an untrusted mount point). Set the real " + e.Binary +
		".exe in Settings > CLI paths (the setting " + CLIPathSettingKey(e.AgentType) + "); Coral does not follow the junctions itself."
}
