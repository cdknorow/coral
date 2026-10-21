package background

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/terminalinput"
)

// newTestPTYRuntime creates a PTYRuntime backed by a fresh PTYBackend.
func newTestPTYRuntime() *PTYRuntime {
	return NewPTYRuntime(ptymanager.NewPTYBackend())
}

// skipIfNoPTY skips the test if PTY spawning is not available (e.g. sandbox, CI).
func skipIfNoPTY(t *testing.T) {
	t.Helper()
	rt := newTestPTYRuntime()
	err := rt.SpawnAgent(context.Background(), "test-probe-aaaaaaaa-bbbb-cccc-dddd-000000000000", t.TempDir(), "", "true")
	if err != nil {
		t.Skipf("PTY spawning not available: %v", err)
		return
	}
	rt.KillAgent(context.Background(), "test-probe-aaaaaaaa-bbbb-cccc-dddd-000000000000")
}

const testUUID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

// --- parseSessionName tests (pure logic, no PTY needed) ---

func TestParseSessionName_ValidFormat(t *testing.T) {
	agentType, sessionID := parseSessionName("claude-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if agentType != "claude" {
		t.Errorf("agentType = %q, want %q", agentType, "claude")
	}
	if sessionID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("sessionID = %q, want UUID", sessionID)
	}
}

func TestParseSessionName_InvalidFormat(t *testing.T) {
	agentType, sessionID := parseSessionName("plain-name")
	if agentType != "" || sessionID != "" {
		t.Errorf("expected empty for invalid name, got type=%q id=%q", agentType, sessionID)
	}
}

func TestParseSessionName_NoHyphen(t *testing.T) {
	agentType, sessionID := parseSessionName("nohyphen")
	if agentType != "" || sessionID != "" {
		t.Errorf("expected empty for no-hyphen name, got type=%q id=%q", agentType, sessionID)
	}
}

func TestParseSessionName_ShortUUID(t *testing.T) {
	// UUID part is too short (< 36 chars)
	agentType, sessionID := parseSessionName("claude-short-id")
	if agentType != "" || sessionID != "" {
		t.Errorf("expected empty for short UUID, got type=%q id=%q", agentType, sessionID)
	}
}

func TestParseSessionName_BadUUIDFormat(t *testing.T) {
	// 36 chars but wrong format (no hyphens at positions 8, 13)
	agentType, sessionID := parseSessionName("claude-aaaaaaa.bbbb.cccc.dddd.eeeeeeeeeeee")
	if agentType != "" || sessionID != "" {
		t.Errorf("expected empty for bad UUID format, got type=%q id=%q", agentType, sessionID)
	}
}

func TestFormatSessionName(t *testing.T) {
	name := FormatSessionName("claude", testUUID)
	expected := "claude-" + testUUID
	if name != expected {
		t.Errorf("FormatSessionName = %q, want %q", name, expected)
	}
}

func TestFormatSessionName_Roundtrip(t *testing.T) {
	name := FormatSessionName("gemini", testUUID)
	agentType, sessionID := parseSessionName(name)
	if agentType != "gemini" {
		t.Errorf("roundtrip agentType = %q, want %q", agentType, "gemini")
	}
	if sessionID != testUUID {
		t.Errorf("roundtrip sessionID = %q, want %q", sessionID, testUUID)
	}
}

// --- AgentRuntime interface compliance (compile-time, no PTY needed) ---

func TestPTYRuntime_ImplementsAgentRuntime(t *testing.T) {
	var _ AgentRuntime = (*PTYRuntime)(nil)
	var _ AgentRuntime = (*TmuxRuntime)(nil)
}

// --- PTYRuntime pure-logic tests (no PTY spawn) ---

func TestPTYRuntime_IsAlive_NotExists(t *testing.T) {
	rt := newTestPTYRuntime()
	ctx := context.Background()

	if rt.IsAlive(ctx, "nonexistent-agent") {
		t.Error("expected false for nonexistent agent")
	}
}

