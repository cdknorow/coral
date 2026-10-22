package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/tracking"
)

// The tray's "Update to vX available" menu item (updater D, #6006). It is shown when the local server reports a newer APPROVED release and the update feature is on; it opens the dashboard's
// Updates pane when the updater is on in this build, and the release's download page while the apply is off. With the feature off (the status route answers 404), an unreachable server, no newer
// release or a release that is not approved, there is no item. Nothing in an answer is a URL: the page is made here from the compiled-in repository and a version that passed ValidVersion.

const (
	updateItemFirst = 20 * time.Second // after the startup notice's own wait for the server
	updateItemEvery = 30 * time.Minute // the server's answer is cached for an hour: asking more often adds nothing
	// updatesPaneLink is the dashboard page the item opens: Settings on its Updates tab. The dashboard accepts this one value and no other (app.js).
	updatesPaneLink = "/?settings=updates"
)

// updaterStatus is what the tray reads of GET /api/system/update: whether the route exists (the feature is on) and whether the updater is on in this build.
type updaterStatus struct {
	FeatureOn bool
	Enabled   bool
}

// askServerForUpdaterStatus calls the server's updater status, signed with Coral's command-line token. A 404 is the feature being off (no error); anything else that is not a 200 is an error.
func askServerForUpdaterStatus(baseURL, dataDir string) (updaterStatus, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	if tok, err := cliauth.ReadToken(dataDir); err == nil {
		client.Transport = cliauth.Transport{Base: http.DefaultTransport, Token: tok, Dir: dataDir}
	}
	resp, err := client.Get(baseURL + "/api/system/update")
	if err != nil {
		return updaterStatus{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return updaterStatus{}, nil
	case http.StatusOK:
	default:
		return updaterStatus{}, fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	var body struct {
		Enabled bool `json:"enabled"` // anything but a JSON true is false
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&body); err != nil {
		return updaterStatus{}, err
	}
	return updaterStatus{FeatureOn: true, Enabled: body.Enabled}, nil
}

// updateChoice is what the item says and does.
type updateChoice struct {
	Show    bool
	Title   string
	Pane    bool   // open the dashboard's Updates pane; else the release's page
	Page    string // the release page (when not Pane)
	Version string
	Channel string
}

// chooseUpdateItem decides from the two answers; the errors are the failures to get them.
func chooseUpdateItem(a updateAnswer, askErr error, st updaterStatus, stErr error) updateChoice {
	switch {
	case askErr != nil || !a.Available:
		return updateChoice{} // no newer release, or the server did not say
	case a.Approved != nil && !*a.Approved:
		return updateChoice{} // a release the Agentic channel offers before it is approved is not a notice
	case stErr != nil || !st.FeatureOn:
		return updateChoice{} // the update feature is off (or the server could not say): nothing
	}
	return updateChoice{Show: true, Title: fmt.Sprintf("Update to v%s available", a.Latest), Pane: st.Enabled, Page: a.pageURL(), Version: a.Latest, Channel: string(a.channelOf())}
}

// menuItem is the part of a systray item the loop needs.
type menuItem interface {
	Show()
	Hide()
	SetTitle(string)
}

// updateItem keeps the menu item in step with the server's answers.
type updateItem struct {
	item   menuItem
	ask    func() (updateAnswer, error)
	status func() (updaterStatus, error)
	track  func(version, channel string) // the notice was shown: update_notice_shown (once per release, only with analytics on, tracking's own rule)
	cur    atomic.Value                  // updateChoice
	shown  string                        // the version the item last said
}

func (u *updateItem) choice() updateChoice {
	c, _ := u.cur.Load().(updateChoice)
	return c
}

// refresh asks and updates the item: shown with its title and the notice counted when there is something to say, hidden otherwise.
func (u *updateItem) refresh() {
	a, aerr := u.ask()
	var st updaterStatus
	var serr error
	if aerr == nil && a.Available { // the status is only worth a request when there is a release to say
		st, serr = u.status()
	}
	c := chooseUpdateItem(a, aerr, st, serr)
	u.cur.Store(c)
	if !c.Show {
		u.item.Hide()
		u.shown = ""
		return
	}
	u.item.SetTitle(c.Title)
	u.item.Show()
	if u.shown != c.Version {
		u.shown = c.Version
		u.track(c.Version, c.Channel)
	}
}

// run refreshes on a schedule until ctx ends. A build whose tier does not check for updates never asks.
func (u *updateItem) run(ctx context.Context) {
	if !config.TierChecksForUpdates {
		return
	}
	for wait := updateItemFirst; ; wait = updateItemEvery {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		u.refresh()
	}
}

// open is what a click does: the dashboard's Updates pane, or the release page (and then the offer is counted, as the page the notice offers was opened).
func (u *updateItem) open(openPane func(), openPage func(string), trackOffer func(version, channel string)) {
	c := u.choice()
	if !c.Show {
		return
	}
	if c.Pane {
		openPane()
		return
	}
	openPage(c.Page)
	trackOffer(c.Version, c.Channel)
}

func trackTrayNotice(version, channel string) {
	tracking.TrackUpdateNotice(version, tracking.UpdateSourceTray, channel)
}

func trackTrayOffer(version, channel string) {
	tracking.TrackUpdateOffer(version, tracking.UpdateSourceTray, channel)
}
