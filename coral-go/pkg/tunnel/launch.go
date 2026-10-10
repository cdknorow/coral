package tunnel

import (
	"net/http"
	"regexp"
	"strings"
)

// launchRoutes are the Coral API routes that start agent processes (directly, or by
// scheduling or waking them). The Cloud Hub refuses them unless the server's "allow
// launching from the web" switch is on, and a connector must refuse them locally for
// the same reason: the hub is not trusted to honour the switch (threat model T-7, P-11).
// Terminal input and chat (send, keys) are deliberately not launch routes.
var launchRoutes = []struct {
	method string // "" matches every method except GET, HEAD and OPTIONS
	re     *regexp.Regexp
}{
	{http.MethodPost, regexp.MustCompile(`^/api/sessions/launch(-team)?$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/teams/detail/[^/]+/resurrect$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/sessions/live/[^/]+/(restart|resume|wake)$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/sessions/live/wake-all$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/sessions/live/team/[^/]+/(wake|reset)$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/teams/generate$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/tasks/run$`)},
	{"", regexp.MustCompile(`^/api/scheduled/`)},
	{"", regexp.MustCompile(`^/api/workflows(/|$)`)},
}

// IsLaunchRoute reports whether method on the decoded path starts, schedules or
// wakes agent processes. Matching is exact on the route shape, not a substring.
func IsLaunchRoute(method, decodedPath string) bool {
	p := strings.TrimSuffix(decodedPath, "/")
	if p == "" {
		p = "/"
	}
	for _, r := range launchRoutes {
		if r.method == "" {
			if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
				continue
			}
		} else if r.method != method {
			continue
		}
		if r.re.MatchString(p) {
			return true
		}
	}
	return false
}

// LaunchClass names the kind of launch for audit rows (never the path or ids).
func LaunchClass(method, decodedPath string) string {
	switch {
	case strings.HasPrefix(decodedPath, "/api/sessions/launch-team"):
		return "launch_team"
	case strings.HasPrefix(decodedPath, "/api/sessions/launch"):
		return "launch"
	case strings.Contains(decodedPath, "/resurrect"):
		return "resurrect_team"
	case strings.Contains(decodedPath, "/wake"):
		return "wake"
	case strings.HasPrefix(decodedPath, "/api/scheduled/"):
		return "schedule"
	case strings.HasPrefix(decodedPath, "/api/workflows"):
		return "workflow"
	}
	return "launch_other"
}
