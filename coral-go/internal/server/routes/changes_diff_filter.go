package routes

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cdknorow/coral/internal/gitutil"
)

// Persisted and tunneled changes diffs (#3555; Security's answer on it). A diff Coral stores
// (a task's or a session's changes.diff) or serves through Coral Hub never carries a
// sensitive file, a dot-file, a binary file or a large untracked file: each is replaced by a
// one-line note, and the whole is capped. The live diff on this computer is unchanged.
const (
	diffSectionMax = 256 << 10 // a file's section (or an untracked file) over this isn't stored
	diffPersistMax = 8 << 20   // a stored or tunneled diff over this is cut, with a note
)

// diffNote is a line git apply skips (it isn't part of any patch).
func diffNote(format string, args ...any) []byte {
	return []byte("# Coral: " + fmt.Sprintf(format, args...) + "\n")
}

// diffPathHidden: a path whose changes never leave this computer in a stored or tunneled
// diff: the sensitive-path list, or any dot-file or dot-folder component.
func diffPathHidden(rel string) bool {
	if sensitivePath(rel) {
		return true
	}
	for _, c := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(c, ".") && c != "." && c != ".." {
			return true
		}
	}
	return false
}

// unquoteGitPath undoes git's C-style quoting of a path ("a/x y" style), else returns it as is.
func unquoteGitPath(p string) string {
	if strings.HasPrefix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}

// sectionPaths are all the paths a file section might be about, over-approximated (Security's
// #3555 review): both sides of the "diff --git" header (every possible split of an unquoted
// one, since a path can itself contain " b/"), the rename and copy lines, and the ---/+++
// lines. A section is hidden when any of them is.
func sectionPaths(sec []byte) []string {
	var out []string
	add := func(p string) {
		p = unquoteGitPath(strings.TrimSpace(p))
		for _, pre := range []string{"a/", "b/"} {
			p = strings.TrimPrefix(p, pre)
		}
		if p != "" && p != "/dev/null" {
			out = append(out, p)
		}
	}
	lines := strings.Split(string(sec), "\n")
	header := strings.TrimPrefix(lines[0], "diff --git ")
	if strings.HasPrefix(header, `"`) {
		if a, err := strconv.QuotedPrefix(header); err == nil {
			add(a)
			add(header[len(a):])
		}
	}
	for k := 0; k < len(header); k++ {
		if strings.HasPrefix(header[k:], " b/") || strings.HasPrefix(header[k:], ` "b/`) {
			add(header[:k])
			add(header[k+1:])
		}
	}
	add(header)
	for _, l := range lines[1:] {
		if strings.HasPrefix(l, "@@") || strings.HasPrefix(l, "GIT binary patch") {
			break
		}
		for _, pre := range []string{"rename from ", "rename to ", "copy from ", "copy to ", "--- ", "+++ "} {
			if strings.HasPrefix(l, pre) {
				add(strings.TrimPrefix(l, pre))
			}
		}
	}
	return out
}

// diffSectionPath is the name a note shows for a (non-hidden) section: the b/ side.
func diffSectionPath(header []byte) string {
	h := strings.TrimSuffix(strings.TrimPrefix(string(header), "diff --git "), "\n")
	if strings.HasPrefix(h, `"`) {
		if a, err := strconv.QuotedPrefix(h); err == nil {
			return strings.TrimPrefix(unquoteGitPath(strings.TrimSpace(h[len(a):])), "b/")
		}
	}
	if i := strings.LastIndex(h, " b/"); i >= 0 {
		return h[i+3:]
	}
	return strings.TrimPrefix(h, "a/")
}

