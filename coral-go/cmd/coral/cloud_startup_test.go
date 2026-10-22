package main

import (
	"bytes"
	"context"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
)

func TestCloudStartupMode(t *testing.T) {
	for _, tc := range []struct {
		setting      string
		haveURL, tty bool
		want         string
	}{
		// Off unless asked for (#3459): a URL, explicit or the release default, is not enough.
		{"", true, true, "off"},
		{"", true, false, "off"},
		{"off", true, true, "off"},
		{"prompt", true, true, "prompt"},
		{"prompt", true, false, "log"}, // never block without a terminal (#3271)
		{"prompt", false, true, "off"}, // no hub configured: nothing to connect to
		{"auto", true, true, "log"},
		{"auto", false, false, "off"},
	} {
		got, err := cloudStartupMode(tc.setting, tc.haveURL, tc.tty)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got, "%+v", tc)
	}
	_, err := cloudStartupMode("yes", true, true)
	assert.Error(t, err)
}

// The build's default URL alone offers nothing; an explicit URL, or CORAL_CLOUD_CONNECT
// (--cloud-connect) prompt or auto, does; off wins over an explicit URL (#3459).
func TestCloudConnectSetting(t *testing.T) {
	mode := func(cfg *config.Config) string {
		m, err := cloudStartupMode(cfg.EffectiveCloudConnect(), cfg.CloudURL != "", true)
		require.NoError(t, err)
		return m
	}
	// The built-in default only (a release build, nothing passed).
	cfg := config.Load(t.TempDir())
	cfg.CloudURL, cfg.CloudURLExplicit, cfg.CloudConnect = "https://app.coralhub.dev", false, ""
	assert.False(t, cfg.OffersCloudConnect(), "the default URL alone offers nothing")
	assert.Equal(t, "off", mode(cfg))

	// An explicit URL: like --cloud-connect prompt.
	t.Setenv("CORAL_CLOUD_CONNECT", "")
	t.Setenv("CORAL_CLOUD_URL", "https://app.coralhub.dev")
	cfg = config.Load(t.TempDir())
	assert.True(t, cfg.CloudURLExplicit)
	assert.True(t, cfg.OffersCloudConnect(), "CORAL_CLOUD_URL given: offered")
	assert.Equal(t, "prompt", mode(cfg))
	t.Setenv("CORAL_CLOUD_CONNECT", "off")
	cfg = config.Load(t.TempDir())
	assert.False(t, cfg.OffersCloudConnect(), "off wins over an explicit URL")
	assert.Equal(t, "off", mode(cfg))
	t.Setenv("CORAL_CLOUD_CONNECT", "auto")
	assert.Equal(t, "log", mode(config.Load(t.TempDir())))
	t.Setenv("CORAL_CLOUD_CONNECT", "")
	t.Setenv("CORAL_CLOUD_URL", "off")
	cfg = config.Load(t.TempDir())
	assert.False(t, cfg.CloudURLExplicit, "CORAL_CLOUD_URL=off is no URL")
	assert.False(t, cfg.OffersCloudConnect())
	t.Setenv("CORAL_CLOUD_URL", "https://app.coralhub.dev")
	// Switch words: 1, true, yes, on are prompt; 0, false, no are off; anything else is a
	// warning and off, never a refusal to start (Backend, #3459).
	for v, want := range map[string]string{"1": "prompt", "true": "prompt", "Yes": "prompt", "on": "prompt",
		"0": "off", "false": "off", "no": "off", "maybe": "off", "promt": "off"} {
		t.Setenv("CORAL_CLOUD_CONNECT", v)
		cfg = config.Load(t.TempDir())
		assert.Equal(t, want, cfg.CloudConnect, v)
		assert.Equal(t, want == "prompt", cfg.OffersCloudConnect(), v)
	}

	for _, v := range []string{"prompt", " Prompt ", "auto"} {
		t.Setenv("CORAL_CLOUD_CONNECT", v)
		cfg = config.Load(t.TempDir())
		assert.True(t, cfg.OffersCloudConnect(), v)
	}
	t.Setenv("CORAL_CLOUD_CONNECT", "off")
	assert.False(t, config.Load(t.TempDir()).OffersCloudConnect())
	// With the flag but no hub (a dev build, or --cloud-url off): nothing to offer.
	t.Setenv("CORAL_CLOUD_CONNECT", "prompt")
	t.Setenv("CORAL_CLOUD_URL", "off")
	assert.False(t, config.Load(t.TempDir()).OffersCloudConnect())
}

