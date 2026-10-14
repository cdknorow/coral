package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/cdknorow/coral/internal/license"
	"github.com/cdknorow/coral/internal/store"
)

// Enrolling this server with a one-time credential, without a browser (#3149,
// Coral Hub launch-anywhere bootstrap). The credential comes from
// /run/coral/bootstrap.d/credential at startup (Linux), --bootstrap-file, or
// `coral cloud connect --ticket-file`, which reads the file itself (as the user
// who runs it) and hands the credential to the running server in an
// Authorization header, so the server is the only writer of the identity and
// never opens a path a caller names. Never from the environment or argv.
//
// Order: the identity check and the file checks are local and come first (no
// network before the file is validated); then one redeem, retried only for
// 429/5xx/transport; then the Hub-confirmed terms acceptance and the identity
// are stored. The local access cap is never touched: a new identity starts at
// whatever the owner set, View by default.

// Bootstrap statuses (shown in cloud status and the CLI) and their exit codes
// (Hub design 12.1, as agreed with the Orchestrator).
const (
	BootstrapNone            = ""                          // no credential offered
	BootstrapReady           = "ready"                     // 0: a pre-check that may proceed (coral cloud connect)
	BootstrapEnrolled        = "enrolled"                  // 0
	BootstrapUnsafeFile      = "unsafe_file"               // 2: a bad or unsafe local file
	BootstrapInvalid         = "credential_invalid"        // 2: the Hub's generic 401, never retried
	BootstrapNoHub           = "no_hub_configured"         // 2
	BootstrapTermsReconsent  = "terms_reconsent_required"  // 3: permanent for this credential
	BootstrapRetryLater      = "retry_later"               // 4: 429/5xx/unreachable, retries exhausted
	BootstrapIgnoredIdentity = "ignored_existing_identity" // 5: already enrolled; nothing was sent
	BootstrapRefused         = "hub_refused"               // 1: a 400 (a Coral bug) or an answer that cannot be used
)

// BootstrapExitCode is the process exit code for a status.
func BootstrapExitCode(status string) int {
	switch status {
	case BootstrapNone, BootstrapReady, BootstrapEnrolled:
		return 0
	case BootstrapUnsafeFile, BootstrapInvalid, BootstrapNoHub:
		return 2
	case BootstrapTermsReconsent:
		return 3
	case BootstrapRetryLater:
		return 4
	case BootstrapIgnoredIdentity:
		return 5
	}
	return 1
}

// BootstrapStatusSetting holds the last bootstrap outcome: {"status","at","detail"}.
const BootstrapStatusSetting = "cloud_bootstrap_status"

// BootstrapResult is a bootstrap's outcome. Detail is safe to show: it never
// contains credential or proof content, or Hub-supplied text.
type BootstrapResult struct {
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
	NotTmpfs bool   `json:"-"`
}

// BootstrapOptions configure one bootstrap.
type BootstrapOptions struct {
	HubURL     string // the configured, normalized origin
	Store      *store.CloudStore
	Settings   *store.SessionStore // for the status; nil skips recording it
	HTTPClient *http.Client        // nil: NewHubHTTPClient()
	Hostname   string
	Logger     *slog.Logger
	Sleep      func(context.Context, time.Duration) bool // nil: a real wait
	Attempts   int                                       // redeem attempts for retryable answers; 0: 6
	// RecordTerms records the terms version the Hub confirmed (nil:
	// license.RecordHubTermsAcceptance, the local acceptance marker).
	RecordTerms func(version string) error
}

func (o *BootstrapOptions) fill() {
	if o.HTTPClient == nil {
		o.HTTPClient = NewHubHTTPClient()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Sleep == nil {
		o.Sleep = func(ctx context.Context, d time.Duration) bool {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-t.C:
				return true
			}
		}
	}
	if o.Attempts <= 0 {
		o.Attempts = 6
	}
	if o.RecordTerms == nil {
		o.RecordTerms = license.RecordHubTermsAcceptance
	}
}

