//go:build windows

package ptymanager

import "github.com/subgentic/coral/internal/executil"

// platformAgentEnv: NoDefaultCurrentDirectoryInExePath=1 makes cmd.exe (a Claude Code hook, Codex's shell tool
// and every other `cmd /c` the agent runs) stop looking in the current folder for a program: without it a
// planted git.exe or node.exe in the repository an agent works in runs in place of the real one (#25 G1B).
func platformAgentEnv() []string { return []string{executil.NoCwdSearchEnv} }
