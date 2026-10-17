package main

import (
	"testing"
	"time"
)

func TestParseWhen(t *testing.T) {
	loc := time.FixedZone("PDT", -7*3600)
	now := time.Date(2026, 10, 17, 4, 30, 0, 0, loc)
	for in, want := range map[string]time.Time{
		"2026-10-19T21:58:00-07:00": time.Date(2026, 10, 19, 21, 58, 0, 0, loc),
		"2026-10-19 21:58":          time.Date(2026, 10, 19, 21, 58, 0, 0, loc),
		"2026-10-19":                time.Date(2026, 10, 19, 0, 0, 0, 0, loc),
		"21:58":                     time.Date(2026, 10, 17, 21, 58, 0, 0, loc),
		"04:00":                     time.Date(2026, 10, 18, 4, 0, 0, 0, loc), // past today: tomorrow
		"30m":                       now.Add(30 * time.Minute),
		"+2h":                       now.Add(2 * time.Hour),
		"3d":                        now.AddDate(0, 0, 3),
	} {
		got, err := parseWhen(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseWhen(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "soon", "-2h", "0d", "25:00", "2026-13-01"} {
		if _, err := parseWhen(bad, now); err == nil {
			t.Errorf("parseWhen(%q) accepted", bad)
		}
	}
}
