package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/cliauth"
)

// What the tray's exit items say depends on the terminal backend the server runs (#6064, #6131). On the built-in terminal (pty, the default, and always on Windows) quitting Coral stops the server the
// normal way: each running agent is marked and wakes the next time Coral opens, so there is one exit, "Quit Coral", and it says how many agents that is. Ending agents for good is each agent's own Kill. With
// tmux the agents outlive Coral, so there are two: "Quit and stop all agents" and "Quit Coral" (agents keep running).
type exitItems struct {
	oneExit  bool   // the Shutdown item is hidden and Quit stops the server the normal way
	quit     string // Quit's title
	quitTip  string
	shutdown string
	shutTip  string
}

// agentsCount says "1 agent" or "3 agents".
func agentsCount(n int) string {
	if n == 1 {
		return "1 agent"
	}
	return fmt.Sprintf("%d agents", n)
}

// quitNote is what Quit tells the person once it has stopped Coral on the built-in terminal: how many agents will wake next time.
func quitNote(agents int) string {
	switch {
	case agents <= 0:
		return "Coral quit."
	case agents == 1:
		return "Coral quit. 1 agent will wake next time you open Coral."
	}
	return fmt.Sprintf("Coral quit. %d agents will wake next time you open Coral.", agents)
}

// exitItemsFor builds the tray's exit items; agents is how many agents are running now (the pty Quit names them).
func exitItemsFor(goos, backend string, agents int) exitItems {
	if goos == "windows" || backend != "tmux" {
		it := exitItems{oneExit: true, quit: "Quit Coral", quitTip: "Exit Coral",
			shutdown: "Quit Coral", shutTip: "Exit Coral"}
		if agents > 0 {
			verb := "wake"
			if agents == 1 {
				verb = "wakes"
			}
			it.quit = fmt.Sprintf("Quit Coral (%s %s next time)", agentsCount(agents), verb)
			it.quitTip = fmt.Sprintf("%s stop now and %s next time you open Coral", agentsCount(agents), verb)
			if agents == 1 {
				it.quitTip = "1 agent stops now and wakes next time you open Coral"
			}
			it.shutdown, it.shutTip = it.quit, it.quitTip
		}
		return it
	}
	return exitItems{quit: "Quit Coral", quitTip: "Exit Coral. The agents keep running (tmux terminal backend).",
		shutdown: "Quit and stop all agents", shutTip: "Kill all agents, then stop the server and exit"}
}

// runningAgents counts the agents running now: the live sessions that are not asleep. 0 when the server does not answer.
func runningAgents(client *http.Client, baseURL string) int {
	resp, err := client.Get(baseURL + "/api/sessions/live")
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var rows []struct {
		Sleeping bool `json:"sleeping"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rows) != nil {
		return 0
	}
	n := 0
	for _, r := range rows {
		if !r.Sleeping {
			n++
		}
	}
	return n
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

// agentsClient is the tray's authenticated client for the local server.
func agentsClient(dataDir string) *http.Client {
	client := &http.Client{Timeout: 3 * time.Second}
	if tok, err := cliauth.ReadToken(dataDir); err == nil {
		client.Transport = cliauth.Transport{Base: http.DefaultTransport, Token: tok, Dir: dataDir}
	}
	return client
}

// runningAgentsNow counts the agents running right now, for the note Quit shows after it stops Coral.
func runningAgentsNow(baseURL, dataDir string) int {
	return runningAgents(agentsClient(dataDir), baseURL)
}

// watchAgents keeps count current for Quit's title: it asks how many agents run and calls onChange when the number changes.
func watchAgents(stop <-chan struct{}, baseURL, dataDir string, count *atomic.Int64, onChange func()) {
	client := agentsClient(dataDir)
	for {
		select {
		case <-stop:
			return
		case <-time.After(10 * time.Second):
		}
		if n := int64(runningAgents(client, baseURL)); n != count.Swap(n) {
			onChange()
		}
	}
}

// quitKeepingAgentsPlace is Quit on the built-in terminal (#6131): it counts the running agents, closes the app window, stops the server the normal way (each running agent is marked and wakes next
// time; there is no kill call), says how many will wake, and exits the tray.
func quitKeepingAgentsPlace(baseURL, dataDir string, closeApp, stopServer func(), notify func(string), exit func()) {
	running := runningAgentsNow(baseURL, dataDir)
	closeApp()
	stopServer()
	notify(quitNote(running))
	exit()
}
