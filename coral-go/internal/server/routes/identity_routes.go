package routes

import (
	"fmt"
	"github.com/subgentic/coral/internal/serverproof"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/cliauth"
)

var agentToolPrefixes = []string{"coral-board/", "coral-agent/", "coral-hook-"}

func isAgentTool(r *http.Request) bool {
	ua := r.UserAgent()
	for _, p := range agentToolPrefixes {
		if strings.HasPrefix(ua, p) {
			return true
		}
	}
	return false
}

// operatorOnlyRoutes run code or start agents on this computer (#3424, audit HIGH-01):
// workflows (shell steps), scheduled jobs and task runs. An agent's token can't use them, in
// every mode since #3929 (only with strict local auth before); the Operator can (the dashboard,
// an API key, the signed command line). Each agent call is recorded in the identity report.
var operatorOnlyRoutes = map[string]string{
	// #3668 (the board authorization audit): deleting a message, and pausing or resuming a board
	// (the Operator's brake), are the Operator's.
	"DELETE /api/board/{project}/messages/{messageID}": "delete a board message",
	"POST /api/board/{project}/pause":                  "pause a board",
	"POST /api/board/{project}/resume":                 "resume a board",
	// #6329: the instruction of an agent's reminder is the Operator's own text to that agent; another agent's token cannot read it.
	"GET /api/board/{project}/reminder": "read an agent's reminder instruction",
	"DELETE /api/artifacts/series/{series_id}":         "delete an artifact series", // #6357: every version of it
	// #3665: resuming agents on the current hook set stops and restarts them.
	"GET /api/system/hooks-status":                      "see which agents run an older hook set",
	"POST /api/sessions/live/{sessionID}/refresh-hooks": "resume an agent on the current hook set",
	"POST /api/workflows":                               "create a workflow",
	"PUT /api/workflows/{workflowID}":                   "change a workflow",
	"DELETE /api/workflows/{workflowID}":                "delete a workflow",
	"POST /api/workflows/{workflowID}/trigger":          "run a workflow",
	"POST /api/workflows/by-name/{name}/trigger":        "run a workflow",
	"POST /api/scheduled/jobs":                          "schedule a job",
	"PUT /api/scheduled/jobs/{jobID}":                   "change a scheduled job",
	"DELETE /api/scheduled/jobs/{jobID}":                "delete a scheduled job",
	"POST /api/scheduled/jobs/{jobID}/toggle":           "turn a scheduled job on or off",
	"POST /api/tasks/run":                               "start a task run",

	// #3445: stopping the Operator's runs, and changing Coral's settings (the whole PUT: it holds
	// cli_path_* (persistent code execution for every later launch), auto-accept, remote access,
	// cloud and hub-operator keys and the debug input log, and no agent tool sends it).
	"POST /api/workflows/runs/{runID}/kill":              "stop a workflow run",
	"POST /api/tasks/runs/{runID}/kill":                  "stop a task run",
	"PUT /api/settings":                                  "change Coral's settings",
	"POST /api/board/{project}/cross-team/disable":       "turn a team's cross-team switch on or off",
	"POST /api/board/{project}/cross-team/enable":        "turn a team's cross-team switch on or off",
	"POST /api/board/{project}/cross-team/files/disable": "turn a team's cross-team switch on or off",
	"POST /api/board/{project}/cross-team/files/enable":  "turn a team's cross-team switch on or off",
	"POST /api/cloud/cross-team-links":                   "send Coral Hub's list of cross-team links",
	"GET /api/board/{project}/cross-team/settings":       "read a team's cross-team switches for Coral Hub",
	"GET /api/board/{project}/cross-team-state":          "read who may message a board",
	"GET /api/board/{project}/cross-team-candidates":     "list the boards that may be put on a board's list",
	"GET /api/board/{project}/settings/log":              "read a board's settings log",
	"GET /api/custom-models":                             "read the custom models",
	"PUT /api/custom-models":                             "change the custom models",
	// #4432 (Security's #4431 INFO 1): reading them too: the CLI paths, prompts and storage and access switches
	// are the Operator's, and no agent tool reads them.
	"GET /api/settings": "read Coral's settings",

	// #3445, found by QA: routes whose only guard was "this looks like the dashboard" (an Origin and
	// a Sec-Fetch-Site header any local caller can send, so an agent token plus two forged headers
	// minted a sign-in link and became the Operator), and Coral's configuration and outbound paths.
	"POST /api/system/login-token":        "mint a dashboard sign-in link",
	"POST /api/system/api-key/regenerate": "regenerate the API key",
	"PUT /api/system/database-security":   "change the database security setting",
	"DELETE /api/system/debug-input-log":  "delete the terminal input debug log",
	"PUT /api/system/hub-operator":        "change the Coral Hub Operator setting",
	// #3460: Settings > Run as a hub, and restarting Coral (Security's review).
	"PUT /api/system/hub-mode": "change whether Coral runs as a hub",
	"POST /api/system/restart": "restart Coral",
	// #5280: relaunching into a staged update (Coral Hub's owner), and what is staged.
	"GET /api/system/update":                 "see the staged update",
	"POST /api/system/update/relaunch":       "relaunch Coral into the staged update",
	"POST /api/system/update/install":        "install the staged update (Windows)",
	"POST /api/system/update/relaunch-local": "relaunch Coral into the staged update from this computer",
	"POST /api/system/update/check":          "check for an update now",
	"PUT /api/system/update/settings":        "change whether Coral checks for updates automatically",
	// #4065: the models each agent CLI offers. Reading runs no CLI, but agents have no use for
	// it; a refresh runs the user's CLIs (rate limited) and is the Operator's.
	"GET /api/agents/models":          "list the models each agent CLI offers",
	"POST /api/agents/models/refresh": "refresh the agent CLIs' model lists",
	// #3634: the terminal backend, and ending agents left in Coral's old tmux server.
	"PUT /api/system/terminal-backend":               "change the terminal backend",
	"POST /api/system/terminal-backend/end-leftover": "end agents left in Coral's old tmux server",
	"POST /api/system/terminal-backend/notice-seen":  "dismiss the line that agents now stop when Coral quits",
	"PUT /api/cloud/max-mode":                        "change what Coral Hub may do",
	"PUT /api/cloud/allow-auto-accept":               "change whether Coral Hub may turn on auto-accept",
	"POST /api/hub-auto-accept":                      "change whether Coral Hub may turn on auto-accept (Coral Hub's owner)", // #5840
	"PUT /api/cloud/redact-tokens":                   "change whether tokens are hidden from Coral Hub viewers",
	"PUT /api/cloud/hub-relaunch":                    "change whether Coral Hub may relaunch Coral for an update",                         // #5280
	"PUT /api/cloud/hub-cross-team":                  "change whether Coral Hub may turn cross-team messaging and file sharing on or off", // #5572
	"PUT /api/system/identity-actor":                 "change how Coral decides who is calling",
	"PUT /api/system/codex-trust-hooks":              "change whether Coral trusts its own hooks for the Codex agents it launches", // #153
	"GET /api/system/codex-trust-hooks":              "read whether Coral trusts its own hooks for the Codex agents it launches",   // #153
	"POST /api/cloud/connect":                        "connect to Coral Hub",
	"POST /api/cloud/connect/cancel":                 "cancel connecting to Coral Hub",
	"POST /api/cloud/disconnect":                     "disconnect from Coral Hub",
	"POST /api/cloud/redeem":                         "redeem a Coral Hub credential",
	"POST /api/cloud/rekey":                          "replace the secret key and discard what it sealed", // #3698
	"GET /api/cloud/owner-label":                     "see whether Coral Hub's owner is recorded on this computer",
	"POST /api/cloud/owner-label/arm":                "confirm who owns this computer on Coral Hub",
	// #3553: the Operator removes an artifact (also refused to anyone but a person, in every mode).
	"DELETE /api/artifacts/{id}": "delete an artifact",
	// #5575: the machine-wide list of every artifact (the owner's, here or through Coral Hub): never an agent's.
	"GET /api/artifacts":              "list every artifact on this computer",
	"GET /api/artifacts/{id}/readers": "see who can read an artifact (#5577)",
	// #5284: the Operator removes one session's shared copy of an image pasted in the dashboard.
	"DELETE /api/shared-images/{id}": "remove a pasted image's shared copy",
	// #3554: pruning, and the keep mark that protects an artifact from it.
	"POST /api/artifacts/retention/ack":               "acknowledge artifact retention",
	"POST /api/artifacts/prune":                       "prune artifacts",
	"PUT /api/artifacts/{id}/keep":                    "keep an artifact",
	"POST /api/artifacts/{id}/comments":               "comment on an artifact",
	"PATCH /api/artifacts/{id}/comments/{cid}":        "edit a comment on an artifact",
	"DELETE /api/artifacts/{id}/comments/{cid}":       "delete a comment on an artifact",
	"POST /api/artifacts/{id}/comments/{cid}/resolve": "resolve a comment thread",
	"POST /api/artifacts/{id}/comments/{cid}/send":    "send a comment to agents",
	"POST /api/artifacts/{id}/comments/{cid}/reopen":  "reopen a comment thread",
	"PUT /api/artifacts/{id}/comments/lock":           "lock the comments on an artifact",
	"DELETE /api/artifacts/{id}/comments/lock":        "unlock the comments on an artifact",
	"POST /api/artifacts/received/unlisted/delete":    "delete received files no board lists",
	"DELETE /api/artifacts/{id}/keep":                 "release a kept artifact",
	// #4512 (Security's #4508 H2 and the class-c routes beside it; a grep of coral-board, coral-agent, the hooks
	// and the mock agent found no agent-side use of any of them): the terminal stream (and with it typing into and
	// resizing any agent's terminal), turning every agent's auto-accept off, the dashboard's file upload and
	// opening a file in the Operator's editor are the Operator's.
	"GET /ws/terminal/{name}":                       "read or type into an agent's terminal",
	"POST /api/auto-accept/disable-all":             "turn off auto-accept for every agent",
	"POST /api/upload":                              "upload a file from the dashboard",
	"DELETE /api/upload/{name}":                     "remove an uploaded file",
	"GET /api/upload/{name}":                        "read an uploaded file", // #5028
	"POST /api/sessions/live/{name}/open-in-editor": "open a file in the Operator's editor",
	"GET /api/system/terminal-logs":                 "see the terminal logs",
	"POST /api/system/terminal-logs/delete-ended":   "delete the terminal logs of ended agents",
	// #4780 (Security's identity read): the dashboard's request for an Agent UI panel types its text, as the
	// user's request, into the live agent named by session_id: any agent, of any team. No agent tool calls it
	// (grep of coral-board, coral-agent, the hooks and the mock agent), and with an agent's token it was a way
	// to put text in front of another agent that send and keys (above) refuse.
	"POST /api/agent/ui-request": "send an Agent UI request to an agent",
	// #4835 (Security's sweep of attribution fields a client sets and others see as fact): the display name, icon and colour of any
	// session were set by any caller that named its session_id, an agent's token included, so an agent could rename another agent's
	// tile (or dress itself as the Orchestrator). Only the dashboard calls them (grep of coral-board, coral-agent, the hooks and the
	// mock agent).
	"PUT /api/sessions/live/{name}/display-name": "rename an agent",
	"PUT /api/sessions/live/{name}/icon":         "change an agent's icon",
	"PUT /api/sessions/live/{name}/name-color":   "change an agent's name colour",
	// #3513 (Security's #3509 finding 3): the API key is the Operator's credential (with it an
	// agent drops its token and is the Operator, settings included), and deleting a board
	// deletes the team's whole history.
	"GET /api/system/api-key": "read the API key",
	// #3949: the browser password and its sessions are the Operator's.
	"POST /api/auth/password/set":             "set the dashboard password",
	"POST /api/auth/password/change":          "change the dashboard password",
	"POST /api/auth/password/reset":           "reset the dashboard password",
	"GET /api/auth/sessions":                  "list the signed-in browsers",
	"GET /api/auth/setup-code":                "read the password setup code",
	"GET /api/auth/setup-code/show":           "show the password setup code",
	"POST /api/auth/sessions/sign-out-others": "sign out other browsers",
	"DELETE /api/auth/sessions/{id}":          "sign one browser out",
	// Sign in with Coral Hub (#3966): its local settings.
	"GET /api/auth/hub-signin":          "read the Sign in with Coral Hub setting",
	"PUT /api/auth/hub-signin":          "turn Sign in with Coral Hub on or off",
	"POST /api/auth/hub-signin/dismiss": "dismiss the Sign in with Coral Hub notice",
	"GET /api/auth/hub-origins":         "read the Coral Hub sign-in addresses",
	"PUT /api/auth/hub-origins":         "change the Coral Hub sign-in addresses",
	"DELETE /api/board/{project}":       "delete a board",
	"PATCH /api/board/{project}":        "rename a board",
	// #3583 (Security's sweep item 4): a team import reads files from a folder the request names.
	"POST /api/teams/import": "import a team from a folder",
	// #3685 (Security's #3670 follow-up): the hub's server registry (each entry holds a remote
	// Coral's address and API key, and the hub forwards to it), and the board's remote
	// subscriptions (which remote boards this Coral reads and relays).
	"POST /api/servers":           "add a remote server",
	"PATCH /api/servers/{id}":     "change a remote server",
	"DELETE /api/servers/{id}":    "remove a remote server",
	"POST /api/servers/{id}/test": "test a remote server",
	"POST /api/board/remotes":     "subscribe to a remote board",
	"DELETE /api/board/remotes":   "unsubscribe from a remote board",
	// #4297 (Security's #4295, SHOULD 6 and INFO 2): no agent tool reads the registry or the remote
	// boards (grep of coral-board, coral-agent and the hooks found none), so reading them is the
	// Operator's too.
	"GET /api/servers":       "list the remote servers",
	"GET /api/board/remotes": "list the remote board subscriptions",
	"GET /api/board/remotes/proxy/{remoteServer}/projects":                 "read a remote server's boards",
	"GET /api/board/remotes/proxy/{remoteServer}/{project}/messages/all":   "read a remote board's messages",
	"GET /api/board/remotes/proxy/{remoteServer}/{project}/subscribers":    "read a remote board's members",
	"GET /api/board/remotes/proxy/{remoteServer}/{project}/messages/check": "check a remote board for unread messages",
	// #3929 step 2 (Security's #3965 classification, the Orchestrator's call): routes no agent tool
	// or hook calls, which act on OTHER agents, on what every agent is told, or on the machine and
	// the dashboard's own data. Agents talk to each other through the board, not by typing into
	// each other's terminals.
	"POST /api/board/{project}/groups/{groupID}/members":               "add a member to a board group",
	"DELETE /api/board/{project}/groups/{groupID}/members/{sessionID}": "remove a member from a board group",
	"PUT /api/board/{project}/settings":                                "change a board's settings",
	"POST /api/sessions/live/{name}/send":                              "type into an agent's terminal",
	"POST /api/sessions/live/{name}/keys":                              "send keys to an agent's terminal",
	"POST /api/sessions/live/{name}/answer-prompt":                     "answer an agent's permission prompt",
	"POST /api/sessions/live/{name}/kill":                              "stop an agent",
	"POST /api/sessions/live/{name}/restart":                           "restart an agent",
	"POST /api/sessions/live/{name}/model":                             "switch an agent's model or thinking level",
	"POST /api/sessions/live/{name}/resume":                            "resume an agent",
	"POST /api/sessions/live/{name}/attach":                            "attach to an agent's terminal",
	"POST /api/sessions/live/{name}/resize":                            "resize an agent's terminal",
	"POST /api/sessions/live/{name}/goal":                              "set an agent's goal",
	"POST /api/sessions/live/{name}/auto-accept/enable":                "turn on auto-accept for an agent",
	"POST /api/sessions/live/{name}/auto-accept/disable":               "turn off auto-accept for an agent",
	"PUT /api/sessions/live/{name}/file-content":                       "write a file in an agent's working folder",
	"POST /api/sessions/live/team/{boardName}/kill":                    "stop a team",
	"POST /api/sessions/live/team/{boardName}/reset":                   "reset a team",
	"POST /api/sessions/live/team/{boardName}/sleep":                   "put a team to sleep",
	"POST /api/sessions/live/team/{boardName}/wake":                    "wake a team",
	"POST /api/sessions/live/sleep-all":                                "put every agent to sleep",
	"POST /api/sessions/live/wake-all":                                 "wake every agent",
	"POST /api/sessions/live/{sessionID}/sleep":                        "put an agent to sleep",
	"POST /api/sessions/live/{sessionID}/wake":                         "wake an agent",
	"POST /api/sessions/live/{sessionID}/archive":                      "archive an agent",
	"POST /api/sessions/live/{sessionID}/unarchive":                    "unarchive an agent",
	"PUT /api/board/{project}/working-mode":                            "change a team's working mode",
	"POST /api/board/{project}/working-mode/presets":                   "add a working-mode preset",
	"PUT /api/board/{project}/working-mode/presets/{presetID}":         "change a working-mode preset",
	"POST /api/board/{project}/working-mode/presets/{presetID}/reset":  "reset a working-mode preset",
	"PUT /api/teams/detail/{name}/knowledge/{agentName}":               "change a team's knowledge",
	// Reads across every session, board and team (#5013): an agent reads its own session and board (agentSessionRoutes, checkBoard), and its own
	// history through /api/agent/history; none of these is called by an agent tool.
	"GET /api/sessions/history":                          "list every session's history",
	"GET /api/sessions/history/search":                   "search every session's history",
	"GET /api/token-usage":                               "list every session's token usage",
	"GET /api/token-usage/by-branch":                     "read token usage by branch",
	"GET /api/goals/metrics":                             "read the goal generator's metrics (every session's goal text)", // #5041
	"GET /api/system/identity-report":                    "read the identity report (every session's calls and mismatches)",
	"GET /api/board/tasks":                               "list the tasks of every board",
	"GET /api/teams/detail/{name}/knowledge":             "read a team's knowledge",
	"GET /api/teams/detail/{name}/knowledge/{agentName}": "read an agent's knowledge",
	"POST /api/teams/detail/{name}/resurrect":            "bring a team back",
	// The Operator's automation (#5075): the commands, prompts and flags of workflows and scheduled jobs, and what their runs wrote. They belong to no board, an
	// agent cannot create, run or change one (census b), and none is called by an agent tool; a step's command often holds a credential.
	"GET /api/workflows":                                        "list every workflow (its commands and prompts)",
	"GET /api/workflows/{workflowID}":                           "read a workflow (its commands and prompts)",
	"GET /api/workflows/by-name/{name}":                         "read a workflow (its commands and prompts)",
	"GET /api/workflows/runs/recent":                            "list every workflow run",
	"GET /api/workflows/runs/{runID}":                           "read a workflow run's output",
	"GET /api/workflows/{workflowID}/runs":                      "list a workflow's runs",
	"GET /api/workflows/runs/{runID}/steps/{stepIndex}/files/*": "read a workflow step's files",
	"GET /api/scheduled/jobs":                                   "list every scheduled job (its prompt and flags)",
	"GET /api/scheduled/jobs/{jobID}":                           "read a scheduled job (its prompt and flags)",
	"GET /api/scheduled/jobs/{jobID}/runs":                      "list a scheduled job's runs",
	"GET /api/scheduled/runs/recent":                            "list every scheduled run",
	"DELETE /api/teams/detail/{name}":                           "delete a team",
	"POST /api/teams/generate":                                  "generate a team",
	"PUT /api/settings/git-diff-mode":                           "change the diff mode",
	"POST /api/pinned-folders":                                  "pin a folder (the file browser's bounds)",
	"DELETE /api/pinned-folders":                                "unpin a folder",
	"GET /api/saved":                                            "see the saved messages",
	"GET /api/saved/{id}/message":                               "read a saved message",
	"POST /api/saved":                                           "save a message",
	"PATCH /api/saved/{id}":                                     "change a saved message",
	"DELETE /api/saved/{id}":                                    "remove a saved message",
	"POST /api/themes/import":                                   "import a theme",
	"POST /api/themes/generate":                                 "generate a theme",
	"PUT /api/themes/{name}":                                    "change a theme",
	"DELETE /api/themes/{name}":                                 "delete a theme",
	"POST /api/views":                                           "add a custom view",
	"PUT /api/views/{id}":                                       "change a custom view",
	"DELETE /api/views/{id}":                                    "delete a custom view",
	"POST /api/indexer/refresh":                                 "re-index the session history",
	"POST /api/sessions/history/{sessionID}/resummarize":        "summarise a session again",
	"PUT /api/sessions/history/{sessionID}/notes":               "change a session's notes",
	"POST /api/tags":                                            "add a tag",
	"DELETE /api/tags/{tagID}":                                  "delete a tag",
	"POST /api/folder-tags/{folderName}":                        "tag a folder",
	"DELETE /api/folder-tags/{folderName}/{tagID}":              "untag a folder",
	"POST /api/sessions/history/{sessionID}/tags":               "tag a session",
	"DELETE /api/sessions/history/{sessionID}/tags/{tagID}":     "untag a session",
	"POST /api/tracking/event":                                  "send a usage event",
	"POST /api/system/telemetry/acknowledge":                    "acknowledge the telemetry notice",
	"POST /api/system/auto-wake/dismiss":                        "dismiss the auto-wake notice",
	"POST /api/cloud/cli-hello":                                 "start a command-line signature",
	"GET /api/system/proof":                                     "prove to a tool that this is Coral",
	"POST /api/scheduled/validate-cron":                         "check a schedule",
	// #3929 (the Orchestrator's call on Backend's question): on a hub, an agent's token never acts
	// on another server through the hub proxy, as it never acts on another team's board.
	"CONNECT /api/remote/{server}/*": "act on another server through the hub",
	"DELETE /api/remote/{server}/*":  "act on another server through the hub",
	"GET /api/remote/{server}/*":     "act on another server through the hub",
	"HEAD /api/remote/{server}/*":    "act on another server through the hub",
	"OPTIONS /api/remote/{server}/*": "act on another server through the hub",
	"PATCH /api/remote/{server}/*":   "act on another server through the hub",
	"POST /api/remote/{server}/*":    "act on another server through the hub",
	"PUT /api/remote/{server}/*":     "act on another server through the hub",
	"QUERY /api/remote/{server}/*":   "act on another server through the hub",
	"TRACE /api/remote/{server}/*":   "act on another server through the hub",
}

