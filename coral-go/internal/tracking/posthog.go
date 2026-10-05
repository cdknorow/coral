// Package tracking provides anonymous install/upgrade tracking via PostHog.
// All tracking is non-blocking, fire-and-forget, and never affects app behavior.
package tracking

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cdknorow/coral/internal/config"
	"github.com/google/uuid"
)

// posthogURL is the capture endpoint. It is a var so tests can point it at a
// local server; production never reassigns it.
var posthogURL = "https://us.i.posthog.com/capture/"

var (
	cachedInstallID string
	installIDMu     sync.RWMutex
	installIDOnce   sync.Once
	// coralDir is where all tracking state lives. It has no default: until
	// SetCoralDir is called this package reads nothing, writes nothing, and
	// sends nothing. See resolveCoralDir for why there is no fallback.
	coralDir string

	// asyncWG tracks in-flight tracking goroutines so tests can wait on them.
	asyncWG           sync.WaitGroup
	telemetryDisabled atomic.Bool
)

// SetCoralDir sets the data directory used for tracking state files. Call it
// as early as possible, before anything can read tracking state — until it is
// called this package is inert and keeps no state at all.
func SetCoralDir(dir string) {
	coralDir = dir
}

// SetTelemetryEnabled controls all PostHog work for this process. It is
// deliberately independent of the build-time key so an operator can opt out
// without changing unrelated application behavior.
func SetTelemetryEnabled(enabled bool) {
	if telemetryDisabled.Swap(!enabled) != !enabled {
		// Any transition invalidates work created under the old setting, so an
		// opt-out followed by an opt-in while a retry sleeps cannot revive it.
		telemetryGen.Add(1)
		if !enabled {
			clearPendingMilestones()
		}
	}
}

// telemetryGen is bumped whenever the telemetry setting changes. Each queued
// event remembers the generation it was created in and is dropped, not sent,
// if the generation has moved on.
var telemetryGen atomic.Uint64

// telemetryValid reports whether work created in generation gen may still be sent.
func telemetryValid(gen uint64) bool { return telemetryEnabled() && telemetryGen.Load() == gen }

// envTelemetryDisabled names the environment override. When set to a true
// value it disables all telemetry for the process, overriding a saved opt-in.
// Automated tests, CI and release verification set it so executing a keyed
// binary can never reach the analytics service.
const envTelemetryDisabled = "CORAL_TELEMETRY_DISABLED"

// TelemetryEnvDisabled reports whether CORAL_TELEMETRY_DISABLED is set to a true value.
func TelemetryEnvDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envTelemetryDisabled))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// telemetryEnabled is checked before every send and before every retry, so an
// opt-out takes effect immediately and queued work is dropped, never replayed.
func telemetryEnabled() bool { return !telemetryDisabled.Load() && !TelemetryEnvDisabled() }

// CoralDir returns the data directory tracking state is written to, or "" if
// it has not been configured. Exposed so the telemetry disclosure can show the
// user exactly where their install ID and failure log live.
func CoralDir() string { return resolveCoralDir() }

// getInstallID returns the install ID, reading from disk once and caching.
func getInstallID() string {
	installIDOnce.Do(func() {
		if !stateDirReady() {
			return
		}
		installIDMu.Lock()
		cachedInstallID = readFile(filepath.Join(resolveCoralDir(), ".install_id"))
		installIDMu.Unlock()
	})
	installIDMu.RLock()
	defer installIDMu.RUnlock()
	return cachedInstallID
}

// TrackInstallAsync checks for first install or version upgrade and sends
// an event to PostHog. Also sends an 'app_opened' heartbeat for DAU.
// Runs in a goroutine, never blocks.
func TrackInstallAsync() {
	if config.PostHogKey == "" || !telemetryEnabled() {
		return
	}
	gen := telemetryGen.Load()
	asyncGo(func() {
		trackInstall(gen)
		// Always send app_opened for DAU tracking
		emitEvent(EventAppOpened, nil, uuid.NewString(), nowUTC(), gen)
		// Retention: fire returned_24h once, on the first open >24h after the first.
		trackReturnVisitSync(gen)
	})
}

// TrackEvent sends a named event to PostHog with optional extra properties.
// Non-blocking — runs in a goroutine. Safe to call from any context.
func TrackEvent(eventName string, extraProps map[string]string) {
	if !telemetryEnabled() {
		return
	}
	gen, ts, id := telemetryGen.Load(), nowUTC(), uuid.NewString()
	asyncGo(func() { emitEvent(eventName, extraProps, id, ts, gen) })
}

// standardProps are attached to every event. run_id groups one process's
// events; it is random, unpersisted and unrelated to the install ID.
func standardProps() map[string]any {
	return map[string]any{
		"version":        config.Version,
		"edition":        config.TierName,
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"schema_version": SchemaVersion,
		"run_id":         runID,
		"entrypoint":     entrypointName(),
	}
}

// trackEventSync is the synchronous body of TrackEvent. It is the single place
// every event acquires its standard properties and is validated against the
// typed allowlist. An unknown event is not sent.
func trackEventSync(eventName string, extraProps map[string]string) bool {
	return emitEvent(eventName, extraProps, uuid.NewString(), nowUTC(), telemetryGen.Load())
}

// emitEvent validates and delivers one event. PostHog deduplicates on the same
// uuid, event, timestamp and distinct_id, so a retry reuses all four (the
// timestamp is fixed when the event is created, not when it is sent). That is
// best-effort deduplication, not an exactly-once guarantee. gen is the
// telemetry generation the event was created in. It reports whether the
// service accepted the event.
func emitEvent(eventName string, extraProps map[string]string, eventUUID, timestamp string, gen uint64) bool {
	if config.PostHogKey == "" || !telemetryValid(gen) {
		return false
	}
	typed, known := sanitizeProps(eventName, extraProps)
	if !known {
		return false
	}
	id := getInstallID()
	if id == "" {
		return false
	}
	props := standardProps()
	for k, v := range typed {
		props[k] = v
	}
	return postEvent(eventName, id, eventUUID, timestamp, gen, props)
}

