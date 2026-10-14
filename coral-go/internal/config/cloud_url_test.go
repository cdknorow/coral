package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The Coral Hub origin (#3221): unset means the tier's default (app.coralai.ai in release
// builds, none in dev); an explicit empty value or "off" turns the connector off.
func TestCloudURLSetting(t *testing.T) {
	assert.Equal(t, TierDefaultCloudURL, CloudURLSetting("", false), "unset: the tier default")
	for _, off := range []string{"", " ", "off", "OFF", "none", "0", "false"} {
		assert.Equal(t, "", CloudURLSetting(off, true), "%q turns it off", off)
	}
	assert.Equal(t, "https://hub.example.com", CloudURLSetting(" https://hub.example.com ", true))
}

func TestTierDefaultCloudURL(t *testing.T) {
	switch TierName {
	case "prod", "staging", "beta":
		assert.Equal(t, "https://app.coralai.ai", TierDefaultCloudURL)
	case "dev", "dropboxers":
		assert.Equal(t, "", TierDefaultCloudURL)
	}
}

func TestLoadUsesTheTierDefaultUnlessCORAL_CLOUD_URLIsSet(t *testing.T) {
	t.Setenv("CORAL_DATA_DIR", t.TempDir())
	t.Setenv("CORAL_CLOUD_URL", "off")
	assert.Equal(t, "", Load("").CloudURL)
	t.Setenv("CORAL_CLOUD_URL", "https://hub.example.com")
	assert.Equal(t, "https://hub.example.com", Load("").CloudURL)
}
