package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/subgentic/coral/internal/clihelp"
	"github.com/subgentic/coral/internal/linktest"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func artifactServer(t *testing.T, data []byte) (*httptest.Server, string) {
	t.Helper()
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/artifacts/"+id {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv, id
}

// An existing --output file is named in the error, with what to do about it (it used to be the raw "link ...: Cannot create a file when that
// file already exists."), and --force replaces it (#222-E).
func TestDownloadToAnExistingFileSaysSoAndForceReplacesIt(t *testing.T) {
	srv, id := artifactServer(t, []byte("new content"))
	out := filepath.Join(t.TempDir(), "got.txt")
	require.NoError(t, os.WriteFile(out, []byte("old"), 0o600))

	_, err := downloadArtifactOpts(srv.URL, id, out, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "--force")
	assert.Contains(t, err.Error(), out)
	assert.NotContains(t, err.Error(), "link ")
	b, _ := os.ReadFile(out)
	assert.Equal(t, "old", string(b), "nothing was replaced without --force")

	p, err := downloadArtifactOpts(srv.URL, id, out, true)
	require.NoError(t, err)
	assert.Equal(t, out, p)
	b, _ = os.ReadFile(out)
	assert.Equal(t, "new content", string(b))
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(out), "coral-artifact-*"))
	assert.Empty(t, left, "no temporary file is left beside it")

	fresh := filepath.Join(t.TempDir(), "fresh.txt")
	_, err = downloadArtifactOpts(srv.URL, id, fresh, true)
	require.NoError(t, err, "--force with no file there is a plain download")
}

// splitOptions: options stand before, between or after the positional arguments, in any of the flag package's spellings.
func TestSplitOptionsTakesOptionsAnywhere(t *testing.T) {
	for _, args := range [][]string{
		{"--output", "f", "link"}, {"-output", "f", "link"}, {"link", "--output", "f"}, {"link", "-output=f"}, {"--output=f", "link"},
	} {
		got, err := splitOptions(args, []string{"output"}, []string{"force"})
		require.NoError(t, err, "%v", args)
		assert.Equal(t, []string{"link"}, got.Positional, "%v", args)
		assert.Equal(t, "f", got.Values["output"], "%v", args)
	}
	got, err := splitOptions([]string{"link", "--force"}, []string{"output"}, []string{"force"})
	require.NoError(t, err)
	assert.True(t, got.Bools["force"])
	for _, args := range [][]string{{"link", "--nope"}, {"link", "--output"}, {"link", "--force=1"}} {
		_, err := splitOptions(args, []string{"output"}, []string{"force"})
		assert.Error(t, err, "%v", args)
	}
	got, err = splitOptions([]string{"--", "-odd-name"}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"-odd-name"}, got.Positional, "after -- everything is positional")
}

// The command: a flag before the link works (it was read as the link), an unknown one is refused with exit 2.
func TestArtifactDownloadCommandOptionOrder(t *testing.T) {
	srv, id := artifactServer(t, []byte("payload"))
	out := filepath.Join(t.TempDir(), "a.txt")
	for _, args := range [][]string{
		{"artifact", "download", "--output", out, "coral://artifacts/" + id},
		{"artifact", "download", "coral://artifacts/" + id, "--output", out + "2"},
	} {
		code, stdout, stderr := runAgent(t, srv.URL, args...)
		require.Equal(t, 0, code, "%v: %s", args, stderr)
		got, err := os.ReadFile(strings.TrimSpace(stdout))
		require.NoError(t, err)
		assert.Equal(t, "payload", string(got))
	}
	code, _, stderr := runAgent(t, srv.URL, "artifact", "download", "coral://artifacts/"+id, "--nope")
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "unknown option")
	code, _, stderr = runAgent(t, srv.URL, "artifact", "download", "--output", out, "coral://artifacts/"+id)
	assert.Equal(t, 1, code, "an existing file")
	assert.Contains(t, stderr, "--force")
	code, _, _ = runAgent(t, srv.URL, "artifact", "download", "--force", "--output", out, "coral://artifacts/"+id)
	assert.Equal(t, 0, code)
}

