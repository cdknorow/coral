package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/clitext"
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

// wakeRule is Settings > Terminal "Wake agents after a restart" and its cap: Coral wakes the agents it stopped only when the setting is on and no more than cap were running (above the cap it asks first).
type wakeRule struct {
	on  bool
	cap int
}

// defaultWakeRule is what Coral does when the setting was never saved: on, up to 8.
var defaultWakeRule = wakeRule{on: true, cap: 8}

// wakes says whether the agents running now will wake by themselves next time. Over the cap Coral asks first, so the tray says "stop" there, not "wake".
func (w wakeRule) wakes(agents int) bool { return w.on && agents <= w.cap }

// wakeRuleFrom reads the rule from a GET /api/settings answer; a missing or odd value is the default.
func wakeRuleFrom(body []byte) (wakeRule, bool) {
	var a struct {
		Settings map[string]string `json:"settings"`
	}
	if json.Unmarshal(body, &a) != nil || a.Settings == nil {
		return defaultWakeRule, false
	}
	r := defaultWakeRule
	if a.Settings["auto_wake_after_restart"] == "false" {
		r.on = false
	}
	if n, err := strconv.Atoi(a.Settings["auto_wake_cap"]); err == nil && n >= 1 && n <= 50 {
		r.cap = n
	}
	return r, true
}

// quitNote is what Quit tells the person once it has stopped Coral on the built-in terminal: how many agents will wake next time, or that they stopped.
func quitNote(agents int, wakes bool) string { return quitNoteWhy(agents, wakes, "") }

// quitNoteWhy is quitNote with the reason the agents will not wake when the server's crash-loop guard is the reason (why is the guard's own words: "Coral woke its agents less than 10 minutes ago").
func quitNoteWhy(agents int, wakes bool, why string) string {
	switch {
	case agents <= 0:
		return "Coral quit."
	case !wakes && why != "" && agents == 1:
		return "Coral quit. 1 agent stopped: " + why + ". Wake it from Coral when you want it back."
	case !wakes && why != "":
		return fmt.Sprintf("Coral quit. %d agents stopped: %s. Wake them from Coral when you want them back.", agents, why)
	case !wakes && agents == 1:
		return "Coral quit. 1 agent stopped. Wake it from Coral when you want it back."
	case !wakes:
		return fmt.Sprintf("Coral quit. %d agents stopped. Wake them from Coral when you want them back.", agents)
	case agents == 1:
		return "Coral quit. 1 agent will wake next time you open Coral."
	}
	return fmt.Sprintf("Coral quit. %d agents will wake next time you open Coral.", agents)
}

// exitItemsFor builds the tray's exit items; agents is how many agents are running now and wakes whether they wake by themselves next time (the pty Quit says which).
func exitItemsFor(goos, backend string, agents int, wakes bool) exitItems {
	return exitItemsWhy(goos, backend, agents, wakes, "")
}

