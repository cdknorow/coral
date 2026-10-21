package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/linktest"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// Sweep items 4 and 5 (#3583): a team import and a CLI check by path are the Operator's, and
// bounded to what they are for.

func asOperator(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), callerKindKey{}, callerDashCookie))
}

func asAgent(r *http.Request) *http.Request {
	r.Header.Set(store.SessionTokenHeader, "agent-token-for-test")
	return r
}

func importTeam(t *testing.T, h *SystemHandler, path string, as func(*http.Request) *http.Request) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"path": path})
	req := as(httptest.NewRequest(http.MethodPost, "/api/teams/import", bytes.NewReader(body)))
	rec := httptest.NewRecorder()
	h.ImportTeam(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(text), 0o644))
}

func newTeamDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "my-team")
	writeFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: Project Lead\n---\nYou plan.")
	writeFile(t, filepath.Join(dir, "agents", "dev.md"), "---\nname: Backend Dev\n---\nYou write Go.")
	return dir
}

func agentNames(out map[string]any) []string {
	var names []string
	if team, ok := out["team"].(map[string]any); ok {
		out = team
	}
	agents, _ := out["agents"].([]any)
	for _, a := range agents {
		if m, ok := a.(map[string]any); ok {
			names = append(names, m["name"].(string))
		}
	}
	return names
}

func TestImportTeamReadsANormalTeam(t *testing.T) {
	h := newSystemHandlerForTest(t)
	code, out := importTeam(t, h, newTeamDir(t), asOperator)
	require.Equal(t, http.StatusOK, code, out)
	assert.ElementsMatch(t, []string{"Project Lead", "Backend Dev"}, agentNames(out))
}

func TestImportTeamRefusesAnAgentToken(t *testing.T) {
	h := newSystemHandlerForTest(t)
	code, out := importTeam(t, h, newTeamDir(t), asAgent)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "operator_only", out["code"])
	code, _ = importTeam(t, h, newTeamDir(t), func(r *http.Request) *http.Request { return r })
	assert.Equal(t, http.StatusForbidden, code, "a caller that isn't a person is refused too")
}

func TestImportTeamNeverFollowsASymlink(t *testing.T) {
	h := newSystemHandlerForTest(t)
	dir := newTeamDir(t)
	secret := filepath.Join(t.TempDir(), "outside.md")
	writeFile(t, secret, "---\nname: Leaked\n---\nOUTSIDE-SECRET-TEXT")
	linktest.Symlink(t, secret, filepath.Join(dir, "agents", "x.md"))
	outsideDir := filepath.Join(t.TempDir(), "elsewhere")
	writeFile(t, filepath.Join(outsideDir, "y.md"), "OUTSIDE-SECRET-TEXT")
	linktest.Symlink(t, outsideDir, filepath.Join(dir, "agents", "sub"))

	code, out := importTeam(t, h, dir, asOperator)
	require.Equal(t, http.StatusOK, code, out)
	raw, _ := json.Marshal(out)
	assert.NotContains(t, string(raw), "OUTSIDE-SECRET-TEXT")
	assert.NotContains(t, agentNames(out), "Leaked")

	// A symlinked SKILL.md and a symlinked agents/ folder are not read either.
	dir2 := filepath.Join(t.TempDir(), "team2")
	require.NoError(t, os.MkdirAll(dir2, 0o755))
	linktest.Symlink(t, secret, filepath.Join(dir2, "SKILL.md"))
	linktest.Symlink(t, outsideDir, filepath.Join(dir2, "agents"))
	code, out = importTeam(t, h, dir2, asOperator)
	raw, _ = json.Marshal(out)
	assert.NotContains(t, string(raw), "OUTSIDE-SECRET-TEXT", "status %d", code)
}

func TestImportTeamRefusesSensitiveAndDotFolders(t *testing.T) {
	h := newSystemHandlerForTest(t)
	base := t.TempDir()
	for _, rel := range []string{".ssh", ".hidden-team", filepath.Join(".aws", "team"), filepath.Join("repo", ".git")} {
		dir := filepath.Join(base, rel)
		writeFile(t, filepath.Join(dir, "SKILL.md"), "SENSITIVE-TEXT")
		code, out := importTeam(t, h, dir, asOperator)
		assert.Equal(t, http.StatusBadRequest, code, rel)
		raw, _ := json.Marshal(out)
		assert.NotContains(t, string(raw), "SENSITIVE-TEXT", rel)
	}
	code, _ := importTeam(t, h, "/", asOperator)
	assert.Equal(t, http.StatusBadRequest, code, "the file system's root")
	if home, err := os.UserHomeDir(); err == nil {
		code, _ = importTeam(t, h, home, asOperator)
		assert.Equal(t, http.StatusBadRequest, code, "the home folder")
	}
	code, _ = importTeam(t, h, "relative/team", asOperator)
	assert.Equal(t, http.StatusBadRequest, code)

	// Inside a team, dot-files and sensitive names are skipped.
	dir := newTeamDir(t)
	writeFile(t, filepath.Join(dir, "agents", ".hidden.md"), "---\nname: Hidden\n---\nDOT-TEXT")
	writeFile(t, filepath.Join(dir, "agents", "credentials.md"), "---\nname: Creds\n---\nCRED-TEXT")
	code, out := importTeam(t, h, dir, asOperator)
	require.Equal(t, http.StatusOK, code)
	raw, _ := json.Marshal(out)
	assert.NotContains(t, string(raw), "DOT-TEXT")
	assert.NotContains(t, string(raw), "CRED-TEXT")
}