func TestTaskListRefusesUnknownOptionsAndHasJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"tasks": []map[string]any{{"id": 3, "status": "pending", "priority": "high", "title": "t"}}})
	}))
	defer srv.Close()
	for _, args := range [][]string{{"task", "list", "--not-a-flag"}, {"task", "current", "--x"}, {"task", "detail", "1", "--x"}, {"task", "detail", "1", "2"}} {
		code, _, stderr := runAgent(t, srv.URL, args...)
		assert.Equal(t, 2, code, "%v: %s", args, stderr)
		assert.Contains(t, stderr, "Usage", "%v", args)
	}
	code, stdout, stderr := runAgent(t, srv.URL, "task", "list", "--json")
	require.Equal(t, 0, code, stderr)
	var tasks []map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &tasks), stdout)
	require.Len(t, tasks, 1)
	assert.EqualValues(t, 3, tasks[0]["id"])
	code, stdout, _ = runAgent(t, srv.URL, "task", "list")
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "ID", "the table is still the default")
}

func TestTaskCurrentJSONAgent(t *testing.T) {
	have := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !have {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"No active task"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":4,"title":"t","priority":"low","status":"in_progress","workflow":{}}`))
	}))
	defer srv.Close()
	code, stdout, stderr := runAgent(t, srv.URL, "task", "current", "--json")
	require.Equal(t, 0, code, stderr)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &got), stdout)
	assert.EqualValues(t, 4, got["id"])
	_, stdout, _ = runAgent(t, srv.URL, "task", "current")
	assert.Contains(t, stdout, "Task #4")
	have = false
	code, stdout, _ = runAgent(t, srv.URL, "task", "current", "--json")
	assert.Equal(t, 0, code)
	assert.Equal(t, "null", strings.TrimSpace(stdout))
}

// --output at a folder, or in a folder that is not there, says so in words (#241).
func TestDownloadOutputProblemsAreExplained(t *testing.T) {
	srv, id := artifactServer(t, []byte("x"))
	dir := t.TempDir()
	_, err := downloadArtifactOpts(srv.URL, id, dir, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is a folder")
	_, err = downloadArtifactOpts(srv.URL, id, filepath.Join(dir, "nope", "x.txt"), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
	assert.NotContains(t, err.Error(), "The system cannot find")

}

func TestHelpTopicsAgent(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	for _, topic := range []string{"windows", "exit-codes", "json", "timeouts"} {
		code, stdout, stderr := runAgent(t, srv.URL, "help", topic)
		assert.Equal(t, 0, code, "%s: %s", topic, stderr)
		assert.Contains(t, stdout, "coral-agent", topic)
	}
	_, stdout, _ := runAgent(t, srv.URL, "help", "json")
	assert.Contains(t, stdout, "task list --json")
	code, _, stderr := runAgent(t, srv.URL, "help", "nonsense")
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "windows | exit-codes | json | timeouts")
	_, stdout, _ = runAgent(t, srv.URL, "--help")
	assert.Contains(t, stdout, "Help topics:  coral-agent help windows | exit-codes | json | timeouts")
}

// Every "coral-agent <word>" in a help page is a command coral-agent has: its windows and exit-codes pages once showed post and read, which belong to coral-board.
func TestHelpPagesNameOnlyRealCommands(t *testing.T) {
	re := regexp.MustCompile(`coral-agent ([a-z][a-z-]*)`)
	for _, topic := range clihelp.TopicNames() {
		page, ok := clihelp.TopicPage("coral-agent", topic)
		require.True(t, ok, topic)
		for _, m := range re.FindAllStringSubmatch(page, -1) {
			assert.True(t, commands[m[1]] || m[1] == "help", "%s page: %q is not a coral-agent command", topic, m[0])
		}
		assert.NotContains(t, page, "coral-agent post", topic)
		assert.NotContains(t, page, "coral-agent read", topic)
	}
	win, _ := clihelp.TopicPage("coral-agent", "windows")
	assert.Contains(t, win, "coral-board help windows", "posting a message is pointed at coral-board")
	codes, _ := clihelp.TopicPage("coral-agent", "exit-codes")
	assert.Contains(t, codes, "coral-agent task list; $LASTEXITCODE")
	assert.Contains(t, codes, "cmd.exe:     coral-agent task list\n               echo %ERRORLEVEL%", "the echo is on its own line")
	boardCodes, _ := clihelp.TopicPage("coral-board", "exit-codes")
	assert.Contains(t, boardCodes, "cmd.exe:     coral-board read\n               echo %ERRORLEVEL%")
	assert.Contains(t, boardCodes, "server_url", "exit 3 mentions the address saved by join")
}

func TestUnknownTaskSubcommandIsUsageAgent(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	code, _, stderr := runAgent(t, srv.URL, "task", "frobnicate")
	assert.Equal(t, 2, code, stderr)
	assert.Contains(t, stderr, "Unknown task subcommand")
}

// Where the file system has no hard links (FAT/exFAT on a USB stick, some shares), --output is still written: the verified bytes are copied into a file created
// exclusively and checked again; an existing file is still refused, --force still replaces, and an error that is not "no links here" is not swallowed.
func TestDownloadOutputFallsBackToACopyWhereThereAreNoHardLinks(t *testing.T) {
	srv, id := artifactServer(t, []byte("payload on a fat stick"))
	old := linkFile
	defer func() { linkFile = old }()

	for _, errno := range unsupportedLinkErrnos {
		linkFile = func(oldname, newname string) error {
			return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: errno}
		}
		dir := t.TempDir()
		out := filepath.Join(dir, "got.txt")
		p, err := downloadArtifactOpts(srv.URL, id, out, false)
		require.NoError(t, err, "errno %d", errno)
		assert.Equal(t, out, p)
		b, _ := os.ReadFile(out)
		assert.Equal(t, "payload on a fat stick", string(b), "errno %d", errno)
		left, _ := filepath.Glob(filepath.Join(dir, "coral-artifact-*"))
		assert.Empty(t, left, "no temporary file is left beside it")

		_, err = downloadArtifactOpts(srv.URL, id, out, false)
		require.Error(t, err, "an existing file is still refused through the copy path")
		assert.Contains(t, err.Error(), "already exists", "errno %d", errno)
		assert.Contains(t, err.Error(), "--force")
	}

	linkFile = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: os.ErrPermission}
	}
	_, err := downloadArtifactOpts(srv.URL, id, filepath.Join(t.TempDir(), "x.txt"), false)
	require.Error(t, err, "a permission error is not a reason to copy")
	assert.True(t, errors.Is(err, os.ErrPermission))
}

