package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseWhen reads a time the way people write one on the command line (#4175 step 3: task park
// --until, task add --remind-at), in local time:
//
//	"2026-10-19T21:58:00-07:00"  RFC 3339
//	"2026-10-19 21:58"           a date and a time
//	"2026-10-19"                 the start of that day
//	"21:58"                      the next time the clock shows it (today, or tomorrow if past)
//	"30m", "+2h", "3d"           that long from now
func parseWhen(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("an empty time")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	loc := now.Location()
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, loc); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("15:04", s, loc); err == nil {
		at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		if !at.After(now) {
			at = at.AddDate(0, 0, 1)
		}
		return at, nil
	}
	d := strings.TrimPrefix(s, "+")
	if n, err := strconv.Atoi(strings.TrimSuffix(d, "d")); err == nil && strings.HasSuffix(d, "d") && n > 0 {
		return now.AddDate(0, 0, n), nil
	}
	if dur, err := time.ParseDuration(d); err == nil && dur > 0 {
		return now.Add(dur), nil
	}
	return time.Time{}, fmt.Errorf("%q is not a time: use 2026-10-19 21:58, 21:58, 2026-10-19, 30m, 2h or 3d", s)
}
