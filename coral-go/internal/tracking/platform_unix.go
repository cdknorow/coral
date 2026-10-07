//go:build !windows

package tracking

import (
	"os"
	"runtime"
	"strings"
	"sync"
)

var (
	platformOnce  sync.Once
	platformValue string
)

func clientPlatform() string {
	platformOnce.Do(func() {
		platformValue = runtime.GOOS
		if runtime.GOOS == "linux" {
			if isWSL2() {
				platformValue = "wsl2"
			}
		}
	})
	return platformValue
}

func isWSL2() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(data))
	return strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl")
}