// The copy is checked against the SHA-256 again: bytes that changed on the way (a failing stick) are removed, not left as the artifact.
func TestPublishNewRemovesACopyThatDoesNotMatch(t *testing.T) {
	old := linkFile
	defer func() { linkFile = old }()
	linkFile = func(string, string) error { return &os.LinkError{Op: "link", Err: unsupportedLinkErrnos[0]} }
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, []byte("not the artifact"), 0o600))
	dst := filepath.Join(dir, "dst")
	err := publishNew(src, dst, strings.Repeat("0", 64))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mismatch")
	_, statErr := os.Stat(dst)
	assert.True(t, os.IsNotExist(statErr), "the bad copy is gone")
}

// Through the copy path, a link planted at the target name is never written through (O_EXCL: the name exists, so the create fails), and a target that
// cannot be created at all (here: its folder is a file, as an access-denied one would be) is an error, not a quiet success (Security's questions on #109).
func TestPublishNewDoesNotWriteThroughAPlantedLinkAndSurfacesAnUncreatableTarget(t *testing.T) {
	old := linkFile
	defer func() { linkFile = old }()
	linkFile = func(string, string) error { return &os.LinkError{Op: "link", Err: unsupportedLinkErrnos[0]} }
	data := []byte("artifact")
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	src := filepath.Join(t.TempDir(), "src")
	require.NoError(t, os.WriteFile(src, data, 0o600))

	dir := t.TempDir()
	linktest.Each(t, filepath.Join(dir, "planted"), func(t *testing.T, p linktest.Planted) {
		err := publishNew(src, filepath.Join(dir, "planted"), id)
		require.Error(t, err, "%s: the name is taken, whatever it is a link to", p.Kind) // (a file name that exists: ErrExist; a junction to a folder: "is a directory")
	})

	notAFolder := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(notAFolder, []byte("x"), 0o600))
	require.Error(t, publishNew(src, filepath.Join(notAFolder, "out.txt"), id), "a target that cannot be created is an error")
}
