package claudestub

import (
	"bytes"
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// run starts the stub with a limit: what it answered, how it ended, how long it took, and whether the limit killed it.
func run(t *testing.T, s *Stub, limit time.Duration, stdin string, args ...string) (out, errOut string, code int, took time.Duration, killed bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Path(), args...)
	cmd.Stdin = strings.NewReader(stdin)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	start := time.Now()
	err := cmd.Run()
	took = time.Since(start)
	killed = ctx.Err() != nil
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil && !killed {
		t.Fatal(err)
	}
	return o.String(), e.String(), code, took, killed
}

// killLimit is when a test ends a child that is stuck: generous, because it is not what tells a hold from a plain run (NoSlower does, against a signed-in control), and a busy machine
// starts a script slowly.
const killLimit = 30 * time.Second

// startOf is one run of the stub, for NoSlower.
func startOf(s *Stub, args ...string) func() {
	return func() { _ = exec.Command(s.Path(), args...).Run() }
}

func TestVersionAnswersAtOnceInEveryState(t *testing.T) {
	control := New(t, SignedIn)
	for _, st := range []State{SignedIn, SignedOut, LoginInProgress, HangingAuth} {
		s := New(t, st)
		out, _, code, took, killed := run(t, s, killLimit, "", "--version")
		assert.Equal(t, "2.1.999 (Claude Code)\n", out, st)
		assert.Zero(t, code, st)
		assert.False(t, killed, st)
		_ = took
		if st != SignedIn {
			NoSlower(t, "--version in "+string(st), startOf(control, "--version"), startOf(s, "--version"), time.Second)
		}
		assert.Equal(t, 0, s.Grabs(), st)
		assert.Empty(t, s.PrintRuns(), st)
	}
}

func TestAuthStatusInEachState(t *testing.T) {
	s := New(t, SignedIn)
	control := New(t, SignedIn)
	out, _, code, _, _ := run(t, s, killLimit, "", "auth", "status")
	var j map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &j))
	assert.Equal(t, true, j["loggedIn"])
	assert.Zero(t, code)

	for _, st := range []State{SignedOut, LoginInProgress} {
		s.Set(st)
		out, errOut, code, took, killed := run(t, s, killLimit, "", "auth", "status", "--json")
		assert.Equal(t, 1, code, st)
		assert.Contains(t, out, `"loggedIn":false`, st)
		assert.Contains(t, errOut, "Not logged in", st)
		assert.False(t, killed, st)
		_ = took
		probe := New(t, st) // its own stand-in: s's call log is counted below
		NoSlower(t, "auth status in "+string(st), startOf(control, "auth", "status", "--json"), startOf(probe, "auth", "status", "--json"), time.Second)
	}

	s.Set(HangingAuth)
	_, _, _, took, killed := run(t, s, 700*time.Millisecond, "", "auth", "status")
	assert.True(t, killed, "a hanging `auth status` is ended by the caller's own limit")
	assert.Less(t, took, 2*killLimit)
	assert.Len(t, s.AuthChecks(), 4, "every check is logged, including the hanging one")
	assert.Zero(t, s.Grabs(), "asking for the status never starts a login")
}

func TestPrintModeSignedInAnswersAndReadsItsStdin(t *testing.T) {
	s := New(t, SignedIn)
	out, _, code, _, _ := run(t, s, killLimit, "a prompt", "--print", "--model", "haiku")
	assert.Equal(t, "stub reply\n", out)
	assert.Zero(t, code)
	out, _, _, _, _ = run(t, s, killLimit, "x", "-p", "--output-format", "json")
	var j map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &j))
	assert.Equal(t, "stub reply", j["result"])
	runs := s.PrintRuns()
	require.Len(t, runs, 2)
	assert.False(t, runs[0].TTY, "stdin was not a terminal")
	assert.Equal(t, []string{"--print", "--model", "haiku"}, runs[0].Args)
	assert.Zero(t, s.Grabs())
}

func TestPrintModeSignedOutIsTheBug(t *testing.T) {
	s := New(t, SignedOut)
	out, _, code, took, killed := run(t, s, 2*killLimit, "", "--print", "--model", "sonnet")
	assert.Contains(t, out, "Not logged in")
	assert.Equal(t, 1, code)
	assert.False(t, killed)
	assert.GreaterOrEqual(t, took, 2*time.Second, "it holds the login flow for 2 s before it ends")
	assert.Equal(t, 1, s.Grabs(), "a print-mode run while signed out starts the login flow")
	assert.Len(t, s.PrintRuns(), 1)
	_, err := os.Stat(filepath.Join(s.Dir(), "login.lock"))
	assert.NoError(t, err, "and takes the lock the user's own login needs")
}