// filterPatch replaces each file section that names a hidden path, is binary, or is over
// diffSectionMax with a one-line note; other sections and any text before the first section
// pass through unchanged.
func filterPatch(diff []byte) []byte {
	var out bytes.Buffer
	sections := splitDiffSections(diff)
	for _, sec := range sections {
		if !bytes.HasPrefix(sec, []byte("diff --git ")) {
			out.Write(sec) // text before the first section (notes)
			continue
		}
		nl := bytes.IndexByte(sec, '\n')
		if nl < 0 {
			nl = len(sec)
		}
		path := diffSectionPath(sec[:nl])
		hidden := false
		for _, p := range sectionPaths(sec) {
			if diffPathHidden(p) {
				hidden = true
				break
			}
		}
		switch {
		case hidden:
			// No name: the name of a secret or personal file is itself information.
			out.Write(diffNote("changes to a sensitive file not shown"))
		case bytes.Contains(sec, []byte("\nGIT binary patch\n")) || bytes.Contains(sec, []byte("\nBinary files ")):
			out.Write(diffNote("changes to %q not shown (binary file)", path))
		case len(sec) > diffSectionMax:
			out.Write(diffNote("changes to %q not shown (%d bytes, over 256 KiB)", path, len(sec)))
		default:
			out.Write(sec)
		}
	}
	return out.Bytes()
}

// splitDiffSections splits a patch at each line starting "diff --git ".
func splitDiffSections(diff []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(diff); {
		nl := bytes.IndexByte(diff[i:], '\n')
		lineEnd := len(diff)
		if nl >= 0 {
			lineEnd = i + nl + 1
		}
		if i > start && bytes.HasPrefix(diff[i:], []byte("diff --git ")) {
			out = append(out, diff[start:i])
			start = i
		}
		i = lineEnd
	}
	if start < len(diff) {
		out = append(out, diff[start:])
	}
	return out
}

// capDiff cuts diff at the last line end at or before max, with a note saying so.
func capDiff(diff []byte, max int) []byte {
	if len(diff) <= max {
		return diff
	}
	cut := bytes.LastIndexByte(diff[:max], '\n') + 1
	out := append([]byte{}, diff[:cut]...)
	return append(out, diffNote("diff cut at %d MiB (%d bytes in all); the rest stays in the working directory", max>>20, len(diff))...)
}

// safeDiff is a stored diff as it may leave this computer through Coral Hub: filtered and
// capped. Applied at serve time, so diffs stored before #3555 are covered too.
func safeDiff(diff []byte) []byte {
	return capDiff(filterPatch(diff), diffPersistMax)
}

// generatePersistedDiff is generateChangesDiff for a diff Coral stores or sends through
// Coral Hub: tracked changes are filtered (filterPatch), and an untracked file that is
// hidden, binary or over 256 KiB is never read into a patch, only noted; the whole is capped.
func (h *SessionsHandler) generatePersistedDiff(ctx context.Context, workdir string) ([]byte, error) {
	base := gitutil.GetDiffBase(ctx, workdir, h.getDiffMode(ctx))
	tracked, err := gitOutput(ctx, workdir, "diff", "--binary", base, "--")
	if err != nil {
		return nil, err
	}
	diff := filterPatch(tracked)
	untracked, err := gitOutput(ctx, workdir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for _, raw := range bytes.Split(untracked, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		rel := string(raw)
		if len(diff) > 0 && diff[len(diff)-1] != '\n' {
			diff = append(diff, '\n')
		}
		if diffPathHidden(rel) {
			diff = append(diff, diffNote("a new sensitive file not shown")...)
			continue
		}
		fi, err := os.Lstat(filepath.Join(workdir, rel))
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if fi.Size() > diffSectionMax {
			diff = append(diff, diffNote("new file %q, %d bytes, not stored", rel, fi.Size())...)
			continue
		}
		if isBinaryFile(filepath.Join(workdir, rel)) {
			diff = append(diff, diffNote("new file %q, %d bytes, not stored (binary)", rel, fi.Size())...)
			continue
		}
		fileDiff, ferr := exec.CommandContext(ctx, "git", "-C", workdir, "diff", "--no-index", "--", os.DevNull, rel).Output()
		if ferr != nil {
			if exitErr, ok := ferr.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
				return nil, fmt.Errorf("diff untracked file %q: %w", rel, ferr)
			}
		}
		diff = append(diff, fileDiff...)
		if len(diff) > diffPersistMax {
			break // capped below anyway
		}
	}
	return capDiff(diff, diffPersistMax), nil
}

// isBinaryFile: a NUL in the first 8000 bytes, git's own rule.
func isBinaryFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	buf := make([]byte, 8000)
	n, _ := io.ReadFull(f, buf)
	return bytes.IndexByte(buf[:n], 0) >= 0
}

// gitOutput runs git in workdir; a failure carries git's own message.
func gitOutput(ctx context.Context, workdir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", workdir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
