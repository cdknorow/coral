//go:build !relaunchfixture

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
)

// A normal build has nothing to check: the seam's startup check changes no setting and never refuses.
func TestADefaultBuildsFixtureCheckIsANoOp(t *testing.T) {
	cfg := &config.Config{Host: "0.0.0.0", CloudURL: "https://app.coralhub.dev"}
	remote := true
	require.NoError(t, applyRelaunchFixtureBuild(cfg, "0.0.0.0", &remote, false))
	assert.Equal(t, "0.0.0.0", cfg.Host)
	assert.Equal(t, "https://app.coralhub.dev", cfg.CloudURL)
}