// hubModeRoutes are entries the router serves only in hub mode (--hub): ValidateGuardedRoutes
// lets them be absent otherwise.
var hubModeRoutes = map[string]bool{
	"GET /api/servers": true, "POST /api/servers": true, "PATCH /api/servers/{id}": true, "DELETE /api/servers/{id}": true, "POST /api/servers/{id}/test": true,
	"CONNECT /api/remote/{server}/*": true,
	"DELETE /api/remote/{server}/*":  true,
	"GET /api/remote/{server}/*":     true,
	"HEAD /api/remote/{server}/*":    true,
	"OPTIONS /api/remote/{server}/*": true,
	"PATCH /api/remote/{server}/*":   true,
	"POST /api/remote/{server}/*":    true,
	"PUT /api/remote/{server}/*":     true,
	"QUERY /api/remote/{server}/*":   true,
	"TRACE /api/remote/{server}/*":   true,
}

// recordedAgentRoutes are allowed to an agent in every mode (an agent launches the agents of
// its team, terminal ones included, by design) but each call is recorded in the identity
// report's agent_operator_calls, so the owner can see who launched what (#3445).
var recordedAgentRoutes = map[string]string{
	"POST /api/sessions/launch":      "launch an agent",
	"POST /api/sessions/launch-team": "launch a team",
}

