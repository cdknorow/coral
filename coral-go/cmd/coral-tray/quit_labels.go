package main

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/cliauth"
)

// What the tray's exit items say depends on the terminal backend the server runs (#6064). On the built-in terminal (pty, the default, and always on Windows) quitting Coral ends its agents, so
// there is one exit, "Quit and stop all agents". With tmux the agents outlive Coral, so there are two: "Quit and stop all agents" and "Quit Coral" (agents keep running).
type exitItems struct {
	oneExit  bool   // the Shutdown item is hidden and Quit stops the agents
	quit     string // Quit's title
	quitTip  string
	shutdown string
	shutTip  string
}

func exitItemsFor(goos, backend string) exitItems {
	if goos == "windows" || backend != "tmux" {
		return exitItems{oneExit: true, quit: "Quit and stop all agents", quitTip: "Stop all agents and exit Coral",
			shutdown: "Quit and stop all agents", shutTip: "Stop all agents and exit Coral"}
	}
	return exitItems{quit: "Quit Coral", quitTip: "Exit Coral. The agents keep running (tmux terminal backend).",
		shutdown: "Quit and stop all agents", shutTip: "Kill all agents, then stop the server and exit"}
}

// runningBackend reads the backend the server runs from a terminal-backend answer: "tmux" or "pty", "" when the answer says neither.
func runningBackend(body []byte) string {
	var st struct {
		Running string `json:"running"`
	}
	if json.Unmarshal(body, &st) != nil || (st.Running != "tmux" && st.Running != "pty") {
		return ""
	}
	return st.Running
}

// watchBackend keeps backend current: it asks the server which backend it runs (the server decides: --backend, the saved choice, the default) and asks again when the server restarts. Until
// the first answer it holds the default, pty. onChange is called when the answer changes.
func watchBackend(stop <-chan struct{}, baseURL, dataDir string, backend *atomic.Value, onChange func(string)) {
	client := &http.Client{Timeout: 3 * time.Second}
	if tok, err := cliauth.ReadToken(dataDir); err == nil {
		client.Transport = cliauth.Transport{Base: http.DefaultTransport, Token: tok, Dir: dataDir}
	}
	ask := func() {
		resp, err := client.Get(baseURL + "/api/system/terminal-backend")
		if err != nil {
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return
		}
		buf := make([]byte, 0, 1024)
		tmp := make([]byte, 1024)
		for len(buf) < 64*1024 {
			n, err := resp.Body.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if err != nil {
				break
			}
		}
		if b := runningBackend(buf); b != "" && b != backend.Load().(string) {
			backend.Store(b)
			onChange(b)
		}
	}
	every := 2 * time.Second
	for {
		select {
		case <-stop:
			return
		case <-time.After(every):
		}
		ask()
		every = 15 * time.Second // the first answers come while the server starts; then a restart is noticed within seconds
	}
}
