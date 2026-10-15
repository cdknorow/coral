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
	put := func(v string) {
		body, _ := json.Marshal(map[string]string{"atomic_a": v, "atomic_b": v, "atomic_c": v, "atomic_d": v})
		req, _ := http.NewRequest("PUT", srv.URL+"/api/settings", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
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
		assert.True(t, s["atomic_a"] == s["atomic_b"] && s["atomic_b"] == s["atomic_c"] && s["atomic_c"] == s["atomic_d"],
			"round %d: keys from different requests: %v %v %v %v", round, s["atomic_a"], s["atomic_b"], s["atomic_c"], s["atomic_d"])
	}
}
