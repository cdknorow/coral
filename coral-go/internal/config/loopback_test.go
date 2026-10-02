package config

import "testing"

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1": true, "127.0.0.2": true, "::1": true, "[::1]": true, "localhost": true, "LOCALHOST": true,
		"0.0.0.0": false, "::": false, "": false, "192.168.1.5": false, "10.0.0.1": false, "example.com": false,
	} {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}
