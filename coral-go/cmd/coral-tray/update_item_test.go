package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/loginlink"
)

// The tray's "Update to vX available" item (updater D, #6006): on, the apply off, the feature off, no update, the server unreachable, and what a click opens.

func approvedAnswer(latest string) updateAnswer {
	t := true
	return updateAnswer{Available: true, Current: "1.4.155", Latest: latest, Channel: "approved", Approved: &t}
}

func TestTheItemSaysWhatTheTwoAnswersSayAndNothingElse(t *testing.T) {
	notApproved := false
	agentic := approvedAnswer("1.4.158")
	agentic.Channel = "agentic"
	unapproved := updateAnswer{Available: true, Latest: "1.4.159", Channel: "agentic", Approved: &notApproved}
	for name, tc := range map[string]struct {
		a      updateAnswer
		aerr   error
		st     updaterStatus
		sterr  error
		show   bool
		pane   bool
		title  string
		page   string
		agentc bool
	}{
		"the updater is on: the dashboard's Updates pane": {a: approvedAnswer("1.4.157"), st: updaterStatus{FeatureOn: true, Enabled: true}, show: true, pane: true, title: "Update to v1.4.157 available", page: "https://github.com/subgentic/coral/releases"},
		"the apply is off: the download page":             {a: approvedAnswer("1.4.157"), st: updaterStatus{FeatureOn: true}, show: true, pane: false, title: "Update to v1.4.157 available", page: "https://github.com/subgentic/coral/releases"},
		"the feature is off (the status route is a 404)":  {a: approvedAnswer("1.4.157"), st: updaterStatus{}},
		"no newer release":                                {a: updateAnswer{Available: false, Latest: "1.4.155"}, st: updaterStatus{FeatureOn: true, Enabled: true}},
		"the update check cannot be asked":                {aerr: errors.New("connection refused"), st: updaterStatus{FeatureOn: true, Enabled: true}},
		"the status cannot be asked":                      {a: approvedAnswer("1.4.157"), sterr: errors.New("connection refused")},
		"an Agentic release that is not approved":         {a: unapproved, st: updaterStatus{FeatureOn: true, Enabled: true}},
		"an approved release on the Agentic channel":      {a: agentic, st: updaterStatus{FeatureOn: true}, show: true, title: "Update to v1.4.158 available", page: "https://github.com/subgentic/coral/releases/tag/v1.4.158", agentc: true},
	} {
		c := chooseUpdateItem(tc.a, tc.aerr, tc.st, tc.sterr)
		assert.Equal(t, tc.show, c.Show, name)
		if !tc.show {
			assert.Equal(t, updateChoice{}, c, name+": nothing at all")
			continue
		}
		assert.Equal(t, tc.title, c.Title, name)
		assert.Equal(t, tc.pane, c.Pane, name)
		assert.Equal(t, tc.page, c.Page, name)
		assert.Equal(t, tc.a.Latest, c.Version, name)
		assert.Equal(t, map[bool]string{true: "agentic", false: "approved"}[tc.agentc], c.Channel, name)
	}
}

