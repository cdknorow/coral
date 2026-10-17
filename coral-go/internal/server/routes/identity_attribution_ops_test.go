package routes

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The routes only the Operator may use (working mode, presets, pause and resume...) and the cross-team
// relay (#4255 commit 4). An agent's token is refused by the guard in every mode; a caller with no
// credential (claimed) is let through in log mode and counted, and refused under enforce; the
// Operator's cookie goes through in both.
func TestOperatorOnlyRoutesAskTheActor(t *testing.T) {
	routes := []struct{ name, method, path, body, site string }{
		{"pause", "POST", "/api/board/team/pause", ``, "operator-only route POST /api/board/{project}/pause"},
		{"resume", "POST", "/api/board/team/resume", ``, "operator-only route POST /api/board/{project}/resume"},
		{"working mode", "PUT", "/api/board/team/working-mode", `{"mode":"none"}`, "operator-only route PUT /api/board/{project}/working-mode"},
		{"save a preset", "POST", "/api/board/team/working-mode/presets", `{"id":"p1","name":"P","instructions":"x"}`, "operator-only route POST /api/board/{project}/working-mode/presets"},
	}
	refused := func(code int, body string) bool {
		return code == http.StatusForbidden && (strings.Contains(body, "identity_unverified") || strings.Contains(body, "forbidden_for_agent"))
	}
	for _, rt := range routes {
		for _, mode := range []string{"log", "enforce"} {
			t.Run(mode+"/"+rt.name, func(t *testing.T) {
				a := newAttributionHarness(t)
				setMode(t, mode == "enforce")
				for _, agent := range []string{"orch", "dev"} {
					code, body := a.callBody(agent, rt.method, rt.path, rt.body)
					assert.True(t, refused(code, body), "an agent's token is refused in every mode: %s as %s -> %d %s", rt.name, agent, code, body)
				}
				code, body := a.callBody("operator", rt.method, rt.path, rt.body)
				assert.False(t, refused(code, body), "the Operator goes through: %s -> %d %s", rt.name, code, body)
				code, body = a.callBody("claimed", rt.method, rt.path, rt.body)
				if mode == "log" {
					assert.False(t, refused(code, body), "log mode lets a claimed caller through, as before: %d %s", code, body)
					assert.Contains(t, enforceDiffersSites(), rt.site, "and counts it")
				} else {
					assert.True(t, strings.Contains(body, "identity_unverified") && code == http.StatusForbidden, "enforce refuses it: %d %s", code, body)
				}
			})
		}
	}
}

// The cross-team send: the Operator name needs the Operator the identity layer verified (not just a browser
// with no sign-in), and an agent sends from its own token's session, whatever session it names.
func TestRelaySenderAsksTheActor(t *testing.T) {
	send := `{"subscriber_id":"Operator","content":"hello","to_address":"srv/team"}`
	sendAsOrch := `{"subscriber_id":"Orchestrator","content":"hello","to_address":"srv/team"}`
	sendNoSession := `{"content":"hello","to_address":"srv/team"}`
	code := func(body string) string { // the relay's error code in the answer
		for _, c := range []string{"operator_requires_person", "session_mismatch", "not_orchestrator", "cross_team_disabled", "identity_unverified", "not_linked"} {
			if strings.Contains(body, c) {
				return c
			}
		}
		return "other: " + body
	}

	for _, mode := range []string{"log", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			a := newAttributionHarness(t)
			setMode(t, mode == "enforce")
			// A browser with no sign-in, claiming the Operator: a person to the earlier test, nobody to the actor.
			_, body := a.callBody("browser", "POST", "/api/board/team/relay-messages", send)
			if mode == "log" {
				assert.NotEqual(t, "operator_requires_person", code(body), "log mode: the earlier test passes it on: %s", body)
				assert.Contains(t, enforceDiffersSites(), "relay-operator POST /api/board/{project}/relay-messages")
			} else {
				// Under enforce the Operator-name guard refuses a caller the layer did not verify before the
				// handler is reached; the handler's own check (relaySenderRole) is the second lock.
				assert.True(t, strings.Contains(body, "reserved for the Operator") || code(body) == "identity_unverified", "enforce: %s", body)
			}
			// The Operator's cookie passes the sender check in both modes (the board has cross-team off).
			_, body = a.callBody("operator", "POST", "/api/board/team/relay-messages", send)
			assert.Equal(t, "cross_team_disabled", code(body), "the Operator is let through to the board's setting: %s", body)
			// The orchestrator's token, naming itself and no session: log mode needs session_name; enforce uses the token's.
			_, body = a.callBody("orch", "POST", "/api/board/team/relay-messages", sendAsOrch)
			if mode == "log" {
				assert.Equal(t, "session_mismatch", code(body), "log mode: no session_name: %s", body)
			} else {
				assert.Equal(t, "cross_team_disabled", code(body), "enforce: the session is the token's: %s", body)
			}
			// ...with no claim at all (enforce), and an ordinary member is not an orchestrator.
			if mode == "enforce" {
				_, body = a.callBody("orch", "POST", "/api/board/team/relay-messages", sendNoSession)
				assert.Equal(t, "cross_team_disabled", code(body), "the claim is optional: %s", body)
			}
			_, body = a.callBody("dev", "POST", "/api/board/team/relay-messages", strings.Replace(sendAsOrch, "Orchestrator", "Dev", 1))
			assert.Contains(t, []string{"not_orchestrator", "session_mismatch"}, code(body), "a member that is not an orchestrator: %s", body)
		})
	}
}