func TestImportTeamCapsFilesAndAgents(t *testing.T) {
	h := newSystemHandlerForTest(t)
	dir := newTeamDir(t)
	writeFile(t, filepath.Join(dir, "agents", "big.md"), strings.Repeat("x", importFileMax+1))
	code, out := importTeam(t, h, dir, asOperator)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, out["error"], "larger than 256 KiB")

	dir = newTeamDir(t)
	writeFile(t, filepath.Join(dir, "agents", "exact.md"), strings.Repeat("x", importFileMax))
	code, _ = importTeam(t, h, dir, asOperator)
	assert.Equal(t, http.StatusOK, code, "a file of exactly the cap is read")

	dir = newTeamDir(t)
	for i := 0; i < importAgentsMax; i++ {
		writeFile(t, filepath.Join(dir, "agents", fmt.Sprintf("a%03d.md", i)), "x")
	}
	code, out = importTeam(t, h, dir, asOperator)
	assert.Equal(t, http.StatusBadRequest, code, "51 agent files")
	assert.Contains(t, out["error"], "more than 50")
}

func cliCheckAs(h *SystemHandler, query string, as func(*http.Request) *http.Request) *httptest.ResponseRecorder {
	req := as(httptest.NewRequest(http.MethodGet, "/api/system/cli-check?"+query, nil))
	rec := httptest.NewRecorder()
	h.CLICheck(rec, req)
	return rec
}

// markerCLI writes a script named name in dir that would leave a marker file if it ran.
func markerCLI(t *testing.T, dir, name string) (script, marker string) {
	t.Helper()
	marker = filepath.Join(dir, "ran-"+name)
	script = filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\necho 9.9.9\n"), 0o755))
	return script, marker
}

func TestCLICheckRefusesABinaryInATempFolder(t *testing.T) {
	h := newSystemHandlerForTest(t)
	for _, name := range []string{"x", "claude"} {
		script, marker := markerCLI(t, t.TempDir(), name)
		rec := cliCheckAs(h, "binary="+script, asOperator)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", name, rec.Body.String())
		time.Sleep(50 * time.Millisecond) // sleep-ok: a run would have left the marker by now
		assert.NoFileExists(t, marker)
	}
	for _, q := range []string{"binary=bash", "binary=sh", "binary=relative/claude", "binary=" + "/bin/echo"} {
		rec := cliCheckAs(h, q, asOperator)
		assert.Equal(t, http.StatusBadRequest, rec.Code, q)
	}
}

// With the temp folders out of the way (the tests run in one), an agent's working folder and
// Coral's data folder are still refused.
func TestCLICheckRefusesAnAgentsWorkdirAndTheDataDir(t *testing.T) {
	prev := cliTempDirs
	cliTempDirs = func() []string { return nil }
	t.Cleanup(func() { cliTempDirs = prev })
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "sys.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{LogDir: t.TempDir()}
	dataDir := t.TempDir()
	cfg.UseCoralDir(dataDir)
	h := NewSystemHandler(db, cfg)

	repo := t.TempDir()
	require.NoError(t, store.NewSessionStore(db).RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID: "s-3583", AgentType: "claude", AgentName: "dev", WorkingDir: repo,
	}))
	for _, dir := range []string{filepath.Join(repo, "bin"), filepath.Join(dataDir, "hooks")} {
		script, marker := markerCLI(t, dir, "claude")
		rec := cliCheckAs(h, "binary="+script+"&type=claude", asOperator)
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		time.Sleep(50 * time.Millisecond) // sleep-ok: a run would have left the marker by now
		assert.NoFileExists(t, marker)
	}
	// A symlink outside to a binary inside is refused too.
	script, _ := markerCLI(t, filepath.Join(repo, "bin2"), "claude")
	link := filepath.Join(t.TempDir(), "claude")
	linktest.Symlink(t, script, link)
	assert.Equal(t, http.StatusBadRequest, cliCheckAs(h, "binary="+link, asOperator).Code)
}

func TestCLICheckRunsTheConfiguredPathAndKnownNames(t *testing.T) {
	if _, err := os.Stat("/bin/echo"); err != nil {
		t.Skip("no /bin/echo")
	}
	h := newSystemHandlerForTest(t)
	require.NoError(t, h.ss.SetSetting(context.Background(), "cli_path_claude", "/bin/echo"))
	rec := cliCheckAs(h, "binary=/bin/echo&type=claude", asOperator)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"found":true`)

	cli := useFakeCLIs(t)
	cli.install(t, "claude", `echo 2.0.0`)
	out := cliCheck(t, h, "binary=claude")
	assert.Equal(t, true, out["found"])
	// type= alone is unchanged, from any caller.
	rec = cliCheckAs(h, "type=codex", asAgent)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestCLICheckByPathRefusesAnAgentToken(t *testing.T) {
	h := newSystemHandlerForTest(t)
	for _, as := range []func(*http.Request) *http.Request{asAgent, func(r *http.Request) *http.Request { return r }} {
		rec := cliCheckAs(h, "binary=claude", as)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Contains(t, rec.Body.String(), "operator_only")
	}
}
