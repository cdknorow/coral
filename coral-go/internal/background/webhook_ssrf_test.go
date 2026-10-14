package background

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cdknorow/coral/internal/httputil"
)

// #3363: job-run callbacks and notification webhooks are posted with the guarded client:
// a target on this machine is refused at connect time (whatever was saved earlier).
func TestRunAndNotificationWebhooksRefusePrivateTargets(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	for name, c := range map[string]*http.Client{
		"job-run callback":     runWebhookClient,
		"notification webhook": NewWebhookDispatcher(nil, time.Minute).client,
	} {
		req, _ := http.NewRequest("POST", srv.URL, strings.NewReader("{}"))
		_, err := c.Do(req)
		assert.ErrorIs(t, err, httputil.ErrWebhookTargetBlocked, name)
	}
	assert.Zero(t, hits)
}
