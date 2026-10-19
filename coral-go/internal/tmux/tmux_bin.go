package tmux

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// resolveTmuxBin returns the path used to invoke tmux, re-checking the
// filesystem if the cached binary isn't reachable. This lets a tmux that
// was installed after the server started be picked up on the next command,
// rather than requiring a restart.
func (c *Client) resolveTmuxBin() string {
	c.tmuxBinMu.Lock()
	defer c.tmuxBinMu.Unlock()
	original := c.TmuxBin
	// Absolute path that still exists — keep using it.
	if filepath.IsAbs(c.TmuxBin) {
		_, err := os.Stat(c.TmuxBin)
		if err == nil {
			return c.TmuxBin
		}
		log.Printf("[tmux] cached absolute tmux path is not usable: path=%q err=%v", c.TmuxBin, err)
	} else if _, err := exec.LookPath(c.TmuxBin); err == nil {
		return c.TmuxBin
	} else {
		log.Printf("[tmux] cached tmux binary not found on PATH: bin=%q path=%q err=%v", c.TmuxBin, os.Getenv("PATH"), err)
	}
	// Cached path no longer works — re-search common locations.
	if p, ok := IsAvailable(); ok {
		c.TmuxBin = p
		if p != original {
			log.Printf("[tmux] resolved tmux binary: previous=%q resolved=%q path=%q", original, p, os.Getenv("PATH"))
		}
		return p
	}
	log.Printf("[tmux] tmux discovery failed; using unresolved binary: bin=%q path=%q shell=%q coral_tmux_bin=%q", c.TmuxBin, os.Getenv("PATH"), os.Getenv("SHELL"), os.Getenv("CORAL_TMUX_BIN"))
	return c.TmuxBin
}

// commonTmuxPaths are checked when tmux is not on PATH (native app bundles
// may not inherit a shell PATH that includes Homebrew).
var commonTmuxPaths = []string{
	"/opt/homebrew/bin/tmux",
	"/usr/local/bin/tmux",
	"/usr/bin/tmux",
	"/opt/local/bin/tmux",
	"/nix/var/nix/profiles/default/bin/tmux",
}

// IsAvailable reports whether tmux can be found on PATH or in a common
// install location. The returned path is the resolved binary (or "tmux"
// if it was found on PATH).
//
// Order: an explicit CORAL_TMUX_BIN override, then the tmux bundled with the
// macOS app (next to the running executable), then PATH, common install
// locations and a login shell lookup.
func IsAvailable() (string, bool) {
	if p := tmuxFromEnv(); p != "" {
		log.Printf("[tmux] discovery found tmux via CORAL_TMUX_BIN: %s", p)
		return p, true
	}
	if p, _ := bundledTmux(); p != "" {
		log.Printf("[tmux] discovery found bundled tmux: %s", p)
		return p, true
	}
	if p, err := exec.LookPath("tmux"); err == nil {
		log.Printf("[tmux] discovery found tmux on PATH: %s", p)
		return p, true
	}
	for _, p := range commonTmuxPaths {
		if executableFileExists(p) {
			log.Printf("[tmux] discovery found tmux in common path: %s", p)
			return p, true
		}
	}
	if p := tmuxFromLoginShell(); p != "" {
		log.Printf("[tmux] discovery found tmux via login shell: %s", p)
		return p, true
	}
	log.Printf("[tmux] discovery failed: path=%q shell=%q coral_tmux_bin=%q common_paths=%v", os.Getenv("PATH"), os.Getenv("SHELL"), os.Getenv("CORAL_TMUX_BIN"), commonTmuxPaths)
	return "", false
}

func tmuxFromEnv() string {
	p := strings.TrimSpace(os.Getenv("CORAL_TMUX_BIN"))
	if p == "" {
		return ""
	}
	if resolved, err := exec.LookPath(p); err == nil {
		return resolved
	}
	if executableFileExists(p) {
		return p
	}
	log.Printf("[tmux] CORAL_TMUX_BIN is set but not executable: %q", p)
	return ""
}

func tmuxFromLoginShell() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	shells := []string{}
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		shells = append(shells, shell)
	}
	shells = append(shells, "/bin/zsh", "/bin/bash", "/bin/sh")

	seen := make(map[string]bool, len(shells))
	for _, shell := range shells {
		if shell == "" || seen[shell] || !executableFileExists(shell) {
			continue
		}
		seen[shell] = true

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		out, err := exec.CommandContext(ctx, shell, "-lc", "command -v tmux").Output()
		cancel()
		if err != nil {
			log.Printf("[tmux] login shell tmux lookup failed: shell=%q err=%v", shell, err)
			continue
		}
		p := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		if p == "" {
			continue
		}
		if resolved, err := exec.LookPath(p); err == nil {
			return resolved
		}
		if executableFileExists(p) {
			return p
		}
	}
	return ""
}

func executableFileExists(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Mode()&0111 != 0
}
