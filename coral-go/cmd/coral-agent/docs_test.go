package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// docsServer answers the three agent-docs routes the way the server does, and refuses a request without
// the session token like strict local auth does (a plain curl has none).
func docsServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Coral-Session-Token") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"code":"dashboard_auth_required","error":"Coral is locked"}`))
			return
		}
		switch r.URL.Path {
		case "/api/agent-docs":
			w.Write([]byte(`[{"name":"README","title":"README"},{"name":"board","title":"Board"}]`))
		case "/api/agent-docs/board":
			w.Write([]byte(`{"name":"board","content":"# Board\n\nUse coral-board post.\n","path":"agent_docs/board.md"}`))
		case "/api/agent-docs/all":
			w.Write([]byte("# README\n\n---\n\n# Board\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"doc not found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runAgentWithToken runs coral-agent as an agent session does: its session token in the environment.
func runAgentWithToken(t *testing.T, serverURL string, args ...string) (int, string, string) {
	t.Helper()
	return runAgentEnv(t, serverURL, []string{"CORAL_SESSION_TOKEN=test-session-token"}, args...)
}

func TestDocsCommand(t *testing.T) {
	srv := docsServer(t)
	run := func(args ...string) (int, string, string) {
		return runAgent(t, srv.URL, append([]string{"docs"}, args...)...)
	}
	t.Run("lists the topics", func(t *testing.T) {
		code, out, errOut := runAgentWithToken(t, srv.URL, "docs")
		if code != 0 || !strings.Contains(out, "board") || !strings.Contains(out, "coral-agent docs <topic>") {
			t.Fatalf("exit %d\nstdout: %q\nstderr: %q", code, out, errOut)
		}
	})
	t.Run("prints one topic's text", func(t *testing.T) {
		code, out, errOut := runAgentWithToken(t, srv.URL, "docs", "board")
		if code != 0 || out != "# Board\n\nUse coral-board post.\n" {
			t.Fatalf("exit %d\nstdout: %q\nstderr: %q", code, out, errOut)
		}
	})
	t.Run("a topic may be named with its file extension", func(t *testing.T) {
		code, out, _ := runAgentWithToken(t, srv.URL, "docs", "board.md")
		if code != 0 || !strings.HasPrefix(out, "# Board") {
			t.Fatalf("exit %d: %q", code, out)
		}
	})
	t.Run("all prints everything", func(t *testing.T) {
		code, out, _ := runAgentWithToken(t, srv.URL, "docs", "all")
		if code != 0 || !strings.Contains(out, "# README") || !strings.Contains(out, "# Board") {
			t.Fatalf("exit %d: %q", code, out)
		}
	})
	t.Run("an unknown topic fails and lists the topics", func(t *testing.T) {
		code, out, errOut := runAgentWithToken(t, srv.URL, "docs", "nope")
		if code == 0 || out != "" || !strings.Contains(errOut, `no Coral doc named "nope"`) || !strings.Contains(errOut, "board") {
			t.Fatalf("exit %d\nstdout: %q\nstderr: %q", code, out, errOut)
		}
	})
	t.Run("a path-shaped topic never leaves the docs route", func(t *testing.T) {
		code, _, errOut := runAgentWithToken(t, srv.URL, "docs", "../sessions/live")
		if code == 0 || !strings.Contains(errOut, "no Coral doc named") {
			t.Fatalf("exit %d: %q", code, errOut)
		}
	})
	t.Run("without the session token Coral's refusal is shown", func(t *testing.T) {
		code, out, errOut := run("board")
		if code == 0 || out != "" || !strings.Contains(errOut, "401") {
			t.Fatalf("exit %d\nstdout: %q\nstderr: %q", code, out, errOut)
		}
	})
	t.Run("two topics are a usage error", func(t *testing.T) {
		code, _, errOut := run("a", "b")
		if code == 0 || !strings.Contains(errOut, "Usage: coral-agent docs") {
			t.Fatalf("exit %d: %q", code, errOut)
		}
	})
	t.Run("terminal control in a doc is removed", func(t *testing.T) {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"content":"hi\u001b]0;title\u0007 there"}`))
		}))
		defer bad.Close()
		code, out, _ := runAgentWithToken(t, bad.URL, "docs", "x")
		if code != 0 || strings.ContainsRune(out, 0x1b) {
			t.Fatalf("exit %d: %q", code, out)
		}
	})
}
