package secfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/subgentic/coral/internal/linktest"
)

// #3920: OpenNoFollow never reads, writes, appends to or truncates the target of a link planted
// at the path (a symbolic link; on Windows also a junction), and works on a plain file.
func TestOpenNoFollowRefusesPlantedLinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coral.log")
	linktest.Each(t, path, func(t *testing.T, p linktest.Planted) {
		for name, flag := range map[string]int{
			"read":   os.O_RDONLY,
			"append": os.O_CREATE | os.O_WRONLY | os.O_APPEND,
			"trunc":  os.O_CREATE | os.O_WRONLY | os.O_TRUNC,
			"rdwr":   os.O_RDWR,
		} {
			if p.Kind == "hardlink" && name == "read" {
				continue // reading a second name is harmless: the same user could read the file
			}
			f, err := OpenNoFollow(path, flag, 0o600)
			if err == nil {
				f.Close()
				t.Errorf("%s: %s opened through the link", p.Kind, name)
			}
		}
	})

	// A plain file: created, appended to, truncated, read.
	f, err := OpenNoFollow(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("one\n")
	f.Close()
	f, err = OpenNoFollow(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("two\n")
	f.Close()
	if b, _ := os.ReadFile(path); string(b) != "one\ntwo\n" {
		t.Fatalf("appended content = %q", b)
	}
	f, err = OpenNoFollow(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("three\n")
	f.Close()
	if b, _ := os.ReadFile(path); string(b) != "three\n" {
		t.Fatalf("truncated content = %q", b)
	}
	if runtime.GOOS == "windows" {
		// A second name (a hard link) is refused for a write: what's written would land in the
		// other file too.
		other := path + ".other"
		if err := os.Link(path, other); err != nil {
			t.Fatal(err)
		}
		if f, err := OpenNoFollow(path, os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			f.Close()
			t.Error("a file with a second name was opened for writing")
		}
	}
}
