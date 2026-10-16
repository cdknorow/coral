package tunnel

// CI-ONLY PROBE (#4011), never landed: 1000 concurrent requests through the tunnel, to see what
// fails on Windows when the local server can't accept that many connections at once.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestBurstProbe4011(t *testing.T) {
	cfg := fastCfg()
	cfg.MaxStreams = 1100
	hub := newHub(t, cfg)
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s|%s", r.URL.Path, b)
	}), func(l *LocalHandler) { l.HubLimits = HubLimits{Global: 1024} })
	var wg sync.WaitGroup
	var mu sync.Mutex
	kinds := map[string]int{}
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", fmt.Sprintf("http://x/api/n/%d", i), strings.NewReader("b"))
			resp, err := sess.RoundTrip(req)
			k := "ok"
			if err != nil {
				k = "rt: " + err.Error()
			} else {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					k = fmt.Sprintf("status %d: %.200s", resp.StatusCode, b)
				}
			}
			mu.Lock()
			kinds[k]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	t.Logf("results: %v", kinds)
	if kinds["ok"] != 1000 {
		t.Errorf("not all ok: %v", kinds)
	}
}

// The raw error a plain transport gets from a 1000-dial burst at a local server (no slots, no retry).
func TestBurstProbeRawError4011(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	cl := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 8}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	kinds := map[string]int{}
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k := "ok"
			resp, err := cl.Get(srv.URL)
			if err != nil {
				k = fmt.Sprintf("%v | refused=%v", err, connRefused(err))
			} else {
				resp.Body.Close()
			}
			mu.Lock()
			kinds[k]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	t.Logf("raw results: %v", kinds)
}