// guardPrefixes limit the (costly) route match: per method, the static start of every entry of
// the tables above (the pattern up to its first parameter), derived from the tables themselves,
// so an entry is enforced wherever its path is (#3670: a hand-kept prefix list missed the
// artifact routes).
var guardPrefixes = buildGuardPrefixes()

func buildGuardPrefixes() map[string][]string {
	out := map[string][]string{}
	for _, table := range []map[string]string{operatorOnlyRoutes, recordedAgentRoutes} {
		for key := range table {
			method, pattern, ok := splitGuardKey(key)
			if !ok {
				panic("identity guard: malformed route " + strconv.Quote(key) + `: want "METHOD /path"`)
			}
			pre := pattern
			if i := strings.IndexAny(pre, "{*"); i >= 0 {
				pre = pre[:i]
			}
			out[method] = append(out[method], pre)
		}
	}
	return out
}

func splitGuardKey(key string) (method, pattern string, ok bool) {
	method, pattern, ok = strings.Cut(key, " ")
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
		http.MethodHead, http.MethodOptions, http.MethodConnect, http.MethodTrace, "QUERY": // the hub proxy is served under every method chi knows
	default:
		return "", "", false
	}
	return method, pattern, ok && strings.HasPrefix(pattern, "/") && !strings.ContainsAny(pattern, " \t")
}