// asyncGo runs fn in a goroutine that can never panic into the caller. All
// tracking work goes through here so no launch path can be blocked or failed
// by tracking. The WaitGroup exists so tests can wait for in-flight work.
func asyncGo(fn func()) {
	asyncWG.Add(1)
	go func() {
		defer asyncWG.Done()
		defer func() {
			if r := recover(); r != nil {
				logDeliveryFailure("panic", 0, fmt.Sprintf("%v", r))
			}
		}()
		fn()
	}()
}

// waitForAsync blocks until all in-flight tracking goroutines finish.
// Test-only helper; production code never waits on tracking.
func waitForAsync() { asyncWG.Wait() }

func trackInstall(gen uint64) {
	dir := resolveCoralDir()
	if dir == "" {
		return
	}
	os.MkdirAll(dir, 0755)

	idFile := filepath.Join(dir, ".install_id")
	versionFile := filepath.Join(dir, ".install_version")

	installID := readFile(idFile)
	storedVersion := readFile(versionFile)
	currentVersion := config.Version

	if installID == "" {
		// New install
		installID = generateUUID()
		os.WriteFile(idFile, []byte(installID), 0600)
		os.WriteFile(versionFile, []byte(currentVersion), 0600)
		// Update cache
		installIDMu.Lock()
		cachedInstallID = installID
		installIDMu.Unlock()
		postEvent(EventInstall, installID, uuid.NewString(), nowUTC(), gen, standardProps())
		return
	}

	// Update cache
	installIDMu.Lock()
	cachedInstallID = installID
	installIDMu.Unlock()

	if currentVersion != "" && storedVersion != currentVersion {
		// Version upgrade
		os.WriteFile(versionFile, []byte(currentVersion), 0600)
		postEvent(EventUpgrade, installID, uuid.NewString(), nowUTC(), gen, standardProps())
	}
}

// retryDelays are the waits before each retry: a bounded three attempts in
// total. It is a var so tests can shorten it.
var retryDelays = []time.Duration{500 * time.Millisecond, 2 * time.Second}

// postEvent delivers one event, retrying transient failures (network errors,
// 5xx, 408, 429) a bounded number of times with an identical payload (same
// uuid, event, timestamp, distinct_id). Telemetry being switched off, or
// switched off and on again, drops the event and stops the retries; a
// non-retryable 4xx is logged and dropped. It reports whether the service
// accepted the event.
func postEvent(event, distinctID, eventUUID, timestamp string, gen uint64, properties map[string]any) bool {
	payload := map[string]any{
		"api_key":     config.PostHogKey,
		"event":       event,
		"distinct_id": distinctID,
		"uuid":        eventUUID,
		"timestamp":   timestamp,
		"properties":  properties,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for attempt := 0; ; attempt++ {
		if !telemetryValid(gen) {
			return false
		}
		status, detail, retryable := postOnce(client, data)
		if status >= 200 && status < 300 {
			return true
		}
		logDeliveryFailure(event, status, detail)
		if !retryable || attempt >= len(retryDelays) {
			return false
		}
		time.Sleep(retryDelays[attempt])
	}
}

// postOnce makes one attempt and reports its status (0 on a network error),
// failure detail, and whether trying again could help.
func postOnce(client *http.Client, data []byte) (status int, detail string, retryable bool) {
	resp, err := client.Post(posthogURL, "application/json", bytes.NewReader(data))
	if err != nil {
		return 0, err.Error(), true
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		return resp.StatusCode, "", false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	retry := resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests
	return resp.StatusCode, strings.TrimSpace(string(body)), retry
}

// deliveryLogMaxBytes caps the local failure log so it can never grow without
// bound on a machine that is permanently offline.
const deliveryLogMaxBytes = 64 * 1024

// logDeliveryFailure records a non-sensitive tracking delivery failure to
// <coralDir>/tracking-failures.log so failures can be diagnosed instead of
// silently discarded. Only the event name, HTTP status, and error detail are
// written — never event properties, install ID, or any user content.
func logDeliveryFailure(event string, status int, detail string) {
	defer func() { recover() }()

	if len(detail) > 300 {
		detail = detail[:300]
	}
	detail = strings.ReplaceAll(detail, "\n", " ")
	line := fmt.Sprintf("%s event=%s status=%d detail=%s\n",
		time.Now().UTC().Format(time.RFC3339), event, status, detail)

	log.Printf("[tracking] delivery failure: event=%s status=%d detail=%s", event, status, detail)

	dir := resolveCoralDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	path := filepath.Join(dir, "tracking-failures.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > deliveryLogMaxBytes {
		os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line)
}

// posthogKeyPresent reports whether an analytics key was injected at build
// time. Builds from source have none and send nothing.
func posthogKeyPresent() bool { return config.PostHogKey != "" }

func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// resolveCoralDir returns the data directory for tracking state files, or ""
// if SetCoralDir has not been called.
//
// There is deliberately no ~/.coral fallback. Guessing a default let this
// package write milestone state into the user's real install from anywhere
// that had not configured it — including the test suite, which exercises the
// launch and task handlers and so silently changed the production install's
// supporter-reminder state. Tracking now touches disk only where it has been
// told to.
func resolveCoralDir() string { return coralDir }

// stateDirReady reports whether tracking has somewhere to keep state.
func stateDirReady() bool { return coralDir != "" }

func generateUUID() string {
	return uuid.New().String()
}
