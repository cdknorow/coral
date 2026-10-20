package config

import (
	"runtime/debug"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func buildInfo(settings ...string) *debug.BuildInfo {
	info := &debug.BuildInfo{}
	for i := 0; i+1 < len(settings); i += 2 {
		info.Settings = append(info.Settings, debug.BuildSetting{Key: settings[i], Value: settings[i+1]})
	}
	return info
}

func TestDevVersion(t *testing.T) {
	const rev = "8f406daea75d473877f5e9d2bbe6798f77a41ee9"
	for _, tc := range []struct {
		name, base string
		info       *debug.BuildInfo
		want       string
	}{
		{"make dev, clean tree", "v1.4.1", buildInfo("vcs.revision", rev, "vcs.modified", "false"), "1.4.2-dev+8f406dae"},
		{"make dev, uncommitted changes", "v1.4.1", buildInfo("vcs.revision", rev, "vcs.modified", "true"), "1.4.2-dev+8f406dae.dirty"},
		{"tag without v", "1.4.9", buildInfo("vcs.revision", rev), "1.4.10-dev+8f406dae"},
		{"plain go build: no base", "", buildInfo("vcs.revision", rev), "0.0.0-dev+8f406dae"},
		{"suffixed tag is not a release", "v1.4.1-beta", buildInfo("vcs.revision", rev), "0.0.0-dev+8f406dae"},
		{"no VCS info", "v1.4.1", buildInfo(), "1.4.2-dev"},
		{"no build info", "v1.4.1", nil, "1.4.2-dev"},
		{"revision that is not hex is dropped", "v1.4.1", buildInfo("vcs.revision", "abc+def"), "1.4.2-dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, devVersion(tc.base, tc.info))
		})
	}
}

// The stamp must fit what a Cloud Hub enrollment keeps (40 characters) and the tunnel Hello allows (64).
func TestDevVersionFitsEnrollmentAndHello(t *testing.T) {
	v := devVersion("v123.456.789", buildInfo("vcs.revision", "0123456789abcdef0123456789abcdef01234567", "vcs.modified", "true"))
	assert.LessOrEqual(t, len(v), 40, v)
}

func TestReportedVersionPrefersReleaseVersion(t *testing.T) {
	prev := Version
	t.Cleanup(func() { Version = prev })

	Version = "1.4.1"
	assert.Equal(t, "1.4.1"+BuildFlavor, ReportedVersion())

	Version = ""
	// the flavor of a relaunchfixture build (#5452) is the one thing a build adds to it
	assert.Regexp(t, `^\d+\.\d+\.\d+-dev(\+[0-9a-f]{1,8}(\.dirty)?)?$`, strings.TrimSuffix(ReportedVersion(), BuildFlavor))
}
