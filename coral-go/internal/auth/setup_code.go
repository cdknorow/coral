package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/subgentic/coral/internal/httputil"
)

// Creating the first password from the sign-in page (#3949; the operator, via the Orchestrator;
// settings by Security). From this computer (fromThisComputer) the form needs nothing else; from
// anywhere else it also needs the one-time setup code, so whoever reaches the port first can't
// claim the install. The code is 8 characters of Crockford's base32 (32 symbols, about 40 bits,
// no I, L, O or U; a typed O counts as 0 and I or L as 1), from crypto/rand, shown XXXX-XXXX. It
// is used once, lasts setupCodeTTL, is replaced when a password is set and on every start, and
// after setupWrongMax wrong codes in total. It is printed on the console and by
// `coral setup-code` only, never in coral.log (people paste logs into bug reports).

const (
	setupAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	setupCodeLen  = 8
	setupCodeTTL  = 60 * time.Minute
	setupWrongMax = 20
)

// setupConsole is where a new setup code is printed (stderr; tests replace it), and
// setupConsoleIsTerminal says whether it is a terminal. The tray and the app send the server's
// stderr to a log file (tray.log) that people attach to bug reports, so the code is printed only
// to a terminal; otherwise 'coral setup-code' is the way to get it (Security, #3949 round 1).
var (
	setupConsole           io.Writer = os.Stderr
	setupConsoleIsTerminal           = func() bool { return term.IsTerminal(int(os.Stderr.Fd())) }
)

