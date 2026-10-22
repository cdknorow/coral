package hubharness

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/secrets"
)

// samples per shape, as coral-hub's harness measures (N=2000).
const defaultSamples = 2000

// samples is HUB_HARNESS_SAMPLES (a number of samples per shape, to measure a rare miss over more draws) or defaultSamples.
var samples = func() int {
	if v := os.Getenv("HUB_HARNESS_SAMPLES"); v != "" {
		n, err := strconv.Atoi(v)
		must(err)
		return n
	}
	return defaultSamples
}()

// How often does the redactor Coral Hub sees leave the random part of a secret in a board reply? One message per shape, replied as the board's
// JSON, as the hub's view-leak test posts them. Every shape must have zero misses, the ones coral-hub lists as known gaps of the library's rules
// included: the keyword rules (#5141) close them.
func TestHubRedactorMissRate(t *testing.T) {
	t.Setenv(secrets.KeywordsEnv, "")
	require.NoError(t, secrets.Init())
	rd := secrets.HubRedactor(func() bool { return true })
	miss := map[string]int{}
	first := map[string]string{}
	var names []string
	for i := 0; i < samples; i++ {
		shapes := leakShapes(&T{})
		var msgs []map[string]string
		for _, s := range shapes {
			msgs = append(msgs, map[string]string{"content": s.text})
		}
		raw, err := json.Marshal(map[string]any{"messages": msgs})
		require.NoError(t, err)
		out, err := rd.Reply(raw, true)
		require.NoError(t, err)
		body := string(out)
		for _, s := range shapes {
			if i == 0 {
				names = append(names, s.name)
			}
			if strings.Contains(body, s.secret) {
				miss[s.name]++
				if first[s.name] == "" {
					first[s.name] = s.text
				}
			}
		}
	}
	require.NotEmpty(t, names)
	for _, n := range names {
		assert.Zero(t, miss[n], "%s: %d of %d missed (seed %d: HUB_HARNESS_SEED=%d HUB_HARNESS_SAMPLES=%d reruns it); the first: %.80q", n, miss[n], samples, harnessSeed(), harnessSeed(), samples, first[n]) // assert, not require: one run lists every shape that leaks
	}
}

// With the keyword rules off the redactor is today's: the harness finds the gaps again (so the test above measures the rules, not the library).
func TestHubRedactorWithKeywordsOffHasTheKnownGaps(t *testing.T) {
	require.NoError(t, secrets.Init())
	rd := secrets.HubRedactor(func() bool { return false })
	missed := 0
	for i := 0; i < 50; i++ {
		for _, s := range leakShapes(&T{}) {
			raw, _ := json.Marshal(map[string]any{"messages": []map[string]string{{"content": s.text}}})
			out, err := rd.Reply(raw, true)
			require.NoError(t, err)
			if strings.Contains(string(out), s.secret) {
				missed++
			}
		}
	}
	require.NotZero(t, missed, "the shapes coral-hub lists as known gaps leak without the keyword rules")
}

// The samples are seeded (#5943): the same seed draws the same secrets, another seed draws others, so a miss found by a run can be run again with its seed.
func TestTheHarnessDrawsTheSameSecretsForTheSameSeed(t *testing.T) {
	draw := func(seed uint64) []string {
		var seedBytes [32]byte
		binary.LittleEndian.PutUint64(seedBytes[:], seed)
		chacha.Seed(seedBytes)
		var out []string
		for _, s := range leakShapes(&T{}) {
			out = append(out, s.text)
		}
		return out
	}
	t.Cleanup(func() { draw(harnessSeed()) })
	a, b, c := draw(7), draw(7), draw(8)
	assert.Equal(t, a, b, "the same seed, the same secrets")
	assert.NotEqual(t, a, c, "another seed, other secrets")
	assert.Equal(t, uint64(defaultSeed), func() uint64 { t.Setenv("HUB_HARNESS_SEED", ""); return harnessSeed() }(), "no seed named: the fixed default")
}
