package routes

import "testing"

func TestTmuxInstallCommandFor(t *testing.T) {
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
	cases := []struct {
		goos string
		has  func(string) bool
		want string
	}{
		{"darwin", only(), "brew install tmux"},
		{"linux", only("apt", "dnf"), "sudo apt install tmux"},
		{"linux", only("dnf"), "sudo dnf install tmux"},
		{"linux", only(), "sudo apt install tmux  (or your distro's package manager)"},
		{"windows", only("apt"), "install tmux for your platform"},
	}
	for _, c := range cases {
		if got := tmuxInstallCommandFor(c.goos, c.has); got != c.want {
			t.Errorf("%s: got %q, want %q", c.goos, got, c.want)
		}
	}
}
