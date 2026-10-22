//go:build !windows

package secfile

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// PathWriters names everyone besides the user and root who could change what a program started by path would run: it walks the path the way the system does, hop by hop, and
// at every step asks who could replace what is there. That covers each folder passed through (its owner, a group or other write bit, an access list that grants a write),
// each file and link (the same), and EVERY symbolic link on the way: a link's target is walked in turn, so a link into a folder anyone can write, a link in the middle of the
// path, and a link whose own folder anyone can write are all found, not only the final target. A folder that others can write but that is root's and sticky (like /tmp) only
// counts for an entry that is not the user's or root's. An empty list is a path only the user and root can change; the error is for a path that could not be examined (a
// missing step, a loop of links, an access list that could not be read), and the caller treats it as unsafe. The last step is checked as a file too. A change made after the
// check is not covered: the caller runs the program by this path afterwards.
func PathWriters(path string) ([]string, error) { return PathWritersFor(path, os.Geteuid()) }

// PathWritersFor is PathWriters for a program that runs as uid (a test can be someone else: a file owned by another user can't be made without root).
func PathWritersFor(path string, uid int) ([]string, error) {
	ws, err := pathWriters(path, uid, nil)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Text
	}
	return out, nil
}

// Writer is one way someone else could change a path: what it says, the folder or file it is about, and whether it is only a group's write bit.
type Writer struct {
	Text  string
	Path  string
	Group bool // only the group write bit (chmod g-w on Path removes it)
}

// PathWritersTrustingGroup is PathWriters for the current user that lets a group write bit pass when it grants nobody else anything: the folder or file is the user's own and groupOK
// says the group is the user's alone (its gid; an error is a refusal). Every other finding stays: another owner, an other write bit, an access list, a group that is not the user's alone.
func PathWritersTrustingGroup(path string, groupOK func(gid uint32) (bool, error)) ([]Writer, error) {
	return pathWriters(path, os.Geteuid(), groupOK)
}

func pathWriters(path string, uid int, groupOK func(gid uint32) (bool, error)) ([]Writer, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	w := &walker{uid: uid, who: map[string]Writer{}, acl: map[string][]string{}, groupOK: groupOK}
	if u, err := user.LookupId(fmt.Sprint(uid)); err == nil {
		w.name = u.Username
	}
	if err := w.walk(abs); err != nil {
		return nil, err
	}
	out := make([]Writer, 0, len(w.who))
	for _, v := range w.who {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Text < out[j].Text })
	return out, nil
}

// readACL is pathACLWriters (a variable so a test can make the access list unreadable or full: macOS and Linux read theirs differently).
var readACL = pathACLWriters

type walker struct {
	uid     int
	name    string                         // the user's login name, for access-list entries
	groupOK func(gid uint32) (bool, error) // nil: a group write bit always counts
	who     map[string]Writer
	acl     map[string][]string // path -> who its access list lets write (cached)
}

func (w *walker) trusted(owner uint32) bool { return owner == 0 || int(owner) == w.uid }

func split(p string) []string {
	var out []string
	for _, c := range strings.Split(p, "/") {
		if c != "" && c != "." {
			out = append(out, c)
		}
	}
	return out
}

func (w *walker) walk(abs string) error {
	rest := split(abs)
	cur := "/"
	hops := 0
	curInfo, err := os.Lstat(cur)
	if err != nil {
		return err
	}
	for len(rest) > 0 {
		comp := rest[0]
		rest = rest[1:]
		if comp == ".." {
			cur = filepath.Dir(cur)
			if curInfo, err = os.Lstat(cur); err != nil {
				return err
			}
			continue
		}
		next := filepath.Join(cur, comp)
		fi, err := os.Lstat(next)
		if err != nil {
			return err
		}
		// Who could replace this entry: whoever can change the folder it is in (cur), unless that folder is root's and sticky and the entry is the user's or root's.
		if err := w.folderWriters(cur, curInfo, fi); err != nil {
			return err
		}
		sys, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("the owner of %s can't be read", next)
		}
		if !w.trusted(sys.Uid) {
			w.add(fmt.Sprintf("%s belongs to another user", next), next, false)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if hops++; hops > 40 {
				return fmt.Errorf("too many links on the way to %s", abs)
			}
			target, err := os.Readlink(next)
			if err != nil {
				return err
			}
			if filepath.IsAbs(target) {
				cur = "/"
				if curInfo, err = os.Lstat(cur); err != nil {
					return err
				}
			}
			rest = append(split(target), rest...)
			continue
		}
		// A file at the end of the path must not be writable by others; a folder on the way is judged when an entry in it is passed (folderWriters), so a sticky folder of root's
		// (/tmp) is not reported for its own mode.
		if len(rest) == 0 && !fi.IsDir() {
			if err := w.entryWriters(next, fi); err != nil {
				return err
			}
		} else if fi.IsDir() {
			if err := w.aclWriters(next); err != nil {
				return err
			}
		}
		cur, curInfo = next, fi
	}
	return nil
}