func newSetupCode() string {
	b := make([]byte, setupCodeLen)
	max := big.NewInt(int64(len(setupAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b[i] = setupAlphabet[n.Int64()]
	}
	return string(b)
}

// FormatSetupCode shows a code as two groups of four (ABCD-EFGH).
func FormatSetupCode(c string) string {
	if len(c) != setupCodeLen {
		return c
	}
	return c[:4] + "-" + c[4:]
}

// normalizeSetupCode accepts what a person types: any case, spaces and dashes, O for 0, I or L
// for 1.
func normalizeSetupCode(s string) string {
	s = strings.ToUpper(s)
	return strings.NewReplacer("-", "", " ", "", "\t", "", "O", "0", "I", "1", "L", "1").Replace(s)
}

// setupCodeLocked returns the live code, making (and printing) a new one when there is none or
// it expired. ks.mu must be held.
func (ks *KeyStore) setupCodeLocked(now time.Time) string {
	if ks.setupCode == "" || !now.Before(ks.setupExpires) {
		ks.setupCode = newSetupCode()
		ks.setupExpires = now.Add(setupCodeTTL)
		ks.setupWrong = 0
		if setupConsoleIsTerminal() {
			fmt.Fprintf(setupConsole, "Coral: no dashboard password yet. To create one from another computer, the sign-in page asks for this one-time setup code: %s (valid %d minutes; 'coral setup-code' prints a fresh one). Do not share it.\n",
				FormatSetupCode(ks.setupCode), int(setupCodeTTL.Minutes()))
		}
	}
	return ks.setupCode
}

// SetupCode returns the live setup code ("" once a password is set).
func (ks *KeyStore) SetupCode() string {
	if ks.PasswordSet() {
		return ""
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	return ks.setupCodeLocked(time.Now())
}

// checkSetupCode compares in constant time; an expired, used or absent code never matches. A
// wrong one counts toward setupWrongMax, after which the code is replaced.
func (ks *KeyStore) checkSetupCode(typed string) bool {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	want := ks.setupCode
	live := want != "" && time.Now().Before(ks.setupExpires)
	got := normalizeSetupCode(typed)
	ok := live && len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
	if !ok && want != "" {
		ks.setupWrong++
		if ks.setupWrong >= setupWrongMax {
			log.Printf("[auth] %d wrong setup codes: the setup code was replaced (the new one is on the console and from 'coral setup-code')", ks.setupWrong)
			ks.setupCode = ""
			ks.setupCodeLocked(time.Now())
		}
	}
	return ok
}

// retireSetupCode ends the current code (a password was set).
func (ks *KeyStore) retireSetupCode() {
	ks.mu.Lock()
	ks.setupCode = ""
	ks.mu.Unlock()
}

// SetPeerUser tells the routes the peer-user check: whether a loopback request comes from
// Coral's own OS user, and whether the system can tell.
func (ar *Routes) SetPeerUser(f func(*http.Request) (same, supported bool)) {
	if f != nil {
		ar.peerUser = f
	}
}

// fromThisComputer is the person at this computer, for creating the password without a code
// (Security's three conditions): IsDirectLocal (a loopback peer, no proxy header, no Coral Hub
// stamp, and require_auth_on_loopback off), a localhost Host, and the peer-user check positively
// saying the same OS user. "Unsupported" is not enough. Known limit: a reverse proxy on this
// computer that adds no forwarding header makes a remote caller look local.
func (ar *Routes) fromThisComputer(r *http.Request) bool {
	if !IsDirectLocal(r) || !AcceptedLocalHost(r.Host) {
		return false
	}
	same, supported := ar.peerUser(r)
	return supported && same
}

// PageState is what the sign-in page shows for r (#3948 renders it): whether a password is set,
// whether r is from this computer (no setup code needed), and whether strict local auth is on.
func (ar *Routes) PageState(r *http.Request) (passwordSet, local, strict bool) {
	return ar.ks.PasswordSet(), ar.fromThisComputer(r), ar.strictLocal
}

// CreatePassword creates the first password from the sign-in page and signs this browser in.
// POST /auth/password/create {"password": "...", "keep": bool, "code": "ABCD-EFGH"}
func (ar *Routes) CreatePassword(w http.ResponseWriter, r *http.Request) {
	if refuseAgent(w, r) {
		return
	}
	if !sameOriginWrite(r, true) {
		authError(w, http.StatusForbidden, "cross_origin", "Create the password from Coral's own page")
		return
	}
	var body struct {
		Password string `json:"password"`
		Keep     bool   `json:"keep"`
		Code     string `json:"code"`
	}
	if !readPasswordBody(w, r, &body) {
		return
	}
	if ar.ks.PasswordSet() {
		authError(w, http.StatusConflict, "password_set", "A password is already set: sign in with it")
		return
	}
	ip := clientIP(r)
	label := sessionLabel(r.UserAgent())
	codeOK := ar.fromThisComputer(r)
	if !codeOK {
		if wait := ar.throttle.delay(ip); wait > 0 {
			tooMany(w, wait)
			return
		}
		release, ok := ar.throttle.acquire(ip)
		if !ok {
			tooMany(w, slotWait)
			return
		}
		codeOK = ar.ks.checkSetupCode(body.Code)
		if codeOK {
			ar.throttle.success(ip)
		} else {
			ar.throttle.fail(ip)
		}
		release()
		if !codeOK {
			ar.audit.add(ip, label, false)
			log.Printf("[auth] wrong setup code from %s (%s)", ip, label)
			authError(w, http.StatusUnauthorized, "bad_code", "That setup code is not right")
			return
		}
	}
	phc, ok := ar.hashNew(w, r, body.Password)
	if !ok {
		return
	}
	if err := CreatePasswordHash(ar.ks.dataDir(), phc); err != nil {
		if errors.Is(err, ErrPasswordExists) {
			authError(w, http.StatusConflict, "password_set", "A password is already set: sign in with it")
			return
		}
		log.Printf("[auth] couldn't save the password: %v", err)
		authError(w, http.StatusInternalServerError, "save_failed", "The password could not be saved")
		return
	}
	ar.ks.retireSetupCode()
	ar.audit.add(ip, label, true)
	log.Printf("[auth] a password was created on the sign-in page from %s (%s)", ip, label)
	if old := ar.ks.extractSessionCookie(r); old != "" {
		ar.ks.RevokeSession(old)
	}
	token, err := ar.ks.NewSession(r, SessionByPassword, body.Keep)
	if err != nil {
		authError(w, http.StatusInternalServerError, "session_failed", "The password is set, but this browser could not be signed in: sign in with it")
		return
	}
	ar.ks.setSessionCookie(w, r, token, body.Keep)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GetSetupCode returns a setup code to the signed command line only (`coral setup-code`).
// GET /api/auth/setup-code
func (ar *Routes) GetSetupCode(w http.ResponseWriter, r *http.Request) {
	if !ar.cliVerified(r) {
		authError(w, http.StatusForbidden, "cli_only", "Run 'coral setup-code' on the computer running Coral")
		return
	}
	c := ar.ks.SetupCode()
	if c == "" {
		authError(w, http.StatusConflict, "password_set", "A password is already set, so there is no setup code")
		return
	}
	ar.ks.mu.RLock()
	left := time.Until(ar.ks.setupExpires)
	ar.ks.mu.RUnlock()
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"code": FormatSetupCode(c), "expires_in": int(left.Seconds())})
}

// ShowSetupCode returns the setup code to the person at this computer who is signed in to the dashboard: Settings, the tray and the app show it
// where they are, not only in a terminal (#6410, Security's #6408 rank 1 option A). It is the same code, expiry and throttle as `coral setup-code`
// (the checks that spend it are unchanged); the route is the person's alone: a browser session (a link, the key form or the password), on this
// computer (fromThisComputer), never an agent's token and never through Coral Hub's tunnel, and it is not in the tunnel's table. The code is
// not logged, and the answer is never cached.
// GET /api/auth/setup-code/show
func (ar *Routes) ShowSetupCode(w http.ResponseWriter, r *http.Request) {
	if refuseAgent(w, r) {
		return
	}
	if r.Header.Get("X-Coral-Via-Tunnel") != "" {
		authError(w, http.StatusForbidden, "not_allowed", "The setup code is shown on the computer Coral runs on")
		return
	}
	if !ar.signedIn(r) {
		notSignedIn(w)
		return
	}
	if !ar.fromThisComputer(r) {
		authError(w, http.StatusForbidden, "this_computer_only", "The setup code is shown on the computer Coral runs on")
		return
	}
	c := ar.ks.SetupCode()
	if c == "" {
		authError(w, http.StatusConflict, "password_set", "A password is already set, so there is no setup code")
		return
	}
	ar.ks.mu.RLock()
	left := time.Until(ar.ks.setupExpires)
	ar.ks.mu.RUnlock()
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"code": FormatSetupCode(c), "expires_in": int(left.Seconds())})
}
