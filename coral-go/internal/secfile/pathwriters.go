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
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	w := &walker{uid: uid, who: map[string]bool{}, acl: map[string][]string{}}
	if u, err := user.LookupId(fmt.Sprint(uid)); err == nil {
		w.name = u.Username
	}
	if err := w.walk(abs); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(w.who))
	for s := range w.who {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

// readACL is pathACLWriters (a variable so a test can make the access list unreadable or full: macOS and Linux read theirs differently).
var readACL = pathACLWriters

type walker struct {
	uid  int
	name string // the user's login name, for access-list entries
	who  map[string]bool
	acl  map[string][]string // path -> who its access list lets write (cached)
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
			w.who[fmt.Sprintf("%s belongs to another user", next)] = true
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
			w.who[fmt.Sprintf("its group can write in %s", dir)] = true
		}
		if m&0o002 != 0 {
			w.who[fmt.Sprintf("everyone can write in %s", dir)] = true
		}
	}
	return w.aclWriters(dir)
}

// entryWriters records who can change the file or folder at path itself (mode bits and access list).
func (w *walker) entryWriters(path string, fi os.FileInfo) error {
	if m := fi.Mode().Perm(); m&0o020 != 0 {
		w.who[fmt.Sprintf("the group of %s can write it", path)] = true
	}
	if fi.Mode().Perm()&0o002 != 0 {
		w.who[fmt.Sprintf("everyone can write %s", path)] = true
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
		w.who[fmt.Sprintf("%s (access list of %s)", s, path)] = true
	}
	return nil
}

var errNoACL = errors.New("no access list")