func TestPrintModeWhileALoginIsOpenOrAuthHangsWaitsAndIsLogged(t *testing.T) {
	for _, st := range []State{LoginInProgress, HangingAuth} {
		s := New(t, st)
		// Wait for the run to be on record (the stub writes it as it starts, so on a loaded machine that can take a while: no fixed limit), then show it is still waiting behind the login a moment
		// later, and end it. A run killed before it recorded anything proved nothing (#5633).
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel() // a failing wait below must not leave the stub's sleeper running
		cmd := exec.CommandContext(ctx, s.Path(), "-p", "--model", "haiku")
		cmd.Stdin = strings.NewReader("")
		require.NoError(t, cmd.Start(), st)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		require.Eventually(t, func() bool { return len(s.PrintRuns()) == 1 }, 15*time.Second, 10*time.Millisecond, "%s: the run is on record", st)
		select {
		case err := <-done:
			cancel()
			t.Fatalf("%s: the run ended instead of waiting behind the login: %v", st, err)
		case <-time.After(500 * time.Millisecond):
		}
		cancel()
		<-done
		assert.Len(t, s.PrintRuns(), 1, "%s: one run on record", st)
		assert.Zero(t, s.Grabs(), "%s: it waited behind the login, it did not start one", st)
	}
}

func TestStateChangesBetweenCallsAndLoginLockFollowsIt(t *testing.T) {
	s := New(t, LoginInProgress)
	lock := filepath.Join(s.Dir(), "login.lock")
	_, err := os.Stat(lock)
	assert.NoError(t, err, "the user's own login holds the lock")
	s.Set(SignedIn)
	_, err = os.Stat(lock)
	assert.True(t, os.IsNotExist(err), "signing in releases it")
	out, _, _, _, _ := run(t, s, killLimit, "x", "--print")
	assert.Equal(t, "stub reply\n", out)
	calls := s.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, SignedIn, calls[0].State)
}

func TestIsolateMakesTheRealCLIUnreachable(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-not-a-real-key")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "not-a-token")
	home := Isolate(t)
	_, err := exec.LookPath("claude")
	assert.Error(t, err, "no claude on the isolated PATH")
	assert.Empty(t, os.Getenv("ANTHROPIC_API_KEY"))
	assert.Empty(t, os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"))
	assert.Equal(t, home, os.Getenv("HOME"))
	assert.Equal(t, "/usr/bin:/bin", os.Getenv("PATH"))
}

// No file that is not a test imports this package: it is test support, like boardtest and storetest.
func TestNoNonTestFileImportsTheStub(t *testing.T) {
	root := filepath.Join("..", "..")
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); path != root && (strings.HasPrefix(n, ".") || n == "node_modules" || n == "testdata" || n == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(filepath.ToSlash(path), "internal/claudestub/") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return nil
		}
		for _, im := range f.Imports {
			if strings.HasSuffix(strings.Trim(im.Path.Value, `"`), "/internal/claudestub") {
				offenders = append(offenders, filepath.ToSlash(path))
			}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, offenders, "a non-test file imports the claude stand-in")
}

// #5475 (Security): the PATH is not the only way Coral finds a CLI. FindCLI falls back to the common install folders by absolute path (/usr/local/bin, /opt/homebrew/bin), which
// Isolate cannot hide, so a machine that has a real claude there makes the matrix skip rather than risk running it (#4354).
func TestRealCLIReachableSeesTheCommonInstallLocations(t *testing.T) {
	Isolate(t)
	old := findCLIInCommonPaths
	t.Cleanup(func() { findCLIInCommonPaths = old })

	findCLIInCommonPaths = func(string) string { return "" }
	assert.Empty(t, RealCLIReachable(), "nothing installed anywhere")

	findCLIInCommonPaths = func(bin string) string { return "/opt/homebrew/bin/" + bin }
	assert.Equal(t, "/opt/homebrew/bin/claude", RealCLIReachable(), "a claude in a common install folder is reachable")
}
