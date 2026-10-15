package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// #3619 (hub audit H1): a board subscription's webhook URL is a stored outbound address that
// keeps receiving board traffic after the hub session ends, so it is set only from this
// computer: a tunneled subscribe naming one is refused (403) and stores nothing; without one it
// works; a local request is unchanged.
func TestSubscribeWebhookURLIsNotSetThroughTheHub(t *testing.T) {
	srv, handler := setupBoardTestServer(t)
	const hook = "https://93.184.216.34/hook" // a public address: accepted locally
	subscribe := func(via, body string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest("POST", srv.URL+"/api/board/team/subscribe", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if via != "" {
			req.Header.Set(tunnel.ViaHeader, via)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	webhooks := func() map[string]string {
		subs, err := handler.bs.ListSubscribers(context.Background(), "team")
		require.NoError(t, err)
		got := map[string]string{}
		for _, s := range subs {
			if s.WebhookURL != nil {
				got[s.SubscriberID] = *s.WebhookURL
			} else {
				got[s.SubscriberID] = ""
			}
		}
		return got
	}

	for _, via := range []string{cloud.ViaSecret(), "forged"} {
		code, out := subscribe(via, `{"subscriber_id":"Hooked","job_title":"Dev","webhook_url":"`+hook+`"}`)
		assert.Equal(t, http.StatusForbidden, code, out)
		assert.Equal(t, "webhook_not_via_hub", out["code"])
	}
	_, stored := webhooks()["Hooked"]
	assert.False(t, stored, "a refused subscribe stores nothing")

	code, out := subscribe(cloud.ViaSecret(), `{"subscriber_id":"Plain","job_title":"Dev"}`)
	assert.Equal(t, http.StatusOK, code, out)
	code, out = subscribe(cloud.ViaSecret(), `{"subscriber_id":"Empty","job_title":"Dev","webhook_url":""}`)
	assert.Equal(t, http.StatusOK, code, out)

	code, out = subscribe("", `{"subscriber_id":"Local","job_title":"Dev","webhook_url":"`+hook+`"}`)
	require.Equal(t, http.StatusOK, code, out)
	got := webhooks()
	assert.Equal(t, hook, got["Local"], "a local request still sets one")
	assert.Equal(t, "", got["Plain"])
	assert.Equal(t, "", got["Empty"])
}
