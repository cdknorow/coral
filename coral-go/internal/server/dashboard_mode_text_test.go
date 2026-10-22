package server

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What the dashboard says each Coral Hub access mode allows, and which mode its dimmed controls name, follow the route table
// (testdata/cloud_routes.golden, what the connector enforces), as TestHubDocModesMatchTheRouteTable does for hub.md (#4526:
// cloud_servers.js MODE_INFO said Interact could kill an agent long after #4274 made it Launch, and the dashboard dimmed Kill
// and Sleep only below Interact).
func TestDashboardModeTextMatchesTheRouteTable(t *testing.T) {
	g, err := os.ReadFile(cloudGolden)
	require.NoError(t, err)
	mode := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(g)), "\n") {
		i := strings.LastIndex(l, " ")
		mode[l[:i]] = l[i+1:]
	}
	read := func(p string) string {
		b, err := os.ReadFile("../../pkg/frontend/" + p)
		require.NoError(t, err)
		return string(b)
	}
	modeOf := func(rs ...string) string {
		t.Helper()
		want := ""
		for _, r := range rs {
			m, ok := mode[r]
			require.True(t, ok, "the route table has %s", r)
			if want == "" {
				want = m
			}
			require.Equal(t, want, m, "%s needs the same mode as %s", r, rs[0])
		}
		return want
	}

	// The claims the mode texts make: a phrase (a regexp, so "start" is not "restart") and the routes it stands for.
	type claim struct {
		phrase string
		routes []string
	}
	claims := []claim{
		{`type into`, []string{"POST /api/sessions/live/{name}/send", "POST /api/sessions/live/{name}/keys"}},
		{`post on boards`, []string{"POST /api/board/{project}/messages"}},
		{`change tasks`, []string{"PATCH /api/board/{project}/tasks/{taskID}", "PATCH /api/sessions/live/{name}/tasks/{taskID}"}},
		{`notes`, []string{"PATCH /api/sessions/live/{name}/notes/{noteID}", "DELETE /api/sessions/live/{name}/notes/{noteID}"}},
		{`rename`, []string{"PUT /api/sessions/live/{name}/display-name"}},
		{`archive`, []string{"POST /api/sessions/live/{sessionID}/archive", "POST /api/sessions/live/{sessionID}/unarchive"}},
		{`working mode`, []string{"PUT /api/board/{project}/working-mode"}},
		{`\bstart\b`, []string{"POST /api/sessions/launch", "POST /api/sessions/launch-team"}},
		{`\bstop\b`, []string{"POST /api/sessions/live/{name}/kill", "POST /api/sessions/live/team/{boardName}/kill"}},
		{`\bsleep\b`, []string{"POST /api/sessions/live/{sessionID}/sleep", "POST /api/sessions/live/team/{boardName}/sleep", "POST /api/sessions/live/sleep-all"}},
		{`\bwake\b`, []string{"POST /api/sessions/live/{sessionID}/wake", "POST /api/sessions/live/team/{boardName}/wake", "POST /api/sessions/live/wake-all"}},
		{`\brestart\b`, []string{"POST /api/sessions/live/{name}/restart"}},
		{`browse`, []string{"GET /api/filesystem/list"}},
		{`pause boards`, []string{"POST /api/board/{project}/pause", "POST /api/board/{project}/resume"}},
		{`delete board messages`, []string{"DELETE /api/board/{project}/messages/{messageID}"}},
		{`turn on auto-accept`, []string{"POST /api/sessions/live/{name}/auto-accept/enable"}},
	}
	// Each mode's text, from the three places the dashboard says it.
	texts := map[string]map[string]string{} // source -> mode -> text
	modeText := regexp.MustCompile(`(?m)^\s*(view|interact|launch): \{ label: '[A-Za-z]+', text: '((?:[^'\\]|\\.)*)' \}`)
	texts["cloud_servers.js MODE_INFO"] = map[string]string{}
	for _, m := range modeText.FindAllStringSubmatch(read("static/cloud_servers.js"), -1) {
		texts["cloud_servers.js MODE_INFO"][m[1]] = m[2]
	}
	helpText := regexp.MustCompile(`(?m)^\s*(view|interact|launch): '((?:[^'\\]|\\.)*)',`)
	help := read("static/cloud_connect.js")
	help = help[strings.Index(help, "const MODE_HELP"):]
	help = help[:strings.Index(help, "};")]
	texts["cloud_connect.js MODE_HELP"] = map[string]string{}
	for _, m := range helpText.FindAllStringSubmatch(help, -1) {
		texts["cloud_connect.js MODE_HELP"][m[1]] = m[2]
	}
	for src, byMode := range texts {
		require.Len(t, byMode, 3, "%s has a text for each mode", src)
		assert.NotRegexp(t, `(?i)\bkill\b`, byMode["interact"], "%s: Interact cannot kill an agent (Launch since #4274)", src)
		for _, c := range claims {
			re := regexp.MustCompile(`(?i)` + c.phrase)
			var in []string
			for m, text := range byMode {
				if re.MatchString(text) {
					in = append(in, m)
				}
			}
			if len(in) == 0 {
				continue // not every text names every action
			}
			assert.Equal(t, []string{modeOf(c.routes...)}, in, "%s: %q is said of the mode the route table gives it", src, c.phrase)
		}
		// Cross-team messaging is a board setting (PUT .../settings, the cross_team_messaging field), not a working-mode one, and
		// the tunnel never reaches it: no mode may claim it (QA on #4526, where the first revision said Interact could).
		for m, text := range byMode {
			assert.NotRegexp(t, `(?i)cross-team`, text, "%s: %s does not claim cross-team messaging", src, m)
		}
		// The two big ones are said, and of the right mode.
		assert.Regexp(t, `\bstop\b`, byMode["launch"], "%s: Launch says it stops agents", src)
		assert.Regexp(t, `type into`, byMode["interact"], "%s: Interact says it types into agents", src)
	}
	// The Settings hint under "Most the hub may do on this computer".
	hint := read("templates/includes/modals.html")
	i := strings.Index(hint, "Interact lets a hub user")
	require.GreaterOrEqual(t, i, 0, "modals.html has the Settings hint")
	hint = hint[i : i+strings.Index(hint[i:], ".")]
	require.Equal(t, "launch", modeOf("POST /api/sessions/live/{name}/kill", "POST /api/sessions/live/{sessionID}/sleep", "POST /api/sessions/live/{name}/restart"))
	assert.Regexp(t, `Launch also lets them start, stop, sleep, wake and restart agents`, hint)

	// The dimmed controls (hub_blocked_controls.js): each action is in the set of the mode its routes need.
	src := read("static/hub_blocked_controls.js")
	set := func(name string) map[string]bool {
		t.Helper()
		m := regexp.MustCompile(`(?s)const ` + name + ` = new Set\(\[(.*?)\]\);`).FindStringSubmatch(src)
		require.NotNil(t, m, "hub_blocked_controls.js has %s", name)
		out := map[string]bool{}
		for _, a := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(m[1], -1) {
			out[a[1]] = true
		}
		return out
	}
	// Board settings: Launch and the owner's alone since #5307, and only the health setup; cross-team messaging and files stay local.
	require.Equal(t, "launch", modeOf("PUT /api/board/{project}/settings"))
	sets := map[string]map[string]bool{"interact": set("INTERACT"), "launch": set("LAUNCH")}
	controls := map[string][]string{
		"cmd-send":      {"POST /api/sessions/live/{name}/send"},
		"cmd-raw-keys":  {"POST /api/sessions/live/{name}/keys"},
		"agent-rename":  {"PUT /api/sessions/live/{name}/display-name"},
		"agent-icon":    {"PUT /api/sessions/live/{name}/icon"},
		"agent-archive": {"POST /api/sessions/live/{sessionID}/archive"},
		"agent-kill":    {"POST /api/sessions/live/{name}/kill"},
		"agent-sleep":   {"POST /api/sessions/live/{sessionID}/sleep", "POST /api/sessions/live/{sessionID}/wake"},
		"agent-restart": {"POST /api/sessions/live/{name}/restart"},
		"team-sleep":    {"POST /api/sessions/live/team/{boardName}/sleep", "POST /api/sessions/live/team/{boardName}/wake"},
		"mb-pause":      {"POST /api/board/{project}/pause", "POST /api/board/{project}/resume"},
		"mb-msg-delete": {"DELETE /api/board/{project}/messages/{messageID}"},
		"attach-image":  {"POST /api/upload"}, // the Add image button (#6088): Launch through the hub, as its route is
	}
	for action, rs := range controls {
		want := modeOf(rs...)
		require.Contains(t, []string{"interact", "launch"}, want, "%s", action)
		assert.True(t, sets[want][action], "%s needs %s (the route table): it is in that set", action, want)
		other := map[string]string{"interact": "launch", "launch": "interact"}[want]
		assert.False(t, sets[other][action], "%s is not also in the %s set", action, other)
	}
	// Auto-accept: on needs Launch, off Interact, and the control says which by its state.
	require.Equal(t, "launch", modeOf("POST /api/sessions/live/{name}/auto-accept/enable"))
	require.Equal(t, "interact", modeOf("POST /api/sessions/live/{name}/auto-accept/disable"))
	assert.Contains(t, src, `if (a === AUTO_ACCEPT) return el.getAttribute('aria-checked') === 'true' ? 'interact' : 'launch';`)
	assert.False(t, sets["interact"]["agent-auto-accept"] || sets["launch"]["agent-auto-accept"], "auto-accept is decided by its state, not a set")
}
