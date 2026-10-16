//go:build windows

package linktest

import (
	"os"
	"os/exec"
	"testing"
)

func kinds() []string { return []string{"symlink", "junction"} }

func plant(t *testing.T, kind, path string) Planted {
	t.Helper()
	if kind == "symlink" {
		return plantSymlink(t, path)
	}
	target := path + ".junction-target"
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", path, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v: %s", err, out)
	}
	return Planted{Kind: "junction", Target: target, Folder: true}
}
