package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentsignin"
)

// The test binary is its own mock: with MOCK_AGENT_TEST_MAIN=1 it runs main(), so a test can start "the mock" with real arguments and watch what it does.
func TestMain(m *testing.M) {
	if os.Getenv("MOCK_AGENT_TEST_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// #5330: a mock-agent that went on into its agent loop on "--version" stayed running after Coral's version probe, in every suite that launched mocks.
func TestTheVersionAndHelpCallsAnswerAndExit(t *testing.T) {
	t.Setenv("MOCK_AGENT_TEST_MAIN", "1")
	for _, arg := range []string{"--version", "-v", "-V", "--help", "-h"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var out bytes.Buffer
		cmd := exec.CommandContext(ctx, os.Args[0], arg)
		cmd.Stdout = &out
		start := time.Now()
		err := cmd.Run()
		timedOut := ctx.Err() != nil
		cancel()
		if err != nil || timedOut {
			t.Fatalf("mock-agent %s: err %v after %v (it must exit 0 at once)", arg, err, time.Since(start))
		}
		if strings.TrimSpace(out.String()) == "" {
			t.Fatalf("mock-agent %s printed nothing", arg)
		}
	}
}

// The line carries no version number, so the sessions of a mock keep the empty cli_version they have always had (agent.VersionNumber is what Coral records), and the probe
// itself returns at once instead of waiting out its 10 second timeout.
func TestCoralsVersionProbeOfTheMockReturnsAtOnceAndEmpty(t *testing.T) {
	t.Setenv("MOCK_AGENT_TEST_MAIN", "1")
	start := time.Now()
	got := agent.CLIVersion(map[string]string{"cli_path_claude": os.Args[0]}, "claude")
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("the version probe took %v: the mock did not exit on --version", took)
	}
	if got != "" {
		t.Fatalf("the mock reported the version %q; it must report none", got)
	}
}

func TestOnlyALoneInfoFlagIsAnInfoCall(t *testing.T) {
	for _, args := range [][]string{nil, {"--session-id", "x"}, {"--version", "--session-id", "x"}, {"--session-id", "x", "-h"}, {"-p"}, {"version"}, {"--versions"}} {
		var out bytes.Buffer
		if answerInfoCall(args, &out) || out.Len() != 0 {
			t.Fatalf("%q was taken for an info call", args)
		}
	}
}

// #5838: Coral asks "claude auth status --json" once after it launches a Claude agent, and background jobs ask it too (#5469). A mock that went on into its agent loop would stay
// running and register like a launched agent; it answers "signed in" and exits, as a CLI does.
func TestTheSignInCheckAnswersSignedInAndExits(t *testing.T) {
	t.Setenv("MOCK_AGENT_TEST_MAIN", "1")
	for _, args := range [][]string{{"auth", "status", "--json"}, {"auth", "status"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var out bytes.Buffer
		cmd := exec.CommandContext(ctx, os.Args[0], args...)
		cmd.Stdout = &out
		err := cmd.Run()
		timedOut := ctx.Err() != nil
		cancel()
		if err != nil || timedOut {
			t.Fatalf("mock-agent %q: err %v (it must exit 0 at once)", args, err)
		}
		if !strings.Contains(out.String(), `"loggedIn": true`) {
			t.Fatalf("mock-agent %q printed %q", args, out.String())
		}
	}
	// the answer comes from Coral's own check (with no credential in the environment or the home folder that would make it say Unknown)
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		t.Setenv(k, "")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if st := agentsignin.Check(context.Background(), "claude", os.Args[0]); st != agentsignin.SignedIn {
		t.Fatalf("Coral's sign-in check of the mock found %v, want signed in", st)
	}
	for _, args := range [][]string{{"auth"}, {"auth", "login"}, {"auth", "status", "--x"}, {"--session-id", "x", "auth", "status"}} {
		var out bytes.Buffer
		if answerInfoCall(args, &out) {
			t.Fatalf("%q was taken for the sign-in check", args)
		}
	}
}
