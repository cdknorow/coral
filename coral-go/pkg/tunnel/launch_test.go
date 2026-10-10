package tunnel

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsLaunchRoute(t *testing.T) {
	yes := [][2]string{
		{"POST", "/api/sessions/launch"}, {"POST", "/api/sessions/launch-team"}, {"POST", "/api/sessions/launch/"},
		{"POST", "/api/teams/detail/my team/resurrect"}, {"POST", "/api/sessions/live/a/restart"},
		{"POST", "/api/sessions/live/a/resume"}, {"POST", "/api/sessions/live/abc/wake"}, {"POST", "/api/sessions/live/wake-all"},
		{"POST", "/api/sessions/live/team/t/wake"}, {"POST", "/api/sessions/live/team/t/reset"}, {"POST", "/api/tasks/run"},
		{"POST", "/api/scheduled/jobs"}, {"PUT", "/api/scheduled/jobs/1"}, {"POST", "/api/scheduled/jobs/1/toggle"},
		{"POST", "/api/workflows/by-name/x/trigger"}, {"POST", "/api/workflows"}, {"DELETE", "/api/workflows/3"}, {"POST", "/api/teams/generate"},
	}
	for _, c := range yes {
		assert.True(t, IsLaunchRoute(c[0], c[1]), c[0]+" "+c[1])
	}
	no := [][2]string{
		{"GET", "/api/sessions/launch"}, {"POST", "/api/sessions/live/a/send"}, {"POST", "/api/sessions/live/a/keys"},
		{"GET", "/api/scheduled/jobs"}, {"GET", "/api/workflows"}, {"GET", "/api/sessions/live"}, {"POST", "/api/sessions/live/a/kill"},
		{"POST", "/api/sessions/live/a/resize"}, {"GET", "/ws/terminal/a"}, {"POST", "/api/sessions/launchx"}, {"POST", "/api/workflowsx"},
		{http.MethodOptions, "/api/scheduled/jobs"},
	}
	for _, c := range no {
		assert.False(t, IsLaunchRoute(c[0], c[1]), c[0]+" "+c[1])
	}
	assert.Equal(t, "launch_team", LaunchClass("POST", "/api/sessions/launch-team"))
}
