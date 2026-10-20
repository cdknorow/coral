// How the server builds the update relaunch routes' handler from its stores (#5280).

package routes

import (
	"context"
	"path/filepath"

	"github.com/cdknorow/coral/internal/store"
)

// updateRelaunchFeature is the off flag: until coral-hub's half ships and the updater exists, both routes answer 404. It is a constant on
// purpose: nothing a request, a setting or the environment says can turn it on.
const updateRelaunchFeature = false

// NewUpdateRelaunchHandler wires the handler to the settings and enrolment stores and the live sessions. updater is the updater's
// interface (nil until it exists: the routes then answer 404 whatever the flag says). backend is the terminal backend in use: only pty
// agents are restarted by a relaunch, a tmux agent survives it.
func NewUpdateRelaunchHandler(ss *store.SessionStore, dataDir string, backend func() string, updater RelaunchUpdater) *UpdateRelaunchHandler {
	cs := store.NewCloudStore(ss.DB(), store.NewRemoteServerStore(ss.DB(), dataDir))
	return &UpdateRelaunchHandler{
		Enabled:   func() bool { return updateRelaunchFeature },
		Updater:   updater,
		Settings:  ss.GetSettings,
		OwnerUser: cs.OwnerUser,
		Dir:       filepath.Join(dataDir, updatesDirName),
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
