package verify

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// versionRe is a release version: three numbers, no suffix (a promoted release is never a prerelease). The same pattern the hub validates.
var versionRe = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,4}$`)

// ParseVersion reads "1.4.123" (a leading v is not accepted: callers strip a tag's).
func ParseVersion(s string) ([3]int, error) {
	var out [3]int
	if !versionRe.MatchString(s) {
		return out, fmt.Errorf("version %q is not X.Y.Z", s)
	}
	for i, p := range strings.Split(s, ".") {
		n, _ := strconv.Atoi(p)
		out[i] = n
	}
	return out, nil
}

// CompareVersions is -1, 0 or 1; both must parse.
func CompareVersions(a, b string) (int, error) {
	x, err := ParseVersion(a)
	if err != nil {
		return 0, err
	}
	y, err := ParseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := range x {
		switch {
		case x[i] < y[i]:
			return -1, nil
		case x[i] > y[i]:
			return 1, nil
		}
	}
	return 0, nil
}
