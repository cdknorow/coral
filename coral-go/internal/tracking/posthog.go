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
	"time"

	"github.com/cdknorow/coral/internal/config"
	"github.com/google/uuid"
)

// posthogURL is the capture endpoint. It is a var so tests can point it at a
// local server; production never reassigns it.
var posthogURL = "https://us.i.posthog.com/capture/"

var (
	cachedInstallID string
	installIDOnce   sync.Once
	coralDir        string // set by SetCoralDir; falls back to ~/.coral

	// asyncWG tracks in-flight tracking goroutines so tests can wait on them.
	asyncWG sync.WaitGroup
)

// SetCoralDir sets the data directory used for tracking state files.
// Must be called before TrackInstallAsync(). If not called, falls back to ~/.coral.
func SetCoralDir(dir string) {
	coralDir = dir
}

// getInstallID returns the install ID, reading from disk once and caching.
func getInstallID() string {
	installIDOnce.Do(func() {
		idFile := filepath.Join(resolveCoralDir(), ".install_id")
		cachedInstallID = readFile(idFile)
	})
	return cachedInstallID
}

// TrackInstallAsync checks for first install or version upgrade and sends
// an event to PostHog. Also sends an 'app_opened' heartbeat for DAU.
// Runs in a goroutine, never blocks.
func TrackInstallAsync() {
	if config.PostHogKey == "" {
		return
	}
	asyncGo(func() {
		trackInstall()
		// Always send app_opened for DAU tracking
		trackEventSync("app_opened", nil)
		// Retention: fire returned_24h once, on the first open >24h after the first.
		trackReturnVisitSync()
	})
}

// TrackEvent sends a named event to PostHog with optional extra properties.
// Non-blocking — runs in a goroutine. Safe to call from any context.
func TrackEvent(eventName string, extraProps map[string]string) {
	asyncGo(func() { trackEventSync(eventName, extraProps) })
}

// trackEventSync is the synchronous body of TrackEvent. It is the single place
// every event acquires its standard properties.
func trackEventSync(eventName string, extraProps map[string]string) {
	if config.PostHogKey == "" {
		return
	}
	id := getInstallID()
	if id == "" {
		return
	}
	props := map[string]any{
		"version": config.Version,
		"edition": config.TierName,
		"os":      runtime.GOOS,
		"arch":    runtime.GOARCH,
	}
	for k, v := range extraProps {
		props[k] = v
	}
	postEvent(eventName, id, props)
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

func trackInstall() {
	dir := resolveCoralDir()
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
		cachedInstallID = installID
		postEvent("install", installID, map[string]any{
			"version": currentVersion,
			"edition": config.TierName,
			"os":      runtime.GOOS,
			"arch":    runtime.GOARCH,
		})
		return
	}

	// Update cache
	cachedInstallID = installID

	if currentVersion != "" && storedVersion != currentVersion {
		// Version upgrade
		os.WriteFile(versionFile, []byte(currentVersion), 0600)
		postEvent("upgrade", installID, map[string]any{
			"version": currentVersion,
			"edition": config.TierName,
			"os":      runtime.GOOS,
			"arch":    runtime.GOARCH,
		})
	}
}

func postEvent(event, distinctID string, properties map[string]any) {
	payload := map[string]any{
		"api_key":     config.PostHogKey,
		"event":       event,
		"distinct_id": distinctID,
		"properties":  properties,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(posthogURL, "application/json", bytes.NewReader(data))
	if err != nil {
		logDeliveryFailure(event, 0, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		logDeliveryFailure(event, resp.StatusCode, strings.TrimSpace(string(body)))
	}
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

func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// resolveCoralDir returns the data directory for tracking state files.
func resolveCoralDir() string {
	if coralDir != "" {
		return coralDir
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".coral")
}

func generateUUID() string {
	return uuid.New().String()
}
