package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"strconv"
	"sync/atomic"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/server/routes"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
)

// Sign in with Coral Hub's wiring (#3966): internal/auth serves it; Coral Hub is reached through
// the connector (internal/cloud's Manager, set later by SetCloud) and the settings live in
// user_settings, written only by auth's own routes (PUT /api/settings refuses them).

// The local settings' keys.
const (
	HubSigninEnabledSetting   = "hub_signin_enabled"
	HubSigninOriginsSetting   = "hub_signin_origins"
	HubSigninDismissedSetting = "hub_signin_offer_dismissed"
)

// hubBackend adapts the connector to auth.HubBackend. Without a manager (no hub configured)
// nothing is ready; linked is the cloud handler's own answer, so both agree.
type hubBackend struct {
	m      *atomic.Pointer[cloud.Manager]
	linked func() bool
}

func (b hubBackend) mgr() *cloud.Manager { return b.m.Load() }

func (b hubBackend) Linked() bool { return b.linked() }

func (b hubBackend) Ready(ctx context.Context, origin string) bool {
	m := b.mgr()
	return m != nil && origin != "" && m.SigninReady(ctx, origin)
}

func hubErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, cloud.ErrSigninUnanswered):
		return auth.ErrHubUnanswered
	case errors.Is(err, cloud.ErrSigninDisabled):
		return auth.ErrHubDisabled
	}
	return err
}

func (b hubBackend) Start(ctx context.Context, challenge, state, origin string) (string, error) {
	m := b.mgr()
	if m == nil {
		return "", cloud.ErrNotLinked
	}
	r, err := m.StartSignin(ctx, challenge, state, origin)
	return r.URL, hubErr(err)
}

func (b hubBackend) Redeem(ctx context.Context, code, verifier string) (auth.HubGrant, error) {
	m := b.mgr()
	if m == nil {
		return auth.HubGrant{}, cloud.ErrNotLinked
	}
	g, err := m.RedeemSignin(ctx, code, verifier)
	return auth.HubGrant{Grant: g.Grant, Account: g.Account, Display: g.Display, TTL: g.TTL}, hubErr(err)
}

func (b hubBackend) Check(ctx context.Context, grant string) error {
	m := b.mgr()
	if m == nil {
		return cloud.ErrNotLinked // no connector at all: not linked, the session ends
	}
	return hubErr(m.CheckSignin(ctx, grant))
}

func (b hubBackend) Link(ctx context.Context) (string, string, error) {
	m := b.mgr()
	if m == nil {
		return "", "", cloud.ErrNotConfigured
	}
	if err := m.Connect(ctx); err != nil {
		return "", "", err
	}
	st := m.Status()
	if st.State != cloud.StateEnrolling || st.VerificationURL == "" {
		return "", "", errors.New("Coral Hub didn't start linking")
	}
	return st.VerificationURL, st.UserCode, nil
}

func (b hubBackend) HubPage() string {
	if m := b.mgr(); m != nil {
		return m.HubPage()
	}
	return ""
}

func (b hubBackend) LinkState() (string, string) {
	m := b.mgr()
	if m == nil {
		return "failed", "Coral Hub isn't configured on this computer"
	}
	st := m.Status()
	switch st.State {
	case cloud.StateEnrolling:
		return "enrolling", ""
	case cloud.StateConnected, cloud.StateConnecting, cloud.StateRetrying:
		return "linked", ""
	}
	msg := st.LastError
	if msg == "" {
		msg = "Linking stopped"
	}
	return "failed", msg
}

// hubSettings keeps Hub sign-in's settings in user_settings.
type hubSettings struct{ ss *store.SessionStore }

// all is the saved settings; an unreadable store reads as none saved.
func (h hubSettings) all() map[string]string {
	s, err := h.ss.GetSettings(context.Background())
	if err != nil {
		return nil
	}
	return s
}

func (h hubSettings) SigninEnabled() bool { return setreg.Bool(h.all(), HubSigninEnabledSetting) }

func (h hubSettings) SetSigninEnabled(on bool) error {
	return h.ss.SetSetting(context.Background(), HubSigninEnabledSetting, strconv.FormatBool(on))
}

