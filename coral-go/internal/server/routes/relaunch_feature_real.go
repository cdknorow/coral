//go:build !relaunchfixture

package routes

// updateRelaunchFeature is true in every build that ships (#6290, the operator's decision #5231): the relaunch routes are on, with the real updater. Nothing a request, a setting, a flag
// or the environment does turns it off or on; it is a constant. What the routes then do is bounded elsewhere: an install needs a person's yes (the dashboard or the signed command line,
// an acknowledgement token the person's own click fetched), the Hub's owner can relaunch only when the person here turned hub_relaunch_updates on (default off), and coral-update's apply
// switch (cmd/coral-update/apply_switch.go) answers not_enabled where it is off. A build made with the relaunchfixture tag (relaunch_feature_fixture.go) has the fake updater instead.
const updateRelaunchFeature = true

// BuildRelaunchUpdater is the updater the server's relaunch routes use: the real one (relaunch_real.go). quit ends the server after a relaunch was started.
func BuildRelaunchUpdater(coralDir string, quit func()) RelaunchUpdater {
	return NewRealUpdater(coralDir, quit)
}
