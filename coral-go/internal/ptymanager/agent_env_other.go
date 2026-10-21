//go:build !windows

package ptymanager

func platformAgentEnv() []string { return nil }
