package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/updatecheck"
)

// The tray asks the local Coral server which release to offer instead of asking GitHub itself (#5557): the server knows the update channel the owner
// chose, and one cache serves the dashboard, the server's log and the tray, so the three never add up to more than one request an hour to GitHub's
// 60-an-hour anonymous limit. A server that does not answer offers nothing: the tray is of no use without it.

// updateAnswer is the part of GET /api/system/update-check the tray reads. Nothing in it is a URL: the page that opens is made here, from the
// compiled-in repository and a version that passed ValidVersion.
type updateAnswer struct {
	Available bool   `json:"available"`
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Channel   string `json:"channel"`
	Approved  *bool  `json:"approved"`
}

// askServerForUpdate calls the server's own update check, signed with Coral's command-line token (strict local auth, #3418). refresh is the menu's
// "Check for Updates": the server asks GitHub again at most once a minute.
func askServerForUpdate(baseURL, dataDir string, refresh bool) (updateAnswer, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	if tok, err := cliauth.ReadToken(dataDir); err == nil {
		client.Transport = cliauth.Transport{Base: http.DefaultTransport, Token: tok, Dir: dataDir}
	}
	u := baseURL + "/api/system/update-check"
	if refresh {
		u += "?refresh=1"
	}
	resp, err := client.Get(u)
	if err != nil {
		return updateAnswer{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return updateAnswer{}, fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	var a updateAnswer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&a); err != nil {
		return updateAnswer{}, err
	}
	if a.Available && !updatecheck.ValidVersion(a.Latest) {
		return updateAnswer{}, errors.New("the server's answer holds no version")
	}
	return a, nil
}

// channelOf is the channel of an answer: anything but agentic is approved.
func (a updateAnswer) channelOf() updatecheck.Channel { return updatecheck.ParseChannel(a.Channel) }

// notice is the notification text for an available update, with the channel named when it is Agentic.
func (a updateAnswer) notice(format string, current string) string {
	msg := fmt.Sprintf(format, a.Latest, current)
	if a.channelOf() == updatecheck.Agentic {
		msg += " (Agentic channel"
		if a.Approved != nil && !*a.Approved {
			msg += ", not yet approved"
		}
		msg += ")"
	}
	return msg
}

// pageURL is the page to open for an available update.
func (a updateAnswer) pageURL() string { return updatecheck.ReleasePageURL(a.channelOf(), a.Latest) }
