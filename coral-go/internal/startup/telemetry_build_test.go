//go:build !windows

package startup

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #5206: the E2E test builds a dev coral with go build; a refused module download is retried (a proxy hiccup is not the code's fault), a compile error is not.
func TestBuildWithRetryOnlyRetriesAModuleDownload(t *testing.T) {
	proxy := []byte(`go: github.com/ulikunitz/xz@v0.5.15: read "https://proxy.golang.org/x": unexpected EOF`)
	compile := []byte("./main.go:3:1: undefined: x")
	var sleeps []time.Duration
	sleep := func(d time.Duration) { sleeps = append(sleeps, d) }
	logf := func(string, ...any) {}
	run := func(outs ...[]byte) (calls int, out []byte, err error) {
		sleeps = nil
		out, err = buildWithRetry(func() ([]byte, error) {
			o := outs[min(calls, len(outs)-1)]
			calls++
			if o == nil {
				return nil, nil
			}
			return o, errors.New("exit status 1")
		}, sleep, logf)
		return
	}

	calls, _, err := run(proxy, proxy, nil)
	assert.NoError(t, err)
	assert.Equal(t, 3, calls, "two refused downloads, then it builds")
	assert.Equal(t, []time.Duration{3 * time.Second, 6 * time.Second}, sleeps)

	calls, out, err := run(proxy)
	assert.Error(t, err)
	assert.Equal(t, 3, calls, "a download that keeps failing is tried three times and then reported")
	assert.Equal(t, proxy, out)

	calls, out, err = run(compile)
	assert.Error(t, err)
	assert.Equal(t, 1, calls, "a compile error is never retried")
	assert.Empty(t, sleeps)
	assert.Equal(t, compile, out)

	assert.True(t, downloadFailed([]byte("dial tcp 1.2.3.4:443: i/o timeout")))
	assert.False(t, downloadFailed([]byte("cannot find package")))
}
