// The local "Install update" trigger (#166, the Windows auto-update design #162): POST /api/system/update/install.
//
// Windows updates itself from its MSI, which needs Windows Installer to ask the person at this computer (UAC), so the only way to start it is the
// person here: the dashboard on this computer or the signed command line, never Coral Hub, never an agent. The request names nothing (no version, no
// path, no URL): the updater (coral-update apply) installs what it staged and verified, and re-verifies it. The server's part is to start that
// program from a copy of its own, outside the install folder it is about to replace, and then to quit with the exit code that tells the tray not to start
// it again (updateQuitExitCode).

package routes

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"sync/atomic"

	"github.com/subgentic/coral/internal/updater/trigger"
)

// UpdateQuitExitCode is the exit code of a server that quit for an update: the tray's supervisor takes it as "the updater has Coral now; do not start the
// server again" (cmd/coral-tray/supervise.go). 4 is Settings > Restart.
const UpdateQuitExitCode = 5

// updateInstallPlatform is whether this build installs updates through the local trigger: Windows only (the other platforms apply an update
// without asking anyone). A variable so a test can stand for Windows.
var updateInstallPlatform = runtime.GOOS == "windows"

// InstallUpdate: POST /api/system/update/install, on this computer only (the dashboard or the signed command line, as Coral's own user), no body.
// 202 once the updater is running and the server has been asked to quit; 409 when there is nothing to install, it is not Windows, or one is already
// under way.
func (h *CloudHandler) InstallUpdate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.refuseCloud(w, r, allowDashboard|allowCLI) {
		return
	}
	if !humanInputCaller(r) {
		writeError(w, http.StatusForbidden, "operator_only", "only the Operator can install an update") // a person only: never an agent with forged browser headers
		return
	}
	// Nothing in the request chooses anything: an empty body, or {}.
	raw, err := io.ReadAll(io.LimitReader(r.Body, 33))
	if err != nil || len(raw) > 32 || (len(bytes.TrimSpace(raw)) != 0 && string(bytes.TrimSpace(raw)) != "{}") {
		errBadRequest(w, "the request has no body: the update to install is the one Coral has staged and verified")
		return
	}
	if !updateInstallPlatform {
		writeError(w, http.StatusConflict, "unsupported_platform", "This computer applies updates another way")
		return
	}
	if h.QuitForUpdate == nil {
		writeError(w, http.StatusConflict, "install_unavailable", "This Coral can't hand itself to the updater: update it from its installer")
		return
	}
	staged := h.stagedUpdate
	if staged == nil {
		staged = func(context.Context) (string, error) { return trigger.StagedMSI(h.dataDir) } // the light check: the server does not link the Sigstore verifier
	}
	version, err := staged(r.Context())
	if err != nil {
		writeError(w, http.StatusConflict, "nothing_staged", "There is no verified update staged to install")
		return
	}
	if !atomic.CompareAndSwapInt32(&h.installing, 0, 1) {
		writeError(w, http.StatusConflict, "busy", "An update is already being installed")
		return
	}
	launch := h.launchUpdater
	if launch == nil {
		launch = func(ctx context.Context) error { return trigger.Launch(ctx, h.dataDir, version) }
	}
	if err := launch(r.Context()); err != nil {
		atomic.StoreInt32(&h.installing, 0)
		slog.Warn("[update] the updater could not be started", "error", err)
		writeError(w, http.StatusInternalServerError, "updater_not_started", "The updater could not be started: Coral is still running, nothing was changed")
		return
	}
	slog.Info("[update] the updater has been started from the dashboard; Coral is quitting for it", "version", version)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "version": version, "quitting": true})
	// After the answer is written the server quits with UpdateQuitExitCode; QuitForUpdate leaves a moment for the reply to go out.
	if err := h.QuitForUpdate(); err != nil {
		slog.Warn("[update] could not quit for the updater", "error", err)
	}
}