func TestTheUpdaterStatusIsReadFromTheLocalServerAndTheFeatureOffIsNotAnError(t *testing.T) {
	serve := func(status int, body string, seen *atomic.Value) string {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if seen != nil {
				seen.Store(r.URL.RequestURI())
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(s.Close)
		return s.URL
	}
	var seen atomic.Value
	st, err := askServerForUpdaterStatus(serve(200, `{"enabled":true,"installed":"1.4.155"}`, &seen), t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, updaterStatus{FeatureOn: true, Enabled: true}, st)
	assert.Equal(t, "/api/system/update", seen.Load())

	st, err = askServerForUpdaterStatus(serve(200, `{"enabled":false}`, nil), t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, updaterStatus{FeatureOn: true}, st, "the feature is on and the updater is not")
	st, err = askServerForUpdaterStatus(serve(404, `{"error":"not found"}`, nil), t.TempDir())
	require.NoError(t, err, "a 404 is the feature being off")
	assert.Equal(t, updaterStatus{}, st)

	for name, url := range map[string]string{
		"403": serve(403, `{}`, nil), "500": serve(500, ``, nil), "not json": serve(200, `<html>`, nil), "enabled as a string": serve(200, `{"enabled":"true"}`, nil), "unreachable": "http://127.0.0.1:1",
	} {
		_, err := askServerForUpdaterStatus(url, t.TempDir())
		assert.Error(t, err, name)
	}
}

type fakeItem struct {
	shown  bool
	title  string
	shows  int
	hides  int
	titles []string
}

func (f *fakeItem) Show()             { f.shown = true; f.shows++ }
func (f *fakeItem) Hide()             { f.shown = false; f.hides++ }
func (f *fakeItem) SetTitle(s string) { f.title = s; f.titles = append(f.titles, s) }

func newItem(a *updateAnswer, aerr *error, st *updaterStatus) (*updateItem, *fakeItem, *[]string, *int) {
	f := &fakeItem{}
	var noticed []string
	statusAsked := 0
	u := &updateItem{item: f,
		ask:    func() (updateAnswer, error) { return *a, *aerr },
		status: func() (updaterStatus, error) { statusAsked++; return *st, nil },
		track:  func(v, c string) { noticed = append(noticed, v+" "+c) }}
	return u, f, &noticed, &statusAsked
}

func TestTheItemAppearsDisappearsAndCountsTheNoticeOncePerRelease(t *testing.T) {
	a, aerr, st := approvedAnswer("1.4.157"), error(nil), updaterStatus{FeatureOn: true, Enabled: true}
	u, f, noticed, statusAsked := newItem(&a, &aerr, &st)

	u.refresh()
	assert.True(t, f.shown)
	assert.Equal(t, "Update to v1.4.157 available", f.title)
	assert.Equal(t, []string{"1.4.157 approved"}, *noticed, "the notice was shown: counted")
	u.refresh()
	assert.Equal(t, []string{"1.4.157 approved"}, *noticed, "the same release again: not counted again")

	a = approvedAnswer("1.4.158") // a newer one
	u.refresh()
	assert.Equal(t, "Update to v1.4.158 available", f.title)
	assert.Equal(t, []string{"1.4.157 approved", "1.4.158 approved"}, *noticed)

	a = updateAnswer{Available: false, Latest: "1.4.158"} // installed since
	before := *statusAsked
	u.refresh()
	assert.False(t, f.shown, "hidden when there is nothing to say")
	assert.Equal(t, before, *statusAsked, "the status is not asked when there is no release to say")
	assert.False(t, u.choice().Show)

	aerr = errors.New("the server stopped") // the server is gone: nothing
	a = approvedAnswer("1.4.158")
	u.refresh()
	assert.False(t, f.shown)

	aerr = nil
	st = updaterStatus{} // the feature is off
	u.refresh()
	assert.False(t, f.shown)
	assert.Len(t, *noticed, 2, "nothing was counted for what was never shown")
}

func TestAClickOpensThePaneOrThePageAndCountsTheOfferOnlyForThePage(t *testing.T) {
	a, aerr, st := approvedAnswer("1.4.157"), error(nil), updaterStatus{FeatureOn: true, Enabled: true}
	u, _, _, _ := newItem(&a, &aerr, &st)
	var pane int
	var pages, offers []string
	click := func() {
		u.open(func() { pane++ }, func(p string) { pages = append(pages, p) }, func(v, c string) { offers = append(offers, v+" "+c) })
	}
	click() // before anything is shown: nothing
	assert.Zero(t, pane)
	assert.Empty(t, pages)

	u.refresh()
	click()
	assert.Equal(t, 1, pane, "the updater is on: the dashboard's Updates pane")
	assert.Empty(t, pages)
	assert.Empty(t, offers, "opening the dashboard is not opening the release page")

	st = updaterStatus{FeatureOn: true} // the apply is off
	u.refresh()
	click()
	assert.Equal(t, 1, pane)
	assert.Equal(t, []string{"https://github.com/subgentic/coral/releases"}, pages)
	assert.Equal(t, []string{"1.4.157 approved"}, offers, "the release page was opened: the offer is counted")
}

func TestTheJobDoesNothingWhenItsContextHasEnded(t *testing.T) {
	if !config.TierChecksForUpdates {
		t.Skip("a build of another tier never asks")
	}
	a, aerr, st := approvedAnswer("1.4.157"), error(nil), updaterStatus{FeatureOn: true}
	u, f, _, _ := newItem(&a, &aerr, &st)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	u.run(ctx)
	assert.Zero(t, f.shows, "an ended context asks nothing")
}

// The menu wiring is the thin part nothing can run without a desktop: pinned from the source, and the link is one the dashboard accepts.
func TestTheMenuItemIsHiddenAtStartAndItsLinkIsAPageOfTheDashboard(t *testing.T) {
	b, err := os.ReadFile("main.go")
	require.NoError(t, err)
	src := string(b)
	assert.Contains(t, src, `mUpdateAvail := systray.AddMenuItem("", "A newer release of Coral is out")`)
	assert.Contains(t, src, "mUpdateAvail.Hide()", "no item until the server says there is something to say")
	assert.Contains(t, src, "loginlink.OnPage(loginlink.Or(url, mintLink), updatesPaneLink)", "the pane opens signed in, on the one link the dashboard accepts")
	assert.Equal(t, "/?settings=updates", updatesPaneLink)
	assert.True(t, loginlink.ValidPage(updatesPaneLink))
	assert.False(t, strings.Contains(updatesPaneLink, "#"))
}
