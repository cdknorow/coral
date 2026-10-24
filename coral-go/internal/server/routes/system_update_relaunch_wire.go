// How the server builds the update relaunch routes' handler from its stores (#5280).

package routes

import (
	"context"
	"os"
	"path/filepath"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/updater/runrecord"
)

// updateRelaunchFeature (relaunch_feature_off.go) is the off flag: until coral-hub's half ships and the updater exists, both routes answer 404. It is a constant on
// purpose: nothing a request, a setting or the environment says can turn it on. Only a build made with the relaunchfixture tag (relaunch_feature_fixture.go, #5452) has it on.

// NewUpdateRelaunchHandler wires the handler to the settings and enrolment stores and the live sessions. updater is the updater's
// interface (nil until it exists: the routes then answer 404 whatever the flag says). backend is the terminal backend in use: only pty
// agents are restarted by a relaunch, a tmux agent survives it.
func NewUpdateRelaunchHandler(ss *store.SessionStore, dataDir string, backend func() string, updater RelaunchUpdater) *UpdateRelaunchHandler {
	cs := store.NewCloudStore(ss.DB(), store.NewRemoteServerStore(ss.DB(), dataDir))
	return &UpdateRelaunchHandler{
		Enabled:     func() bool { return updateRelaunchFeature },
		Updater:     updater,
		Settings:    ss.GetSettings,
		SetSetting:  ss.SetSetting,
		OwnerUser:   cs.OwnerUser,
		OwnerRecord: cs.OwnerRecord,
		Feeds:       releaseFeeds,
		Dir:         filepath.Join(dataDir, updatesDirName),
		// Coral must be startable again before a relaunch is offered: this computer's own record of how it was started, or a service manager (#5419).
		CanRestart: func() bool {
			exe, err := os.Executable()
			return err == nil && runrecord.Restartable(dataDir, exe) == nil
		},
		Agents: func(ctx context.Context) ([]RestartAgent, error) {
			live, err := ss.GetAllLiveSessions(ctx)
			if err != nil {
				return nil, err
			}
			var out []RestartAgent
			for _, l := range live {
				if l.IsSleeping == 1 || l.IsArchived == 1 {
					continue
				}
				// Decided by the terminal this Coral runs, not by the stored launch path: on a tmux Coral every agent's launch path reads "pty" (it goes through the TerminalBackend), and a tmux agent survives a
				// relaunch. Only an agent that was started the legacy tmux way (launch path "tmux") survives on a pty Coral too (#6146).
				if backend() != "pty" || (l.Backend != nil && *l.Backend == "tmux") {
					continue
				}
				name := l.AgentName
				if l.DisplayName != nil && *l.DisplayName != "" {
					name = *l.DisplayName
				}
				out = append(out, RestartAgent{SessionID: l.SessionID, Name: name, State: "working"})
			}
			return out, nil
		},
	}
}
