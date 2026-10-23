package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #6538 (UI/UX's finding in #6521): next_fire_at was the job's clock time in its own zone labelled "+00:00", so the dashboard showed the wrong next run for any job outside UTC.

func scheduleRig(t *testing.T) (*ScheduleHandler, *store.ScheduleStore, chi.Router) {
	t.Helper()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "sched.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewScheduleHandler(db, &config.Config{})
	r := chi.NewRouter()
	r.Get("/api/scheduled/jobs", h.ListJobs)
	r.Post("/api/scheduled/validate-cron", h.ValidateCron)
	return h, store.NewScheduleStore(db), r
}

// nextIn is the next fire of cron expr in zone, as the parser itself computes it from now.
func nextIn(t *testing.T, expr, zone string, now time.Time) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	require.NoError(t, err)
	s, err := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(expr)
	require.NoError(t, err)
	return s.Next(now.In(loc))
}

func TestNextFireAtCarriesTheJobsRealOffset(t *testing.T) {
	_, ss, r := scheduleRig(t)
	ctx := context.Background()
	type job struct{ name, zone, expr string }
	jobs := []job{{"la", "America/Los_Angeles", "0 2 * * *"}, {"tokyo", "Asia/Tokyo", "30 9 * * *"}, {"utc", "UTC", "0 2 * * *"}, {"none", "", "0 2 * * *"}, {"kolkata", "Asia/Kolkata", "15 6 * * *"}}
	for _, j := range jobs {
		_, err := ss.CreateScheduledJob(ctx, &store.ScheduledJob{Name: j.name, CronExpr: j.expr, Timezone: j.zone, JobType: "shell", Enabled: 1})
		require.NoError(t, err)
	}
	before := time.Now()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/scheduled/jobs", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	after := time.Now()
	var out struct {
		Jobs []struct {
			Name       string  `json:"name"`
			NextFireAt *string `json:"next_fire_at"`
		} `json:"jobs"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Jobs, len(jobs))
	byName := map[string]string{}
	for _, j := range out.Jobs {
		require.NotNil(t, j.NextFireAt, j.Name)
		byName[j.Name] = *j.NextFireAt
	}
	for _, j := range jobs {
		zone := j.zone
		if zone == "" {
			zone = "UTC"
		}
		got, err := time.Parse(time.RFC3339, byName[j.name])
		require.NoError(t, err, "%s: %q is RFC 3339", j.name, byName[j.name])
		want1, want2 := nextIn(t, j.expr, zone, before), nextIn(t, j.expr, zone, after)
		assert.True(t, got.Equal(want1) || got.Equal(want2), "%s: %q is the instant %s, the real next fire is %s", j.name, byName[j.name], got.UTC(), want1.UTC())
		// the offset written is the zone's offset at that instant
		loc, _ := time.LoadLocation(zone)
		_, off := got.In(loc).Zone()
		assert.Equal(t, got.In(loc).Format("-07:00"), byName[j.name][len(byName[j.name])-6:], "%s: the offset is the zone's (%d s)", j.name, off)
	}
	assert.True(t, strings.HasSuffix(byName["utc"], "+00:00") && strings.HasSuffix(byName["none"], "+00:00"), "a UTC job reads as before: %q %q", byName["utc"], byName["none"])
	assert.True(t, strings.HasSuffix(byName["tokyo"], "+09:00"), byName["tokyo"])
	assert.True(t, strings.HasSuffix(byName["kolkata"], "+05:30"), "a half-hour zone: %q", byName["kolkata"])
	assert.True(t, strings.HasSuffix(byName["la"], "-07:00") || strings.HasSuffix(byName["la"], "-08:00"), "Los Angeles is -07:00 or -08:00: %q", byName["la"])
}

func TestValidateCronNextFireTimesCarryTheRealOffsetToo(t *testing.T) {
	_, _, r := scheduleRig(t)
	for zone, suffix := range map[string]string{"Asia/Tokyo": "+09:00", "Asia/Kolkata": "+05:30", "UTC": "+00:00"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/scheduled/validate-cron", strings.NewReader(`{"cron_expr":"0 2 * * *","timezone":"`+zone+`"}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out struct {
			Times []string `json:"next_fire_times"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		require.Len(t, out.Times, 5)
		prev := time.Time{}
		for _, ts := range out.Times {
			got, err := time.Parse(time.RFC3339, ts)
			require.NoError(t, err, ts)
			assert.True(t, strings.HasSuffix(ts, suffix), "%s: %q", zone, ts)
			assert.Equal(t, 2, got.Hour(), "%s: 02:00 on the job's own clock: %q", zone, ts)
			assert.True(t, got.After(prev), "in order")
			prev = got
		}
	}
}