func TestPTYRuntime_KillAgent_NotExists(t *testing.T) {
	rt := newTestPTYRuntime()
	ctx := context.Background()

	err := rt.KillAgent(ctx, "nonexistent-agent")
	if err == nil {
		t.Error("expected error when killing nonexistent agent")
	}
}

func TestPTYRuntime_SendInput_NotExists(t *testing.T) {
	rt := newTestPTYRuntime()
	ctx := context.Background()

	err := rt.SendInput(ctx, terminalinput.Unclassified, "nonexistent-agent", "hello")
	if err == nil {
		t.Error("expected error when sending to nonexistent agent")
	}
}

func TestPTYRuntime_ListAgents_Empty(t *testing.T) {
	rt := newTestPTYRuntime()
	ctx := context.Background()

	agents, err := rt.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents failed: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

// --- PTYRuntime.SpawnAgent tests (require PTY) ---

func TestPTYRuntime_SpawnAgent(t *testing.T) {
	skipIfNoPTY(t)
	rt := newTestPTYRuntime()
	ctx := context.Background()
	name := FormatSessionName("claude", testUUID)

	err := rt.SpawnAgent(ctx, name, t.TempDir(), "", "echo hello")
	if err != nil {
		t.Fatalf("SpawnAgent failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	agents, err := rt.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents failed: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].AgentType != "claude" {
		t.Errorf("AgentType = %q, want %q", agents[0].AgentType, "claude")
	}
	if agents[0].SessionID != testUUID {
		t.Errorf("SessionID = %q, want %q", agents[0].SessionID, testUUID)
	}

	rt.KillAgent(ctx, name)
}

func TestPTYRuntime_SpawnAgent_DuplicateName(t *testing.T) {
	skipIfNoPTY(t)
	rt := newTestPTYRuntime()
	ctx := context.Background()
	name := FormatSessionName("claude", testUUID)

	err := rt.SpawnAgent(ctx, name, t.TempDir(), "", "sleep 10")
	if err != nil {
		t.Fatalf("first SpawnAgent failed: %v", err)
	}
	defer rt.KillAgent(ctx, name)

	err = rt.SpawnAgent(ctx, name, t.TempDir(), "", "sleep 10")
	if err == nil {
		t.Fatal("expected error on duplicate spawn, got nil")
	}
}

func TestPTYRuntime_SpawnAgent_SimpleNameFallback(t *testing.T) {
	skipIfNoPTY(t)
	rt := newTestPTYRuntime()
	ctx := context.Background()

	// Name without valid UUID — should still work (uses name as sessionID)
	err := rt.SpawnAgent(ctx, "simple-name", t.TempDir(), "", "echo ok")
	if err != nil {
		t.Fatalf("SpawnAgent with simple name failed: %v", err)
	}
	defer rt.KillAgent(ctx, "simple-name")
}

// --- PTYRuntime.IsAlive tests (require PTY) ---

func TestPTYRuntime_IsAlive_Running(t *testing.T) {
	skipIfNoPTY(t)
	rt := newTestPTYRuntime()
	ctx := context.Background()
	name := FormatSessionName("claude", testUUID)

	err := rt.SpawnAgent(ctx, name, t.TempDir(), "", "sleep 30")
	if err != nil {
		t.Fatalf("SpawnAgent failed: %v", err)
	}
	defer rt.KillAgent(ctx, name)

	time.Sleep(200 * time.Millisecond)

	if !rt.IsAlive(ctx, name) {
		t.Error("expected agent to be alive")
	}
}

// --- PTYRuntime.KillAgent tests (require PTY) ---

func TestPTYRuntime_KillAgent(t *testing.T) {
	skipIfNoPTY(t)
	rt := newTestPTYRuntime()
	ctx := context.Background()
	name := FormatSessionName("claude", testUUID)

	err := rt.SpawnAgent(ctx, name, t.TempDir(), "", "sleep 30")
	if err != nil {
		t.Fatalf("SpawnAgent failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	err = rt.KillAgent(ctx, name)
	if err != nil {
		t.Fatalf("KillAgent failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	if rt.IsAlive(ctx, name) {
		t.Error("expected agent to be dead after kill")
	}
}

// --- PTYRuntime.SendInput tests (require PTY) ---

func TestPTYRuntime_SendInput(t *testing.T) {
	skipIfNoPTY(t)
	rt := newTestPTYRuntime()
	ctx := context.Background()
	name := FormatSessionName("claude", testUUID)

	err := rt.SpawnAgent(ctx, name, t.TempDir(), "", "cat")
	if err != nil {
		t.Fatalf("SpawnAgent failed: %v", err)
	}
	defer rt.KillAgent(ctx, name)

	time.Sleep(300 * time.Millisecond)

	err = rt.SendInput(ctx, terminalinput.Unclassified, name, "hello world")
	if err != nil {
		t.Fatalf("SendInput failed: %v", err)
	}
}

// --- PTYRuntime.ListAgents tests (require PTY) ---

func TestPTYRuntime_ListAgents_Multiple(t *testing.T) {
	skipIfNoPTY(t)
	rt := newTestPTYRuntime()
	ctx := context.Background()

	uuid1 := "11111111-2222-3333-4444-555555555555"
	uuid2 := "66666666-7777-8888-9999-aaaaaaaaaaaa"

	name1 := FormatSessionName("claude", uuid1)
	name2 := FormatSessionName("gemini", uuid2)

	if err := rt.SpawnAgent(ctx, name1, t.TempDir(), "", "sleep 30"); err != nil {
		t.Fatalf("SpawnAgent 1 failed: %v", err)
	}
	defer rt.KillAgent(ctx, name1)

	if err := rt.SpawnAgent(ctx, name2, t.TempDir(), "", "sleep 30"); err != nil {
		t.Fatalf("SpawnAgent 2 failed: %v", err)
	}
	defer rt.KillAgent(ctx, name2)

	time.Sleep(300 * time.Millisecond)

	agents, err := rt.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents failed: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}

	types := map[string]bool{}
	for _, a := range agents {
		types[a.AgentType] = true
	}
	if !types["claude"] || !types["gemini"] {
		t.Errorf("expected claude and gemini, got types: %v", types)
	}
}

// --- Concurrent operations (require PTY) ---

func TestPTYRuntime_ConcurrentSpawnKill(t *testing.T) {
	skipIfNoPTY(t)
	rt := newTestPTYRuntime()
	ctx := context.Background()

	const numAgents = 5
	var wg sync.WaitGroup

	names := make([]string, numAgents)
	for i := 0; i < numAgents; i++ {
		names[i] = FormatSessionName("agent", testUUIDWithSuffix(i))
	}

	// Spawn concurrently
	wg.Add(numAgents)
	for i := 0; i < numAgents; i++ {
		go func(idx int) {
			defer wg.Done()
			rt.SpawnAgent(ctx, names[idx], t.TempDir(), "", "sleep 30")
		}(i)
	}
	wg.Wait()

	time.Sleep(300 * time.Millisecond)

	agents, _ := rt.ListAgents(ctx)
	if len(agents) != numAgents {
		t.Errorf("expected %d agents after concurrent spawn, got %d", numAgents, len(agents))
	}

	// Kill concurrently
	wg.Add(numAgents)
	for i := 0; i < numAgents; i++ {
		go func(idx int) {
			defer wg.Done()
			rt.KillAgent(ctx, names[idx])
		}(i)
	}
	wg.Wait()

	time.Sleep(300 * time.Millisecond)

	agents, _ = rt.ListAgents(ctx)
	if len(agents) != 0 {
		t.Errorf("expected 0 agents after concurrent kill, got %d", len(agents))
	}
}

// --- Helpers ---

func testUUIDWithSuffix(i int) string {
	return fmt.Sprintf("aaaaaaaa-bbbb-cccc-dddd-%012d", i)
}
