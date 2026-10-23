package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"fyne.io/systray"
	"github.com/gen2brain/beeep"

	"github.com/subgentic/coral/internal/cliauth"
)

// The setup code in the tray menu (#6410, Security's #6408 rank 1 option A): the one-time code that another device's sign-in page, or "Link this computer to Coral Hub", asks for, shown where
// the person is and not only in a terminal ('coral setup-code'). It is the server's own answer to the signed command line (GET /api/auth/setup-code): the same code, the same expiry, nothing the
// tray keeps. The code is never written to tray.log (this file logs no part of an answer), and there is none to show once a password is set.

// errNoSetupCode is the server's answer when a password is set: there is no setup code, and the menu item goes.
var errNoSetupCode = errors.New("a password is set, so there is no setup code")

type setupCodeAnswer struct {
	Code      string `json:"code"`
	ExpiresIn int    `json:"expires_in"`
}

// askServerForSetupCode reads the live setup code, signed with the command-line token as `coral setup-code` is.
func askServerForSetupCode(baseURL, dataDir string) (setupCodeAnswer, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	if tok, err := cliauth.ReadToken(dataDir); err == nil {
		client.Transport = cliauth.Transport{Base: http.DefaultTransport, Token: tok, Dir: dataDir}
	}
	resp, err := client.Get(baseURL + "/api/auth/setup-code")
	if err != nil {
		return setupCodeAnswer{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusConflict:
		return setupCodeAnswer{}, errNoSetupCode
	default:
		return setupCodeAnswer{}, fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	var a setupCodeAnswer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&a); err != nil {
		return setupCodeAnswer{}, err
	}
	if len(a.Code) != 9 || a.Code[4] != '-' {
		return setupCodeAnswer{}, errors.New("the server's answer holds no setup code")
	}
	return a, nil
}

// setupCodeTitle is the menu line once the code is read: the code and how long it lasts.
func setupCodeTitle(a setupCodeAnswer) string {
	mins := (a.ExpiresIn + 59) / 60
	if mins < 1 {
		mins = 1
	}
	unit := "minutes"
	if mins == 1 {
		unit = "minute"
	}
	return fmt.Sprintf("Setup code: %s (%d %s left)", a.Code, mins, unit)
}

const setupCodeMenuTitle = "Show setup code"

// setupCodeItem is the menu item: hidden while there is no code to show (a password is set, or the server does not answer), "Show setup code" while there is, and the code and its
// time left once the person has clicked it. It looks again every minute so it goes when a password is set, and the time left stays true.
type setupCodeItem struct {
	item     menuItem
	revealed atomic.Bool
	gen      atomic.Int64 // each click is a generation: only the last click's timer puts the title back
	hideIn   time.Duration
}

const (
	setupCodeLookEvery = time.Minute
	// The code is on the menu line only for a short while after the click: the menu is then the plain item again (Security's #6410 LOW), and a click shows it again.
	setupCodeShownFor = 2 * time.Minute
)

func newSetupCodeItem(item *systray.MenuItem) *setupCodeItem {
	return &setupCodeItem{item: item, hideIn: setupCodeShownFor}
}

func (s *setupCodeItem) look(baseURL, dataDir string) {
	a, err := askServerForSetupCode(baseURL, dataDir)
	if err != nil {
		s.gen.Add(1)
		s.revealed.Store(false)
		s.item.Hide() // none to show (a password is set) or the server is not answering: nothing to offer
		return
	}
	if s.revealed.Load() {
		s.item.SetTitle(setupCodeTitle(a))
	} else {
		s.item.SetTitle(setupCodeMenuTitle)
	}
	s.item.Show()
}

// click reads the code and puts it in the item's own title. A failure is a notification that names no code.
func (s *setupCodeItem) click(baseURL, dataDir string) {
	a, err := askServerForSetupCode(baseURL, dataDir)
	if err != nil {
		s.gen.Add(1)
		s.revealed.Store(false)
		s.item.Hide()
		if !errors.Is(err, errNoSetupCode) {
			log.Printf("[setup-code] could not read the setup code: %v", err) // the error, never an answer
			_ = beeep.Notify("Coral", "Could not read the setup code. Run 'coral setup-code' in a terminal.", "")
		}
		return
	}
	s.revealed.Store(true)
	s.item.SetTitle(setupCodeTitle(a))
	s.item.Show()
	g := s.gen.Add(1)
	time.AfterFunc(s.hideIn, func() {
		if s.gen.Load() != g || !s.revealed.Swap(false) { // a later click, or the item already went, owns the title
			return
		}
		s.item.SetTitle(setupCodeMenuTitle)
	})
}

func (s *setupCodeItem) run(ctx context.Context, baseURL, dataDir string) {
	s.look(baseURL, dataDir)
	t := time.NewTicker(setupCodeLookEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.look(baseURL, dataDir)
		}
	}
}
