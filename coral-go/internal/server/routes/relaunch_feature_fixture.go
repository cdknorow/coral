//go:build relaunchfixture

package routes

// updateRelaunchFeature is true only in a build made with the relaunchfixture tag (#5452): coral-hub's test seam, built from source and never by anything that ships. The
// relaunch routes are then on and BuildRelaunchUpdater is a fake updater that answers from one fixture file; every check of a release (the hub-role stamp, the owner
// label, the ack token, the cap, the switch) runs as it does there.
const updateRelaunchFeature = true

// BuildRelaunchUpdater is the fixture updater (relaunch_fixture.go).
func BuildRelaunchUpdater(string, func()) RelaunchUpdater { return &fixtureUpdater{} }
