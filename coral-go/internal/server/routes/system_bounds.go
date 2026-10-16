package routes

// Bounds for two system routes that read or run what the request names (#3583, Security's sweep
// items 4 and 5): CLICheck runs a binary to ask its version, and ImportTeam reads a team folder.
// Both are the Operator's (a person at the dashboard or the signed command line); an agent's
// token is refused.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/safefs"
	"github.com/cdknorow/coral/internal/secfile"
)

// knownCLIBinaries are the CLI names Coral launches (claude, codex, agy, pi).
func knownCLIBinaries() map[string]bool {
	out := map[string]bool{}
	for _, t := range agent.LaunchableAgentTypes() {
		if info := agent.GetCLIInfo(t); info != nil {
			out[info.Binary] = true
		}
	}
	return out
}

// cliCheckRefusal says why CLICheck won't run binary, or "" when it may. A bare name must be a
// known CLI's; a path must be absolute and either a CLI path configured in settings or named
// like that CLI's binary (the Settings page checks a path before it's saved), and neither it nor
// what it resolves to may be in Coral's data folder, a live agent's working folder or a temp
// folder: those hold files an agent can write.
func (h *SystemHandler) cliCheckRefusal(ctx context.Context, binary, agentType string) string {
	known := knownCLIBinaries()
	if !strings.ContainsAny(binary, `/\`) {
		if known[binary] {
			return ""
		}
		return "binary must be a known CLI (" + strings.Join(sortedKeys(known), ", ") + ") or the path of one"
	}
	if !filepath.IsAbs(binary) {
		return "binary must be an absolute path"
	}
	clean := filepath.Clean(binary)
	configured := false
	settings := map[string]string{}
	if h.ss != nil {
		if s, err := h.ss.GetSettings(ctx); err == nil && s != nil {
			settings = s
		}
	}
	for _, t := range agent.LaunchableAgentTypes() {
		if p := agent.ResolveCLIPath(settings, t); p != "" && filepath.Clean(p) == clean {
			configured = true
		}
	}
	if !configured {
		want := known
		if info := agent.GetCLIInfo(agentType); info != nil {
			want = map[string]bool{info.Binary: true}
		}
		if !want[filepath.Base(clean)] {
			return "binary must be a configured CLI path or a path to " + strings.Join(sortedKeys(want), " or ")
		}
	}
	for _, p := range []string{clean, realOrSelf(clean)} {
		if why := h.untrustedLocation(ctx, p); why != "" {
			return "binary is " + why
		}
	}
	return ""
}

// untrustedLocation says whether p is inside a folder an agent can write to: Coral's data
// folder, a live agent's working folder, or a temp folder.
func (h *SystemHandler) untrustedLocation(ctx context.Context, p string) string {
	real := realOrSelf(p)
	inside := func(dir string) bool {
		if dir == "" {
			return false
		}
		d := realOrSelf(dir)
		return within(d, real) || within(dir, p)
	}
	if h.cfg != nil && inside(h.cfg.CoralDir()) {
		return "in Coral's data folder"
	}
	for _, tmp := range cliTempDirs() {
		if inside(tmp) {
			return "in a temporary folder"
		}
	}
	if h.ss != nil {
		if live, err := h.ss.GetAllLiveSessions(ctx); err == nil {
			for _, s := range live {
				if inside(s.WorkingDir) {
					return "in an agent's working folder"
				}
			}
		}
	}
	return ""
}

// cliTempDirs are the temp folders a checked binary may not be in (a var for tests, which run
// inside one).
var cliTempDirs = func() []string { return []string{os.TempDir(), "/tmp", "/private/tmp", "/var/tmp"} }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Team import limits.
const (
	importFileMax   = 256 << 10 // a SKILL.md or agents/*.md larger than this is refused
	importAgentsMax = 50        // agent files read from agents/
)

var errImportRefused = errors.New("import refused")

// importTeamRoot checks the folder a team import reads: it must exist, be a directory that is
// neither the file system's root nor the home folder (or a parent of it), not itself a dot-folder,
// and not a sensitive location (.ssh, .aws, credentials and the like). A team inside a dot-folder
// (~/.claude/skills/<team>) is allowed. It returns the folder's real path.
func importTeamRoot(path string) (string, error) {
	if safefs.RefuseRemote(path) != nil { // before the stat, which would connect to the host (#3671)
		return "", fmt.Errorf("%w: a network path can't be imported from", errImportRefused)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: path must be absolute", errImportRefused)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: path does not exist or is not a directory", errImportRefused)
	}
	real := realPath(path)
	if real == "" || !rootAllowed(real) || sensitivePath(real) || sensitivePath(filepath.Clean(path)) ||
		strings.HasPrefix(filepath.Base(real), ".") || strings.HasPrefix(filepath.Base(filepath.Clean(path)), ".") {
		return "", fmt.Errorf("%w: this folder can't be imported from", errImportRefused)
	}
	return real, nil
}

// readImportFile reads one team file for an import: a regular file (not a symlink), directly in
// the folder it is listed in, inside the import's real root, not a dot-file or a sensitive path,
// and at most importFileMax. ok is false when the file is absent or skipped; err when it is
// refused (too large).
func readImportFile(root, path string) (data []byte, ok bool, err error) {
	fi, statErr := os.Lstat(path)
	if statErr != nil || !fi.Mode().IsRegular() {
		return nil, false, nil // absent, a symlink or not a file: never followed
	}
	name := filepath.Base(path)
	if strings.HasPrefix(name, ".") || sensitivePath(path) {
		return nil, false, nil
	}
	if real := realPath(path); real == "" || !within(root, real) {
		return nil, false, nil
	}
	if fi.Size() > importFileMax {
		return nil, false, fmt.Errorf("%w: %s is larger than %d KiB", errImportRefused, name, importFileMax>>10)
	}
	// No symlink swapped in after the Lstat: open without following one, and check that the
	// handle is the regular file that was checked.
	f, openErr := secfile.OpenNoFollow(path, os.O_RDONLY, 0)
	if openErr != nil {
		return nil, false, nil
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() || !os.SameFile(fi, st) {
		return nil, false, nil
	}
	data, err = io.ReadAll(io.LimitReader(f, importFileMax+1))
	if err != nil {
		return nil, false, nil
	}
	if len(data) > importFileMax {
		return nil, false, fmt.Errorf("%w: %s is larger than %d KiB", errImportRefused, name, importFileMax>>10)
	}
	return data, true, nil
}
