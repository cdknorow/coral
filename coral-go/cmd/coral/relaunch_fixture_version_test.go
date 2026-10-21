package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/config"
)

// coral --version carries the test seam's flavor exactly when the build has it (#5452, Security's condition 3): a shipped binary never does.
func TestVersionOutputSaysRelaunchfixtureOnlyInAFixtureBuild(t *testing.T) {
	old := config.Version
	t.Cleanup(func() { config.Version = old })
	for _, v := range []string{"", "1.4.134"} {
		config.Version = v
		var b bytes.Buffer
		printVersion(&b)
		assert.Equal(t, config.BuildFlavor != "", strings.Contains(b.String(), "+relaunchfixture"), "%q: %q", v, b.String())
		assert.True(t, strings.HasPrefix(b.String(), "coral "), b.String())
	}
}
