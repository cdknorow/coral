package tmux

import (
	"strings"
	"testing"
)

func TestInstallCommandFor(t *testing.T) {
	only := func(names ...string) func(string) bool {
		return func(n string) bool {
			for _, x := range names {
				if x == n {
					return true
				}
			}
			return false
		}
	}
	for _, c := range []struct {
		goos string
		has  func(string) bool
		want string
	}{
		{"darwin", only("apt"), "brew install tmux"},
		{"linux", only("apt", "dnf"), "sudo apt install tmux"},
		{"linux", only("dnf", "zypper"), "sudo dnf install tmux"},
		{"linux", only("pacman"), "sudo pacman -S tmux"},
		{"linux", only("zypper"), "sudo zypper install tmux"},
		{"linux", only(), "install tmux with your distro's package manager"},
		{"windows", only("apt"), "install tmux for your platform"},
	} {
		if got := installCommandFor(c.goos, c.has); got != c.want {
			t.Errorf("%s: got %q, want %q", c.goos, got, c.want)
		}
	}
}

func TestMissingMessageNamesTheCommandAndNeverTheWrongOS(t *testing.T) {
	m := MissingMessage()
	if !strings.Contains(m, InstallCommand()) || !strings.Contains(m, "required") {
		t.Fatalf("message %q", m)
	}
	if InstallCommand() != "brew install tmux" && strings.Contains(m, "brew") {
		t.Fatalf("a message on another OS names brew: %q", m)
	}
}
