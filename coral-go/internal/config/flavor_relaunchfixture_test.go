//go:build relaunchfixture

package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #5452: a relaunchfixture build says so in every version it reports.
func TestAFixtureBuildSaysSoInItsVersion(t *testing.T) {
	assert.Equal(t, "+relaunchfixture", BuildFlavor)
	assert.Equal(t, " +relaunchfixture", FlavorNote())
	assert.True(t, strings.HasSuffix(ReportedVersion(), "+relaunchfixture"), ReportedVersion())
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "1.4.134"
	assert.Equal(t, "1.4.134+relaunchfixture", ReportedVersion())
}
