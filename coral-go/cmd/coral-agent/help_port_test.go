package main

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/config"
)

// The help names this platform's default port (8450 on Windows, 8420 elsewhere), not a fixed 8420 (#282 row 3).
func TestHelpNamesThePlatformDefaultPort(t *testing.T) {
	assert.Contains(t, agentUsageText, "http://localhost:"+strconv.Itoa(config.DefaultPort))
	assert.NotContains(t, agentUsageText, "{{PORT}}")
}
