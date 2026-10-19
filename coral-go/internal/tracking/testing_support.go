package tracking

// ConfigureForTest points tracking at a local capture endpoint and an isolated
// state directory with a fixed install ID, and returns a restore function. It
// exists so other packages' tests can observe events without any real
// analytics call; production code never calls it.
func ConfigureForTest(captureURL, dir, installID string) (restore func()) {
	prevURL, prevDir, prevID := posthogURL, coralDir, cachedInstallID
	prevDelays := retryDelays
	prereqMu.Lock()
	prereqSeen = map[string]bool{} // each test observes prerequisites afresh
	prereqMu.Unlock()
	browserMu.Lock()
	seenPages, failuresPerCode = map[string]bool{}, map[string]int{} // and dashboard events (go test -count>1)
	browserMu.Unlock()
	posthogURL, coralDir, cachedInstallID = captureURL, dir, installID
	retryDelays = nil // no retries/sleeps in other packages' tests
	installIDOnce.Do(func() {})
	resetActiveWeek()
	SetTelemetryEnabled(true)
	return func() {
		waitForAsync()
		posthogURL, coralDir, cachedInstallID, retryDelays = prevURL, prevDir, prevID, prevDelays
	}
}

// WaitForAsyncForTest blocks until every in-flight tracking goroutine has finished. A test
// that is about to change tracking's configuration (config.PostHogKey, ConfigureForTest)
// calls it first, so a goroutine another test's request left behind cannot read that
// configuration while it is written (a data race under go test -race).
func WaitForAsyncForTest() { waitForAsync() }
