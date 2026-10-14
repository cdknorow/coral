//go:build linux

package agentprio

func rawToNice(v int) int { return 20 - v }
