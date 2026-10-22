package trigger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Preflight asks the installed updater, before the server quits for it, whether it will install anything (#5946). The server hands Coral over
// and exits, so an updater that then answers "not enabled", "off" or "this is no registered install" would leave nobody to start Coral again:
// those are refused here, with the server still running. The question is "coral-update status --json" (it only reads the updater's own files:
// no network, no trusted root, nothing written), answered with the feature on or off, so it works for every build.
//
// The refusals are stable errors, one per cause, for the route's 409 codes.
var (
	// ErrApplyDisabled: the apply's off switch is off for this platform (applyEnabled in cmd/coral-update).
	ErrApplyDisabled = errors.New("the updater's apply is not enabled")
	// ErrUpdatesOff: automatic updates are off for this build (a development, beta or Dropbox build) or by CORAL_AUTO_UPDATE=off.
	ErrUpdatesOff = errors.New("updates are off for this build or environment")
	// ErrInstallUnsupported: this is not a Coral installed per machine under Program Files and registered with Windows Installer.
	ErrInstallUnsupported = errors.New("this Coral is not a registered per-machine install")
	// ErrUpdaterUnavailable: the updater could not be asked, or its answer is not the one expected.
	ErrUpdaterUnavailable = errors.New("the updater could not be asked")
)

// maxStatusBytes bounds what is read from the updater's answer.
const maxStatusBytes = 1 << 20

// statusAnswer is the part of "coral-update status --json" Preflight reads.
type statusAnswer struct {
	Feature      string  `json:"feature"`
	ApplyEnabled *bool   `json:"apply_enabled"`
	Install      *string `json:"install"` // "supported", or why not (cmd/coral-update/status.go); absent where the question does not apply
}

// runStatus runs the installed updater's status; windows runs the program beside the server (launch_windows.go), a test replaces it.
var runStatus = func(ctx context.Context, dataDir string) ([]byte, error) {
	return nil, errors.New("the updater's status is read on Windows only")
}

// Preflight returns nil when the updater would go on to install, else one of the errors above.
func Preflight(ctx context.Context, dataDir string) error {
	raw, err := runStatus(ctx, dataDir)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUpdaterUnavailable, err)
	}
	if len(raw) > maxStatusBytes {
		return ErrUpdaterUnavailable
	}
	var a statusAnswer
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&a); err != nil || a.ApplyEnabled == nil || a.Install == nil {
		return ErrUpdaterUnavailable // an older updater that cannot say is no reason to quit Coral on its word
	}
	switch {
	case a.Feature != "on":
		return ErrUpdatesOff
	case !*a.ApplyEnabled:
		return ErrApplyDisabled
	case *a.Install != "supported":
		return ErrInstallUnsupported
	}
	return nil
}
