package routes

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// The Coral Hub request pools' sizes (#3642): how many requests Coral Hub can hold open on
// this computer at once, in all, on long-lived routes (long polls, terminals), and on the
// search routes. Empty means the default (64, 64, 4). One Hub user can use a quarter of the
// general pool, half the long pool (at least 16: a dashboard tab and its terminals, #3663) and
// half the search pool. Set with PUT /api/settings, which the Hub can't reach, so the Hub can't
// raise its own limits.
const (
	HubLimitGlobalSetting = "hub_limit_global"
	HubLimitLongSetting   = "hub_limit_long"
	HubLimitSearchSetting = "hub_limit_search"
)

var hubLimitRanges = []struct {
	key      string
	min, max int
}{
	{HubLimitGlobalSetting, 8, 1024},
	{HubLimitLongSetting, 2, 256},
	{HubLimitSearchSetting, 1, 64},
}

// validateHubLimitSettings checks and normalises the pool sizes in a settings update: a whole
// number in range, or "" / null for the default.
func validateHubLimitSettings(body map[string]any) error {
	for _, r := range hubLimitRanges {
		v, ok := body[r.key]
		if !ok {
			continue
		}
		var n int64
		switch x := v.(type) {
		case nil:
			body[r.key] = ""
			continue
		case float64:
			if x != float64(int64(x)) {
				return hubLimitErr(r.key, r.min, r.max)
			}
			n = int64(x)
		case string:
			if strings.TrimSpace(x) == "" {
				body[r.key] = ""
				continue
			}
			p, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
			if err != nil {
				return hubLimitErr(r.key, r.min, r.max)
			}
			n = p
		default:
			return hubLimitErr(r.key, r.min, r.max)
		}
		if n < int64(r.min) || n > int64(r.max) {
			return hubLimitErr(r.key, r.min, r.max)
		}
		body[r.key] = strconv.FormatInt(n, 10)
	}
	return nil
}

func hubLimitErr(key string, min, max int) error {
	return fmt.Errorf("%s must be a whole number from %d to %d (empty for the default)", key, min, max)
}

// HubLimitsFromSettings gives the pool sizes for each Coral Hub request, from the settings at
// most a second old (a change applies within a second). A missing, unreadable or out-of-range
// value is the default.
func HubLimitsFromSettings(ss *store.SessionStore) func() tunnel.HubLimits {
	var mu sync.Mutex
	var at time.Time
	var cur tunnel.HubLimits
	return func() tunnel.HubLimits {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(at) < time.Second {
			return cur
		}
		s, err := ss.GetSettings(context.Background())
		cur, at = tunnel.HubLimits{}, time.Now()
		if err != nil {
			return cur
		}
		read := func(key string) int {
			for _, r := range hubLimitRanges {
				if r.key == key {
					if n, err := strconv.Atoi(s[key]); err == nil && n >= r.min && n <= r.max {
						return n
					}
				}
			}
			return 0
		}
		cur.Global, cur.Long, cur.Search = read(HubLimitGlobalSetting), read(HubLimitLongSetting), read(HubLimitSearchSetting)
		return cur
	}
}