// exitItemsWhy is exitItemsFor with the reason the agents will not wake when the crash-loop guard is the reason (#319): the title and the tip say so in the server's words,
// not "wake next time".
func exitItemsWhy(goos, backend string, agents int, wakes bool, why string) exitItems {
	if goos == "windows" || backend != "tmux" {
		it := exitItems{oneExit: true, quit: "Quit Coral", quitTip: "Exit Coral",
			shutdown: "Quit Coral", shutTip: "Exit Coral"}
		switch {
		case agents <= 0:
		case !wakes && why != "" && agents == 1:
			it.quit, it.quitTip = "Quit Coral (1 agent stops: "+why+")", "1 agent stops now: "+why+". Wake it from Coral when you want it back."
		case !wakes && why != "":
			it.quit, it.quitTip = fmt.Sprintf("Quit Coral (%d agents stop: %s)", agents, why), fmt.Sprintf("%d agents stop now: %s. Wake them from Coral when you want them back.", agents, why)
		case wakes && agents == 1:
			it.quit, it.quitTip = "Quit Coral (1 agent wakes next time)", "1 agent stops now and wakes next time you open Coral"
		case wakes:
			it.quit, it.quitTip = fmt.Sprintf("Quit Coral (%d agents wake next time)", agents), fmt.Sprintf("%d agents stop now and wake next time you open Coral", agents)
		case agents == 1:
			it.quit, it.quitTip = "Quit Coral (1 agent stops)", "1 agent stops now. Wake it from Coral when you want it back."
		default:
			it.quit, it.quitTip = fmt.Sprintf("Quit Coral (%d agents stop)", agents), fmt.Sprintf("%d agents stop now. Wake them from Coral when you want them back.", agents)
		}
		it.shutdown, it.shutTip = it.quit, it.quitTip
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

// wakeRuleNow reads the wake setting from the server. ok is false when it could not be read (no answer, not signed in, a refusal, not JSON): the tray then does not promise waking.
func wakeRuleNow(client *http.Client, baseURL string) (wakeRule, bool) {
	resp, err := client.Get(baseURL + "/api/settings")
	if err != nil {
		return defaultWakeRule, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return defaultWakeRule, false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return wakeRuleFrom(body)
}

// agentsNow is what the tray says about the agents: how many run and whether they wake next time.
type agentsNow struct {
	count int
	wakes bool
	why   string // when the agents will not wake because of the server's crash-loop guard: its words
}

// wakeGuardNow reads the crash-loop guard from the server (GET /api/system/auto-wake): whether a Coral started now would skip the wake, and the reason in the server's own words.
func wakeGuardNow(client *http.Client, baseURL string) (guarded bool, why string) {
	resp, err := client.Get(baseURL + "/api/system/auto-wake")
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, ""
	}
	var st struct {
		GuardedNow bool   `json:"guarded_now"`
		GuardWhy   string `json:"guard_why"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&st) != nil || !st.GuardedNow {
		return false, ""
	}
	// The words go into a menu title, a tooltip and a notification: whoever answers on the port (only Coral holds it) can change text, not more; cleaned and capped (Security, #144).
	why = clitext.CleanName(st.GuardWhy)
	if r := []rune(why); len(r) > 100 {
		why = string(r[:100])
	}
	return true, why
}

// readAgentsNow asks the server for both.
func readAgentsNow(baseURL, dataDir string) agentsNow {
	client := agentsClient(dataDir)
	n := runningAgents(client, baseURL)
	rule, ok := wakeRuleNow(client, baseURL)
	wakes := ok && rule.wakes(n)
	if wakes && n > 0 {
		// The setting says wake, but the crash-loop guard skips a wake that follows a recent one: say so, as the dashboard's notice does.
		if guarded, why := wakeGuardNow(client, baseURL); guarded {
			return agentsNow{count: n, wakes: false, why: why}
		}
	}
	return agentsNow{count: n, wakes: wakes}
}

// watchAgents keeps cur current for Quit's title: it asks every 10 seconds and calls onChange when the answer changes.
func watchAgents(stop <-chan struct{}, baseURL, dataDir string, cur *atomic.Value, onChange func()) {
	for {
		select {
		case <-stop:
			return
		case <-time.After(10 * time.Second):
		}
		if n := readAgentsNow(baseURL, dataDir); n != cur.Swap(n).(agentsNow) {
			onChange()
		}
	}
}

// quitKeepingAgentsPlace is Quit on the built-in terminal (#6131): it reads the running agents and whether they wake, closes the app window, stops the server the normal way (each running agent is
// marked and wakes next time when the setting allows; there is no kill call), says what happened, and exits the tray.
func quitKeepingAgentsPlace(baseURL, dataDir string, closeApp, stopServer func(), notify func(string), exit func()) {
	a := readAgentsNow(baseURL, dataDir)
	closeApp()
	stopServer()
	notify(quitNoteWhy(a.count, a.wakes, a.why))
	exit()
}
