// How the server builds the update relaunch routes' handler from its stores (#5280).

package routes

import (
	"context"
	"os"
	"path/filepath"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/updater/runrecord"
)

// updateRelaunchFeature (relaunch_feature_real.go) is the flag the routes read: a constant on purpose, so nothing a request, a setting or the environment says can change it. It is true in
// every shipping build (#6290) and the routes' limits are elsewhere (a person's yes, the Hub's switch, coral-update's apply switch). A build made with the relaunchfixture tag
// (relaunch_feature_fixture.go, #5452) has it on with a fake updater.

// NewUpdateRelaunchHandler wires the handler to the settings and enrolment stores and the live sessions. updater is the updater's
// interface (nil until it exists: the routes then answer 404 whatever the flag says). backend is the terminal backend in use: only pty
// agents are restarted by a relaunch, a tmux agent survives it.
func NewUpdateRelaunchHandler(ss *store.SessionStore, dataDir string, backend func() string, updater RelaunchUpdater) *UpdateRelaunchHandler {
	cs := store.NewCloudStore(ss.DB(), store.NewRemoteServerStore(ss.DB(), dataDir))
	return &UpdateRelaunchHandler{
		Enabled:    func() bool { return updateRelaunchFeature },
		Updater:    updater,
		Settings:   ss.GetSettings,
		SetSetting: ss.SetSetting,
		OwnerUser:  cs.OwnerUser,
		Feeds:      releaseFeeds,
		Dir:        filepath.Join(dataDir, updatesDirName),
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
				b := backend()
				if l.Backend != nil && *l.Backend != "" {
					b = *l.Backend
				}
				if b != "pty" {
					continue
				}
				name := l.AgentName
				if l.DisplayName != nil && *l.DisplayName != "" {
					name = *l.DisplayName
				}
				out = append(out, RestartAgent{SessionID: l.SessionID, Name: name, Backend: "pty", State: "working"})
			}
			return out, nil
		},
	}
}