// ValidateGuardedRoutes checks every entry of the tables above against the router: an entry the
// router doesn't serve under exactly that method and pattern would never be matched, so it would
// be silently unenforced. The server refuses to start on one (#3670). Outside hub mode the
// hubModeRoutes may be absent.
func ValidateGuardedRoutes(routes chi.Routes, hubMode bool) error {
	served := map[string]bool{}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served[method+" "+route] = true
		return nil
	}); err != nil {
		return err
	}
	var missing []string
	for _, table := range []map[string]string{operatorOnlyRoutes, recordedAgentRoutes} {
		for key := range table {
			if !served[key] && (hubMode || !hubModeRoutes[key]) {
				missing = append(missing, key)
			}
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("identity guard: no such route for %s", strings.Join(missing, ", "))
	}
	return nil
}

// OperatorOnlyRouteKeys lists operatorOnlyRoutes ("METHOD pattern"), sorted, for tests.
func OperatorOnlyRouteKeys() []string {
	keys := make([]string, 0, len(operatorOnlyRoutes))
	for k := range operatorOnlyRoutes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// guardedRoute returns the route pattern of r when it is one of operatorOnlyRoutes (only is
// true: an agent is refused in strict mode) or recordedAgentRoutes (recorded, never refused),
// asking the router (the middleware runs before routing).
func guardedRoute(r *http.Request) (route string, only, ok bool) {
	hit := false
	for _, pre := range guardPrefixes[r.Method] {
		if strings.HasPrefix(r.URL.Path, pre) {
			hit = true
			break
		}
	}
	if !hit {
		return "", false, false
	}
	rc := chi.RouteContext(r.Context())
	if rc == nil || rc.Routes == nil {
		return "", false, false
	}
	tctx := chi.NewRouteContext()
	if !rc.Routes.Match(tctx, r.Method, routedPath(r)) {
		return "", false, false
	}
	route = tctx.RoutePattern()
	if _, ok := operatorOnlyRoutes[r.Method+" "+route]; ok {
		return route, true, true
	}
	if _, ok := recordedAgentRoutes[r.Method+" "+route]; ok {
		return route, false, true
	}
	return "", false, false
}

func (o *IdentityObserver) countOperatorCall(session, route string) {
	o.mu.Lock()
	key := session + " " + route
	if _, ok := o.operatorCalls[key]; !ok && len(o.operatorCalls) >= identityBreakKeysMax {
		key = "other"
	}
	o.operatorCalls[key]++
	o.mu.Unlock()
	o.logOnce("operator_route:"+session, "[identity] an agent called an Operator-only (or recorded) route", "session", session, "route", route, "strict", o.strict)
}

// strictOpenPaths need no credential in strict mode: the liveness check and the command
// line's hello (step 1 of its signature, which proves the server, not the caller).
var strictOpenPaths = map[string]bool{"GET /api/health": true, "HEAD /api/health": true, "POST " + cliauth.CLIHelloPath: true, "GET " + serverproof.Path: true}

// ValidateAgentSessionRoutes checks every entry of agentSessionRoutes against the router: one the router doesn't serve under exactly that
// method and pattern would be silently unenforced (the server refuses to start on one, as for the guard tables, #5013).
func ValidateAgentSessionRoutes(routes chi.Routes) error {
	served := map[string]bool{}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served[method+" "+route] = true
		return nil
	}); err != nil {
		return err
	}
	var missing []string
	for key := range agentSessionRoutes {
		if !served[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("agent session scope: no such route for %s", strings.Join(missing, ", "))
	}
	return nil
}