// folderWriters records who can add, remove or rename entries in dir (info) that is passed through on the way to entry.
func (w *walker) folderWriters(dir string, info, entry os.FileInfo) error {
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() {
		return nil // a path through a file fails at the next step
	}
	esys, ok := entry.Sys().(*syscall.Stat_t)
	stickyProtects := ok && info.Mode()&os.ModeSticky != 0 && sys.Uid == 0 && w.trusted(esys.Uid)
	if m := info.Mode().Perm(); m&0o022 != 0 && !stickyProtects {
		if m&0o020 != 0 {
			trusted, err := w.groupIsMine(sys)
			if err != nil {
				return err
			}
			if !trusted {
				w.add(fmt.Sprintf("its group (%s) can write in %s", groupName(sys.Gid), dir), dir, true)
			}
		}
		if m&0o002 != 0 {
			w.add(fmt.Sprintf("everyone can write in %s", dir), dir, false)
		}
	}
	return w.aclWriters(dir)
}

// entryWriters records who can change the file or folder at path itself (mode bits and access list).
func (w *walker) entryWriters(path string, fi os.FileInfo) error {
	if m := fi.Mode().Perm(); m&0o020 != 0 {
		sys, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("the group of %s can't be read", path)
		}
		trusted, err := w.groupIsMine(sys)
		if err != nil {
			return err
		}
		if !trusted {
			w.add(fmt.Sprintf("the group (%s) of %s can write it", groupName(sys.Gid), path), path, true)
		}
	}
	if fi.Mode().Perm()&0o002 != 0 {
		w.add(fmt.Sprintf("everyone can write %s", path), path, false)
	}
	return w.aclWriters(path)
}

func (w *walker) aclWriters(path string) error {
	if _, done := w.acl[path]; done {
		return nil
	}
	list, err := readACL(path, w.uid, w.name)
	if err != nil {
		return fmt.Errorf("the access list of %s could not be read: %w", path, err)
	}
	w.acl[path] = list
	for _, s := range list {
		w.add(fmt.Sprintf("%s (access list of %s)", s, path), path, false)
	}
	return nil
}

var errNoACL = errors.New("no access list")

func (w *walker) add(text, path string, group bool) {
	if prev, ok := w.who[text]; ok && !prev.Group {
		group = false
	}
	w.who[text] = Writer{Text: text, Path: path, Group: group}
}

// groupIsMine says whether a group write bit on an entry grants nobody else anything: the entry is the user's own and the caller's check calls the group the user's alone. A walker with no
// check never trusts one.
func (w *walker) groupIsMine(sys *syscall.Stat_t) (bool, error) {
	if w.groupOK == nil || int(sys.Uid) != w.uid {
		return false, nil
	}
	ok, err := w.groupOK(sys.Gid)
	if err != nil {
		return false, fmt.Errorf("the group %s could not be checked: %w", groupName(sys.Gid), err)
	}
	return ok, nil
}

// groupName is the group's name for a message, its number when it has none.
func groupName(gid uint32) string {
	if g, err := user.LookupGroupId(fmt.Sprint(gid)); err == nil && g.Name != "" {
		return g.Name
	}
	return fmt.Sprint(gid)
}
