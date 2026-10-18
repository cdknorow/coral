package routes

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #3999 (the #3994 audit, finding 3): the once-per-interval log memory and the by-tool counts
// stay bounded when a caller varies the value they are keyed by.
func TestIdentityLogMemoryIsBounded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	o := NewIdentityObserver(nil, nil, func(*http.Request) bool { return false })
	o.now = func() time.Time { return now }
	for i := 0; i < 8*identityLogKeysMax; i++ { // eight times the cap in one interval (each insert at the cap prunes the map: 100,000 took seconds, #4852)
		o.logOnce(fmt.Sprintf("k%d", i), "test line")
	}
	assert.LessOrEqual(t, len(o.lastLog), identityLogKeysMax, "eight times the cap in distinct keys in one interval")
	// Once the interval has passed, the old keys are forgotten and new ones are logged again.
	now = now.Add(identityLogInterval + time.Second)
	o.logOnce("fresh", "test line")
	assert.Contains(t, o.lastLog, "fresh")
	assert.LessOrEqual(t, len(o.lastLog), identityLogKeysMax)
}

func TestIdentityByToolCountIsBounded(t *testing.T) {
	o := NewIdentityObserver(nil, nil, func(*http.Request) bool { return false })
	for i := 0; i < 5000; i++ {
		o.attribute("session", fmt.Sprintf("tool-%d/1.0", i))
	}
	assert.LessOrEqual(t, len(o.byTool), identityBreakKeysMax+1)
	assert.Positive(t, o.byTool["other"], "later tools are counted under other")
	o.attribute("session", string(make([]byte, 500))+"/1")
	for k := range o.byTool {
		assert.LessOrEqual(t, len(k), 40)
	}
}
