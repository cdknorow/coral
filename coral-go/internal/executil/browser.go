package executil

import (
	"os/exec"
	"runtime"
)

func OpenBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
		HideWindow(cmd)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}