// While the step is diverted, Ctrl-C ends the step only; SIGTERM, and Ctrl-C at
// any other time, stop the server.
func TestInterruptRouter(t *testing.T) {
	var shutdowns, steps atomic.Int32
	ir := &interruptRouter{shutdown: func() { shutdowns.Add(1) }}
	ir.handle(os.Interrupt)
	assert.EqualValues(t, 1, shutdowns.Load())
	restore := ir.Divert(func() { steps.Add(1) })
	ir.handle(os.Interrupt)
	assert.EqualValues(t, 1, steps.Load())
	assert.EqualValues(t, 1, shutdowns.Load(), "the server keeps running")
	ir.handle(syscall.SIGTERM)
	assert.EqualValues(t, 2, shutdowns.Load(), "SIGTERM always stops it")
	restore()
	ir.handle(os.Interrupt)
	assert.EqualValues(t, 3, shutdowns.Load())
}

// stepEnv is a startup step against a fake Coral, with the log captured.
func stepEnv(t *testing.T, statuses ...string) (*fakeCoral, *config.Config, *syncBuffer) {
	t.Helper()
	fastPoll(t)
	f := newFakeCoral(t, ready, enrolled)
	f.statuses = statuses
	u, err := url.Parse(f.srv.URL)
	require.NoError(t, err)
	cfg := config.Load("")
	cfg.Port, err = strconv.Atoi(u.Port())
	require.NoError(t, err)
	var logs syncBuffer
	old := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(old) })
	return f, cfg, &logs
}

// #3285: on a terminal, the step shows the link and code, waits, asks for the
// access, and the server keeps running afterwards.
func TestCloudStartupStepPrompt(t *testing.T) {
	f, cfg, _ := stepEnv(t, idle, enrolling, connected)
	var out bytes.Buffer
	ir := &interruptRouter{shutdown: func() { t.Error("the step must not stop the server") }}
	runCloudStartupStep(context.Background(), cfg, cloudConnectPrompt, ir, strings.NewReader("\n2\n"), &out)
	o := out.String()
	assert.Contains(t, o, "Connect this server to Coral Hub (https://app.coralhub.dev) now?")
	assert.Contains(t, o, "Your code ABCD-EFGH should already be filled in")
	assert.Contains(t, o, "Connected to Coral Hub as build-box.")
	assert.Contains(t, o, "Coral Hub access: interact.")
	assert.Equal(t, 1, f.connects)
}

func TestCloudStartupStepSkip(t *testing.T) {
	f, cfg, _ := stepEnv(t, idle)
	var out bytes.Buffer
	runCloudStartupStep(context.Background(), cfg, cloudConnectPrompt, &interruptRouter{}, strings.NewReader("s\n"), &out)
	assert.Contains(t, out.String(), "Skipped")
	assert.Contains(t, out.String(), "coral cloud connect")
	assert.Zero(t, f.connects)
}

// Ctrl-C while the step waits cancels the enrollment and only the step.
func TestCloudStartupStepCtrlC(t *testing.T) {
	f, cfg, _ := stepEnv(t, idle, enrolling)
	ir := &interruptRouter{shutdown: func() { t.Error("Ctrl-C in the step must not stop the server") }}
	polls := 0
	f.onStatus = func() {
		if polls++; polls == 4 {
			go ir.handle(os.Interrupt)
		}
	}
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		runCloudStartupStep(context.Background(), cfg, cloudConnectPrompt, ir, strings.NewReader("\n"), &out)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the step did not end on Ctrl-C")
	}
	assert.Contains(t, out.String(), "Cancelled: this server was not connected")
	assert.Contains(t, strings.Join(f.callList(), "\n"), "POST /api/cloud/connect/cancel")
	ir.mu.Lock()
	assert.Nil(t, ir.divert, "Ctrl-C goes back to stopping the server")
	ir.mu.Unlock()
}

