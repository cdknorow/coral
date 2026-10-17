// Package updatecheck is the one update check the server and the tray share (#3660, update
// audit items 4 and 5). It asks GitHub for the latest release and offers it only when it is
// strictly newer than this build:
//   - only an HTTP 200 reply counts (a rate-limit 403 is an error, not "no update");
//   - the body is read up to MaxBody;
//   - tag_name must look like a version (v1.2.3 or v1.2.3-rc1) before it reaches a toast, a
//     dismissed key or an OS notification; anything else is an error;
//   - versions compare as semver, so a newer, dev or beta build is never told to move to an
//     older release.
//
// Coral never downloads or runs an update: the caller shows the releases page.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// ReleasesAPI is GitHub's latest-release endpoint for Coral (prereleases excluded).
	ReleasesAPI = "https://api.github.com/repos/cdknorow/coral/releases/latest"
	// ReleasesURL is the page the update notice links to.
	ReleasesURL = "https://github.com/cdknorow/coral/releases"
	// MaxBody caps the reply read: a release object is a few KB.
	MaxBody = 64 << 10
	// Timeout bounds the whole request.
	Timeout = 5 * time.Second
)

// tagShape is a release tag: v, three numbers, an optional prerelease of letters, digits and dots.
var tagShape = regexp.MustCompile(`^v?(\d{1,9})\.(\d{1,9})\.(\d{1,9})(?:-([0-9A-Za-z.]{1,40}))?$`)

// ErrBadTag is a reply whose tag_name doesn't look like a version.
var ErrBadTag = errors.New("updatecheck: the release tag is not a version")

// Latest returns the latest release's version (without the "v"), from apiURL (ReleasesAPI;
// tests pass their own). The request is anonymous: it reads a public endpoint, and no credential
// from the environment (GITHUB_TOKEN, which is often a developer's token with write rights) is
// attached to it (#4133).
func Latest(ctx context.Context, apiURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("updatecheck: GitHub answered %d", resp.StatusCode)
	}
	var data struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxBody)).Decode(&data); err != nil {
		return "", fmt.Errorf("updatecheck: %w", err)
	}
	if !tagShape.MatchString(data.TagName) {
		return "", ErrBadTag
	}
	return strings.TrimPrefix(data.TagName, "v"), nil
}

// Newer reports whether latest is strictly newer than current, both versions with or without a
// "v". A current that isn't a version ("dev", "") is never offered an update.
func Newer(latest, current string) bool {
	l, ok1 := parse(latest)
	c, ok2 := parse(current)
	return ok1 && ok2 && compare(l, c) > 0
}

// Available is Latest, then Newer against current: the version to offer, or "".
func Available(ctx context.Context, apiURL, current string) (string, error) {
	latest, err := Latest(ctx, apiURL)
	if err != nil || !Newer(latest, current) {
		return "", err
	}
	return latest, nil
}

type version struct {
	core [3]int
	pre  []string // empty for a release
}

func parse(s string) (version, bool) {
	m := tagShape.FindStringSubmatch(s)
	if m == nil {
		return version{}, false
	}
	var v version
	for i := 0; i < 3; i++ {
		v.core[i], _ = strconv.Atoi(m[i+1])
	}
	if m[4] != "" {
		v.pre = strings.Split(m[4], ".")
	}
	return v, true
}

// compare orders versions as semver: the core numbers, then a release above any prerelease of
// the same core, then prerelease identifiers (numbers below words, numbers by value).
func compare(a, b version) int {
	for i := range a.core {
		if a.core[i] != b.core[i] {
			return cmpInt(a.core[i], b.core[i])
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if xn != yn {
				return cmpInt(xn, yn)
			}
		case xerr == nil:
			return -1
		case yerr == nil:
			return 1
		case x != y:
			return strings.Compare(x, y)
		}
	}
	return cmpInt(len(a.pre), len(b.pre))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
