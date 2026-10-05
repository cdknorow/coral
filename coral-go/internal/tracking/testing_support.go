package tracking

// ConfigureForTest points tracking at a local capture endpoint and an isolated
// state directory with a fixed install ID, and returns a restore function. It
// exists so other packages' tests can observe events without any real
// analytics call; production code never calls it.
func ConfigureForTest(captureURL, dir, installID string) (restore func()) {
	prevURL, prevDir, prevID := posthogURL, coralDir, cachedInstallID
	prevDelays := retryDelays
	posthogURL, coralDir, cachedInstallID = captureURL, dir, installID
	retryDelays = nil // no retries/sleeps in other packages' tests
	installIDOnce.Do(func() {})
	SetTelemetryEnabled(true)
	return func() {
		waitForAsync()
		posthogURL, coralDir, cachedInstallID, retryDelays = prevURL, prevDir, prevID, prevDelays
	}
}
