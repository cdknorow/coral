package ptymanager

import (
	"strconv"
	"sync/atomic"
)

var uniqueSeq atomic.Int64

// uniqueName is base with a number no other run of any test in this process has used. The gate keeps per-session memory in package-level state
// (surveyTried, the held-send log limiter), so a test that is run again in the same process (go test -count=N, which the flake repeats use) must
// not reuse a session name: its second run found the survey "already tried" and the log line "already said" (#5148).
func uniqueName(base string) string { return base + "-" + strconv.FormatInt(uniqueSeq.Add(1), 10) }
