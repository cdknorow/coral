//go:build !windows && !linux

package agentprio

func rawToNice(v int) int { return v }