// CredentialSource is where a credential comes from.
type CredentialSource struct {
	WellKnown bool   // /run/coral/bootstrap.d/credential (Linux)
	File      string // --bootstrap-file / --ticket-file
	Value     []byte // already read and removed by the caller (the CLI's file or stdin)
	ProofFile string
	Proof     []byte // already read and validated by the caller
}

func (s CredentialSource) offered() bool { return s.WellKnown || s.File != "" || s.Value != nil }

// Bootstrap enrolls this server with the credential from src, if there is one.
func Bootstrap(ctx context.Context, o BootstrapOptions, src CredentialSource) BootstrapResult {
	o.fill()
	res := bootstrap(ctx, o, src)
	if res.Status != BootstrapNone {
		o.Logger.Info("cloud bootstrap", "status", res.Status, "detail", res.Detail)
		if res.NotTmpfs {
			o.Logger.Warn("cloud bootstrap: the credential file was not on a tmpfs (it was removed after reading)")
		}
		recordBootstrapStatus(ctx, o, res)
	}
	return res
}

func bootstrap(ctx context.Context, o BootstrapOptions, src CredentialSource) BootstrapResult {
	if !src.offered() {
		return BootstrapResult{}
	}
	// 1. Already enrolled: no Hub call. The well-known credential (in its own
	// 0700 tmpfs folder) is removed unread; a named file is removed only if it
	// holds a credential, so a wrong path never deletes anything else.
	if res := PreCheck(ctx, o.HubURL, o.Store); res.Status == BootstrapIgnoredIdentity {
		switch {
		case src.WellKnown:
			found, err := RemoveWellKnownBootstrap()
			if !found && err == nil {
				return BootstrapResult{}
			}
			if err != nil {
				return BootstrapResult{Status: BootstrapIgnoredIdentity, Detail: "this machine is already enrolled; the credential could not be removed: " + err.Error()}
			}
		case src.File != "":
			raw, _, err := ReadCredentialFile(src.File)
			clear(raw)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return BootstrapResult{Status: BootstrapIgnoredIdentity, Detail: "this machine is already enrolled; the file was left in place: " + err.Error()}
			}
		}
		return res
	} else if res.Status != BootstrapNone {
		return res
	}

	// 2. The credential and proof, with the file checks; nothing over the network yet.
	raw, notTmpfs := src.Value, false
	switch {
	case src.WellKnown:
		var found bool
		var err error
		raw, found, err = ReadWellKnownBootstrap()
		if !found && err == nil {
			return BootstrapResult{}
		}
		if err != nil {
			return BootstrapResult{Status: BootstrapUnsafeFile, Detail: err.Error()}
		}
	case src.File != "":
		var err error
		raw, notTmpfs, err = ReadCredentialFile(src.File)
		if err != nil {
			return BootstrapResult{Status: BootstrapUnsafeFile, Detail: err.Error()}
		}
	}
	cred, err := ParseCredential(raw)
	clear(raw) // the credential is kept in memory only, as one string, for the redeem series
	if err != nil {
		return BootstrapResult{Status: BootstrapUnsafeFile, Detail: err.Error(), NotTmpfs: notTmpfs}
	}
	proof := src.Proof
	if src.ProofFile != "" {
		if proof, err = ReadProofFile(src.ProofFile); err != nil {
			return BootstrapResult{Status: BootstrapUnsafeFile, Detail: "proof file: " + err.Error(), NotTmpfs: notTmpfs}
		}
	}
	if len(proof) > 0 {
		if err := ValidateProof(proof); err != nil {
			return BootstrapResult{Status: BootstrapUnsafeFile, Detail: err.Error(), NotTmpfs: notTmpfs}
		}
	}

	// 3. Redeem, retrying only what may succeed later, with ONE redeem id for the
	// whole series: after a lost response the Hub recognises the retry and
	// returns the same identity (design 5.7).
	redeemID := NewRedeemID()
	var r *Redeemed
	reached := false // an earlier attempt may have reached the Hub (its answer was lost)
	for attempt := 1; ; attempt++ {
		r, err = Redeem(ctx, o.HTTPClient, o.HubURL, cred, proof, o.Hostname, redeemID)
		var later *RetryAfterError
		if !errors.As(err, &later) || attempt >= o.Attempts {
			break
		}
		reached = reached || later.mayHaveReached()
		wait := later.After
		if wait == 0 {
			wait = time.Duration(1<<(attempt-1)) * time.Second
		}
		if !o.Sleep(ctx, wait) {
			break
		}
	}
	var later *RetryAfterError
	switch {
	case err == nil:
	case errors.Is(err, ErrCredentialInvalid) && reached:
		// An earlier attempt may have used it and its answer was lost.
		return BootstrapResult{Status: BootstrapInvalid, Detail: "the credential is no longer usable here: ask the automation for a new one", NotTmpfs: notTmpfs}
	case errors.Is(err, ErrCredentialInvalid):
		return BootstrapResult{Status: BootstrapInvalid, Detail: "credential not valid: " + ErrCredentialInvalid.Error(), NotTmpfs: notTmpfs}
	case errors.Is(err, ErrTermsReconsent):
		return BootstrapResult{Status: BootstrapTermsReconsent, Detail: ErrTermsReconsent.Error(), NotTmpfs: notTmpfs}
	case errors.As(err, &later):
		return BootstrapResult{Status: BootstrapRetryLater, Detail: later.Why + " (the Coral Hub is unreachable or busy); the credential may expire before the next try: if it is refused then, ask for a new one", NotTmpfs: notTmpfs}
	default:
		return BootstrapResult{Status: BootstrapRefused, Detail: err.Error(), NotTmpfs: notTmpfs}
	}

	// 4. The Hub confirmed this binary's terms version: record it, then the identity.
	if err := o.RecordTerms(r.TermsAccepted); err != nil {
		return BootstrapResult{Status: BootstrapRefused, Detail: err.Error(), NotTmpfs: notTmpfs}
	}
	if err := o.Store.Save(ctx, o.HubURL, r.ServerID, cleanLabel(o.Hostname), r.Token); err != nil {
		return BootstrapResult{Status: BootstrapRefused, Detail: "the identity could not be stored", NotTmpfs: notTmpfs}
	}
	return BootstrapResult{Status: BootstrapEnrolled, Detail: "enrolled as server " + r.ServerID, NotTmpfs: notTmpfs}
}

