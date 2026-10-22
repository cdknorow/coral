package tmux

import (
	"os/exec"
	"runtime"
)

// InstallCommand is the one command that installs tmux on this computer: brew on macOS, and on Linux the package manager that is here (apt, dnf, pacman, zypper). Coral only shows it, to the
// banner, the launch dialogs and the error a launch gives when tmux is missing (#6046); it never runs it.
func InstallCommand() string {
	return installCommandFor(runtime.GOOS, func(name string) bool { _, err := exec.LookPath(name); return err == nil })
}

func installCommandFor(goos string, has func(string) bool) string {
	switch goos {
	case "darwin":
		return "brew install tmux"
	case "linux":
		for _, m := range []struct{ bin, cmd string }{
			{"apt", "sudo apt install tmux"},
			{"dnf", "sudo dnf install tmux"},
			{"pacman", "sudo pacman -S tmux"},
			{"zypper", "sudo zypper install tmux"},
		} {
			if has(m.bin) {
				return m.cmd
			}
		}
		return "install tmux with your distro's package manager"
	default:
		return "install tmux for your platform"
	}
}

// MissingMessage is what a launch says when tmux is not there: what is needed and the one command for this computer.
func MissingMessage() string {
	return "tmux is required to launch agents but was not found on this system. Install it with: " + InstallCommand() + ", then try again"
}
