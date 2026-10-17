package routes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3547: one PUT /api/settings is written as a whole. Two clients each set a pair of keys
// to their own value, many times at once; afterwards both keys come from the same request.
// Written key by key, a request landing between two of another's keys left them mixed (the
// editions_browser flake: welcome_choice from one save, welcome_pending from another).
func TestPutSettingsIsAtomic(t *testing.T) {
	srv, _ := setupSystemTestServer(t)
	// Four free-text settings (the registry refuses keys it doesn't know, #4432).
	keys := []string{"default_prompt_orchestrator", "default_prompt_worker", "team_reminder_orchestrator", "team_reminder_worker"}
	put := func(v string) {
		body, _ := json.Marshal(map[string]string{keys[0]: v, keys[1]: v, keys[2]: v, keys[3]: v})
		req, _ := http.NewRequest("PUT", srv.URL+"/api/settings", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			if resp.StatusCode != http.StatusOK {
				t.Errorf("PUT answered %d", resp.StatusCode)
			}
			resp.Body.Close()
		}
	}
	for round := 0; round < 40; round++ {
		var wg sync.WaitGroup
		for c := 0; c < 4; c++ {
			wg.Add(1)
			go func(c int) {
				defer wg.Done()
				put(fmt.Sprintf("client-%d-round-%d", c, round))
			}(c)
		}
		wg.Wait()
		resp, err := http.Get(srv.URL + "/api/settings")
		require.NoError(t, err)
		var out struct {
			Settings map[string]any `json:"settings"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		resp.Body.Close()
		s := out.Settings
		require.NotEmpty(t, s[keys[0]], "round %d: the keys were saved", round)
		assert.True(t, s[keys[0]] == s[keys[1]] && s[keys[1]] == s[keys[2]] && s[keys[2]] == s[keys[3]],
			"round %d: keys from different requests: %v %v %v %v", round, s[keys[0]], s[keys[1]], s[keys[2]], s[keys[3]])
	}
}