// PreCheck answers, without a credential and without touching any file,
// whether a bootstrap could proceed: BootstrapNone when it can, or the status
// it would end with (already enrolled, no Hub configured).
func PreCheck(ctx context.Context, hubURL string, st *store.CloudStore) BootstrapResult {
	if conn, err := st.Get(ctx); err == nil && conn != nil {
		return BootstrapResult{Status: BootstrapIgnoredIdentity, Detail: "this machine is already enrolled with a Coral Hub; disconnect it first to enroll again"}
	} else if err != nil && !errors.Is(err, store.ErrNoCloudConnection) {
		return BootstrapResult{Status: BootstrapRefused, Detail: "the local enrollment could not be read"}
	}
	if hubURL == "" {
		return BootstrapResult{Status: BootstrapNoHub, Detail: "no Coral Hub is configured (--cloud-url)"}
	}
	return BootstrapResult{}
}

func recordBootstrapStatus(ctx context.Context, o BootstrapOptions, res BootstrapResult) {
	if o.Settings == nil {
		return
	}
	b, _ := json.Marshal(map[string]string{"status": res.Status, "detail": res.Detail, "at": time.Now().UTC().Format(time.RFC3339)})
	if err := o.Settings.SetSetting(ctx, BootstrapStatusSetting, string(b)); err != nil {
		o.Logger.Warn("cloud bootstrap: could not record the status", "error", err)
	}
}
