package config

import (
	"fmt"
	"regexp"
	"runtime/debug"
	"strconv"
	"sync"
)

// DevBase is the release tag a non-release build was made from (for example "v1.4.1"),
// set by the Makefile via ldflags. Release builds set Version and ignore it.
var DevBase string

var (
	reportedOnce sync.Once
	reported     string
)

// ReportedVersion is the version this build tells other machines about: /api/health and
// the Cloud Hub tunnel Hello. A release build reports Version. A dev or local build keeps
// Version empty, so it never offers itself an update, but still reports a semver stamp,
// "<next patch>-dev+<commit>[.dirty]", which sorts after the release it was built from
// and before the next one. Without DevBase (a plain go build) the base is 0.0.0.
func ReportedVersion() string {
	if Version != "" {
		return Version + BuildFlavor
	}
	reportedOnce.Do(func() {
		info, _ := debug.ReadBuildInfo()
		reported = devVersion(DevBase, info) + BuildFlavor
	})
	return reported
}

var (
	releaseTag = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)
	hexRev     = regexp.MustCompile(`^[0-9a-f]+$`)
)

// devVersion builds the dev stamp from a release tag and the binary's build info.
func devVersion(base string, info *debug.BuildInfo) string {
	next := "0.0.0"
	if m := releaseTag.FindStringSubmatch(base); m != nil {
		patch, err := strconv.Atoi(m[3])
		if err == nil {
			next = fmt.Sprintf("%s.%s.%d", m[1], m[2], patch+1)
		}
	}
	v := next + "-dev"
	if info == nil {
		return v
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(rev) > 8 {
		rev = rev[:8]
	}
	if !hexRev.MatchString(rev) {
		return v
	}
	v += "+" + rev
	if dirty {
		v += ".dirty"
	}
	return v
}

// FlavorNote is the build flavor as a word for a log line: "" in a normal build, " +relaunchfixture" in a test-seam build.
func FlavorNote() string {
	if BuildFlavor == "" {
		return ""
	}
	return " " + BuildFlavor
}
