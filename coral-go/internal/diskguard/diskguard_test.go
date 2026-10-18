package diskguard

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestLowIsUnderOneGiBOrOnePercent(t *testing.T) {
	now := time.Now()
	g := &Guard{dir: "d", now: func() time.Time { return now }}
	for _, c := range []struct {
		free, total uint64
		low         bool
	}{
		{500 << 20, 100 << 30, true},    // under 1 GiB
		{2 << 30, 100 << 30, false},     // 2 GiB of 100 GiB: 2%
		{2 << 30, 500 << 30, true},      // 2 GiB of 500 GiB: under 1% (5 GiB)
		{6 << 30, 500 << 30, false},     // over 1%
		{1 << 30, 10 << 30, false},      // exactly the minimum is not under it
		{(1 << 30) - 1, 10 << 30, true}, // one byte under
	} {
		c := c
		g.usage = func(string) (uint64, uint64, error) { return c.free, c.total, nil }
		g.known = false
		low, free := g.Low()
		assert.Equal(t, c.low, low, "free %d of %d", c.free, c.total)
		assert.Equal(t, c.free, free)
	}
}

func TestAReadingIsReusedForTenSecondsAndAFailureIsNotLow(t *testing.T) {
	now := time.Now()
	calls := 0
	g := &Guard{dir: "d", now: func() time.Time { return now }}
	g.usage = func(string) (uint64, uint64, error) { calls++; return 100 << 20, 100 << 30, nil }
	low, _ := g.Low()
	assert.True(t, low)
	now = now.Add(9 * time.Second)
	g.Low()
	assert.Equal(t, 1, calls, "reused inside ten seconds")
	now = now.Add(2 * time.Second)
	g.Low()
	assert.Equal(t, 2, calls, "read again after")
	g.usage = func(string) (uint64, uint64, error) { return 0, 0, errors.New("no volume") }
	now = now.Add(11 * time.Second)
	low, _ = g.Low()
	assert.False(t, low, "a volume that can't be read is not low")
}

func TestNothingIsLowUntilUsed(t *testing.T) {
	Use("")
	low, _ := Low()
	assert.False(t, low)
}

func TestRealVolumeReads(t *testing.T) {
	free, total, err := realUsage(t.TempDir())
	assert.NoError(t, err)
	assert.Greater(t, total, uint64(0))
	assert.LessOrEqual(t, free, total)
}
