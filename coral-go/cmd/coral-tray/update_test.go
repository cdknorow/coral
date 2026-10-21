package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tray asks the local server which release to offer (#5557); it never asks GitHub and never opens a URL the reply names.

func updateServer(t *testing.T, status int, body string, seen *atomic.Value) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			seen.Store(r.URL.RequestURI())
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func TestTheTrayAsksTheLocalServerAndOffersTheApprovedReleaseAsToday(t *testing.T) {
	var seen atomic.Value
	s := updateServer(t, 200, `{"available":true,"current":"1.4.141","latest":"1.4.142","channel":"approved","approved":true,"release_url":"https://evil.example/x"}`, &seen)
	a, err := askServerForUpdate(s.URL, t.TempDir(), false)
	require.NoError(t, err)
	assert.Equal(t, "/api/system/update-check", seen.Load())
	assert.True(t, a.Available)
	assert.Equal(t, "Update available: v1.4.142 (you have v1.4.141)", a.notice("Update available: v%s (you have v%s)", "1.4.141"))
	assert.Equal(t, "https://github.com/subgentic/coral/releases", a.pageURL(), "the page is made here; the reply's release_url is not read")
}

func TestTheMenuCheckAsksForARefresh(t *testing.T) {
	var seen atomic.Value
	s := updateServer(t, 200, `{"available":false,"current":"1.4.142","latest":"1.4.142","channel":"approved","approved":true}`, &seen)
	a, err := askServerForUpdate(s.URL, t.TempDir(), true)
	require.NoError(t, err)
	assert.False(t, a.Available)
	assert.Equal(t, "/api/system/update-check?refresh=1", seen.Load())
}

func TestOnTheAgenticChannelTheNoticeNamesIt(t *testing.T) {
	s := updateServer(t, 200, `{"available":true,"current":"1.4.141","latest":"1.4.144","channel":"agentic","approved":false}`, nil)
	a, err := askServerForUpdate(s.URL, t.TempDir(), false)
	require.NoError(t, err)
	assert.Equal(t, "Update available: v1.4.144 (you have v1.4.141) (Agentic channel, not yet approved)", a.notice("Update available: v%s (you have v%s)", "1.4.141"))
	assert.Equal(t, "https://github.com/subgentic/coral/releases/tag/v1.4.144", a.pageURL())
	b := updateAnswer{Available: true, Latest: "1.4.145", Channel: "agentic", Approved: boolPtr(true)}
	assert.Equal(t, "v1.4.145 from v1.4.141 (Agentic channel)", b.notice("v%s from v%s", "1.4.141"))
}

func boolPtr(b bool) *bool { return &b }

func TestTheTrayReadsTheChannelWordAsTheServerDoes(t *testing.T) {
	a := updateAnswer{Available: true, Latest: "1.4.145", Channel: " Agentic "}
	assert.Equal(t, "https://github.com/subgentic/coral/releases/tag/v1.4.145", a.pageURL())
}

func TestAChannelTheServerAnswersWithoutOrWithNonsenseIsApproved(t *testing.T) {
	for _, ch := range []string{"", "nightly", "AGENTIC;x", "all"} {
		a := updateAnswer{Available: true, Latest: "1.4.142", Channel: ch}
		assert.Equal(t, "v1.4.142 from v1.4.141", a.notice("v%s from v%s", "1.4.141"), ch)
		assert.Equal(t, "https://github.com/subgentic/coral/releases", a.pageURL(), ch)
	}
}

func TestTheTrayOffersNothingWhenTheServerDoesNotAnswerWell(t *testing.T) {
	for name, s := range map[string]*httptest.Server{
		"403":              updateServer(t, 403, `{"error":"no"}`, nil),
		"500":              updateServer(t, 500, ``, nil),
		"not json":         updateServer(t, 200, `<html>`, nil),
		"no version":       updateServer(t, 200, `{"available":true,"latest":""}`, nil),
		"a url as version": updateServer(t, 200, `{"available":true,"latest":"http://evil.example/x"}`, nil),
		"a path":           updateServer(t, 200, `{"available":true,"latest":"1.4.1/../../x"}`, nil),
	} {
		_, err := askServerForUpdate(s.URL, t.TempDir(), false)
		assert.Error(t, err, name)
	}
	_, err := askServerForUpdate("http://127.0.0.1:1", t.TempDir(), false)
	assert.Error(t, err, "a server that is not there")
}

func TestAnUpToDateAnswerNeedsNoValidVersion(t *testing.T) {
	s := updateServer(t, 200, `{"available":false,"current":"1.4.142","latest":""}`, nil)
	a, err := askServerForUpdate(s.URL, t.TempDir(), false)
	require.NoError(t, err)
	assert.False(t, a.Available)
}
