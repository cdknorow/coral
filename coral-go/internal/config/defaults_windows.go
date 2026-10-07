//go:build windows

package config

// DefaultPort is the default HTTP server port.
// Windows uses 8450 to avoid conflicts with svchost on port 8420.
const DefaultPort = 8450
