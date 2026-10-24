package startup

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

type fakeEventReader struct {
	types      map[string][2]string
	typesErr   error
	failed     bool
	failedErr  error
	failedAsks int
}

func (f *fakeEventReader) GetLatestEventTypes(context.Context, []string) (map[string][2]string, error) {
	return f.types, f.typesErr
}

func (f *fakeEventReader) LatestStopFailure(context.Context, string) (string, bool, error) {
	f.failedAsks++
	return "", f.failed, f.failedErr
}

// What a run's agent last did, for a run that ended while Coral was down (#6898 LOW 3, #6928): an unknown result is never a finish, so a read that errors counts as a failed stop.
func TestLastEventOfRunNeverReadsAnUnknownAsAFinish(t *testing.T) {
	stop := map[string][2]string{"s": {"stop", ""}}
	for _, c := range []struct {
		name       string
		r          *fakeEventReader
		wantType   string
		wantFailed bool
		asks       int
	}{
		{"a normal stop", &fakeEventReader{types: stop}, "stop", false, 1},
		{"a failed stop", &fakeEventReader{types: stop, failed: true}, "stop", true, 1},
		{"the failed-stop read errors: unknown counts as failed", &fakeEventReader{types: stop, failedErr: errors.New("db busy")}, "stop", true, 1},
		{"another event is not asked about a failed stop", &fakeEventReader{types: map[string][2]string{"s": {"tool_use", ""}}}, "tool_use", false, 0},
		{"the last-event read errors: nothing is known", &fakeEventReader{typesErr: errors.New("db busy")}, "", true, 0},
		{"no event", &fakeEventReader{types: map[string][2]string{}}, "", false, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			typ, failed := lastEventOfRun(context.Background(), c.r, "s")
			assert.Equal(t, c.wantType, typ)
			assert.Equal(t, c.wantFailed, failed)
			assert.Equal(t, c.asks, c.r.failedAsks)
		})
	}
}