// Without a terminal the step never blocks: it logs one line with the link and
// code, and logs the result when connected.
func TestCloudStartupStepLogsWithoutATerminal(t *testing.T) {
	f, cfg, logs := stepEnv(t, idle, enrolling, enrolling, connected)
	start := time.Now()
	runCloudStartupStep(context.Background(), cfg, "log", &interruptRouter{}, nil, nil)
	assert.Less(t, time.Since(start), 2*time.Second, "returns at once")
	require.Eventually(t, func() bool { return strings.Contains(logs.String(), "Connected to Coral Hub as build-box") }, 5*time.Second, 10*time.Millisecond, logs.String())
	assert.Contains(t, logs.String(), "[CLOUD] To connect this server to Coral Hub, open https://app.coralhub.dev/enroll?code=ABCD-EFGH and enter code ABCD-EFGH")
	assert.Equal(t, 1, f.connects)
	for _, c := range f.callList() {
		assert.NotContains(t, c, "max-mode", "nothing asked or changed without a terminal")
	}
}

// A server already connected (or still loading its enrollment) is left alone:
// the step waits for the connector to load and starts no enrollment.
func TestCloudStartupStepLeavesAConnectedServerAlone(t *testing.T) {
	loading := `{"configured":true,"hub_url":"https://app.coralhub.dev","state":"idle","loading":true,"max_mode":"view"}`
	f, cfg, logs := stepEnv(t, loading, loading, connected)
	var out bytes.Buffer
	runCloudStartupStep(context.Background(), cfg, cloudConnectPrompt, &interruptRouter{}, strings.NewReader("\n"), &out)
	assert.Zero(t, f.connects)
	assert.Empty(t, out.String(), "no prompt")
	assert.Contains(t, logs.String(), "[CLOUD] Coral Hub: this server is connected")
	assert.NotContains(t, logs.String(), "build-box", "the Hub label is not written to the log")

	f2, cfg2, _ := stepEnv(t, notConfigured)
	runCloudStartupStep(context.Background(), cfg2, cloudConnectPrompt, &interruptRouter{}, strings.NewReader("\n"), &out)
	assert.Zero(t, f2.connects)
	assert.Empty(t, out.String())
}

// A server enrolled with the hub's old host (#3546, hub_moved) is left alone in both modes: the
// step logs why and starts no enrollment, so nothing reaches either hub and the message stays.
func TestCloudStartupStepLeavesAMovedHubAlone(t *testing.T) {
	moved := `{"configured":true,"hub_url":"https://app.coralhub.dev","state":"idle","last_error_code":"hub_moved","last_error":"this server is enrolled with a different hub (https://app.coralai.ai); run 'coral cloud connect --force' to enroll with https://app.coralhub.dev","max_mode":"view"}`
	// After a failed attempt to enroll again the code is that failure's, and hub_moved still says it.
	afterFailure := `{"configured":true,"hub_url":"https://app.coralhub.dev","state":"idle","hub_moved":true,"last_error_code":"rate_limited","last_error":"this server is enrolled with a different hub (https://app.coralai.ai); run 'coral cloud connect --force' to enroll with https://app.coralhub.dev","max_mode":"view"}`
	for _, mode := range []string{cloudConnectPrompt, "log", cloudConnectPrompt + " after a failure"} {
		f, cfg, logs := stepEnv(t, moved, moved, moved)
		if strings.HasSuffix(mode, " after a failure") {
			mode = cloudConnectPrompt
			f, cfg, logs = stepEnv(t, afterFailure, afterFailure, afterFailure)
		}
		var out bytes.Buffer
		runCloudStartupStep(context.Background(), cfg, mode, &interruptRouter{}, strings.NewReader("\n"), &out)
		time.Sleep(200 * time.Millisecond) // log mode would start its flow in a goroutine
		assert.Zero(t, f.connects, mode)
		assert.Empty(t, out.String(), "no prompt")
		assert.Contains(t, logs.String(), "[CLOUD] Coral Hub: this server is enrolled with a different hub (https://app.coralai.ai); run 'coral cloud connect --force'", mode)
	}
}