// Origins are re-validated on every read: a value that doesn't parse is no origin.
func (h hubSettings) Origins() []string {
	var list []string
	if json.Unmarshal([]byte(setreg.Raw(h.all(), HubSigninOriginsSetting)), &list) != nil {
		return nil
	}
	var out []string
	for _, o := range list {
		if c, err := auth.CanonicalOrigin(o); err == nil && c == o {
			out = append(out, c)
		}
	}
	return out
}

func (h hubSettings) SetOrigins(origins []string) error {
	if origins == nil {
		origins = []string{}
	}
	b, _ := json.Marshal(origins)
	return h.ss.SetSetting(context.Background(), HubSigninOriginsSetting, string(b))
}

func (h hubSettings) OfferDismissed() bool { return setreg.Bool(h.all(), HubSigninDismissedSetting) }

func (h hubSettings) DismissOffer() error {
	return h.ss.SetSetting(context.Background(), HubSigninDismissedSetting, "true")
}

// newHubSignin wires Sign in with Coral Hub to this server.
func (s *Server) newHubSignin(ar *auth.Routes) *auth.HubSignin {
	ss := store.NewSessionStore(s.db)
	return ar.NewHubSignin(auth.HubSigninConfig{
		Hub:      hubBackend{m: &s.cloudMgr, linked: func() bool { return s.cloudHandler != nil && s.cloudHandler.HubLinked() }},
		Settings: hubSettings{ss: ss},
		OperatorAllowed: func() bool {
			st, err := ss.GetSettings(context.Background())
			return err == nil && !routes.HubOperatorDisabled(st)
		},
		// A Coral Hub organisation's machine refuses Hub sign-in (#7061); a record that cannot be read refuses too.
		TeamMachine: func() bool {
			kind, _, err := store.NewCloudStore(s.db, store.NewRemoteServerStore(s.db, s.cfg.CoralDir())).OwnerRecord(context.Background())
			return err != nil || kind != store.OwnerKindPersonal
		},
		LoopbackOrigins: s.loopbackOrigins,
		OriginsChanged: func() {
			if m := s.cloudMgr.Load(); m != nil {
				m.SigninOriginsChanged()
			}
		},
		OnAction: func(ctx context.Context, who, method, path string, status int) {
			routes.RecordHubSigninAction(ctx, s.boardStore, who, method, path, status)
		},
	})
}

// loopbackOrigins are this server's own loopback origins for Hub sign-in: http on its port,
// and https as well when it serves TLS on that port (--tls-cert; the port speaks both), so a
// dashboard opened at https://localhost gets the Hub button too.
func (s *Server) loopbackOrigins() []string {
	p := strconv.Itoa(s.cfg.Port)
	schemes := []string{"http"}
	if s.cfg.TLS {
		schemes = append(schemes, "https")
	}
	var out []string
	for _, sc := range schemes {
		out = append(out, sc+"://localhost:"+p, sc+"://"+net.JoinHostPort("127.0.0.1", p), sc+"://"+net.JoinHostPort("::1", p))
	}
	return out
}

// HubSigninOrigins is the return-origin list the connector sends Coral Hub.
func (s *Server) HubSigninOrigins() []string {
	if s.hubSignin == nil {
		return nil
	}
	return s.hubSignin.Origins()
}

// HubSigninEvent acts on the connector's owner-pin events: another account named ends every
// Hub session; a pin is recorded.
func (s *Server) HubSigninEvent(ctx context.Context, ev cloud.SigninEvent) {
	switch ev.Kind {
	case cloud.SigninOwnerMismatch:
		n := s.hubSignin.EndAll(ctx, "Coral Hub named another account than this computer's owner")
		log.Printf("[auth] Coral Hub named another owner: Hub sign-in is off until this computer is linked again; %d session(s) ended", n)
		routes.RecordHubSigninAction(ctx, s.boardStore, "this computer", "EVENT", "Coral Hub named another owner: Hub sign-in off until re-linked", 0)
	case cloud.SigninOwnerPinned:
		routes.RecordHubSigninAction(ctx, s.boardStore, "this computer", "EVENT", "Coral Hub owner pinned ("+ev.How+")", 0)
	}
}

// EndHubSessions ends every Hub session (the link was removed or revoked).
func (s *Server) EndHubSessions(ctx context.Context, why string) {
	if s.hubSignin != nil {
		s.hubSignin.EndAll(ctx, why)
	}
}
