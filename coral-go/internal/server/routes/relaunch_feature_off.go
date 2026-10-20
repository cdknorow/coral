//go:build !relaunchfixture

package routes

// updateRelaunchFeature is false in every build that ships: until coral-hub's half ships and the updater exists, both relaunch routes answer 404. A constant on purpose: nothing a
// request, a setting, a flag or the environment can turn it on. The only build where it is true is one made with the relaunchfixture tag (relaunch_feature_fixture.go, #5452).
const updateRelaunchFeature = false

// BuildRelaunchUpdater is the updater the server's relaunch routes use: none here (the routes answer 404 whatever else is set).
func BuildRelaunchUpdater() RelaunchUpdater { return nil }
