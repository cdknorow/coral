// Package secrets is Coral's secret redaction (#3808, the operator's decision): betterleaks'
// rules, and only them, run through pkg/redact's Scanner, which frames streams, walks JSON and
// fails closed. Coral Hub's replies and streams, agent events at rest and every coral.log line
// go through it, with a few rules of Coral's own beside the library's (extra.go, #4265).
// pkg/redact's own rules (redact.Rules) are not used.
package secrets

import (
	"sync"
	"time"

	"github.com/cdknorow/coral/pkg/redact"
	"github.com/cdknorow/coral/pkg/redact/betterleaks"
)

// Budget bounds the detection for one reply, document, log line or stream unit. Text dense with
// the rules' keywords checks at a few hundred KB a second (#3807); over budget it is not passed
// on (Coral Hub answers "could not be checked").
const Budget = 3 * time.Second

// LogBudget bounds the check of one coral.log line: the writer runs under the logger's lock,
// so a slow line would hold every other line back (Security on #3808). Over it, the line is
// replaced by a note.
const LogBudget = 250 * time.Millisecond

// RulesVersion names the rule source, for the release notes and logs.
const RulesVersion = "betterleaks " + betterleaks.Version

var (
	once    sync.Once
	red     redact.Redactor
	logRed  redact.Redactor
	hubRed  redact.Redactor // the connector's redactor for Coral Hub: red plus the keyword rules (HubRedactor)
	det     redact.Detector
	initErr error
)

func build() {
	d, err := betterleaks.New()
	if err != nil {
		initErr, red, logRed, hubRed = err, redact.Refuse, redact.Refuse, redact.Refuse
		return
	}
	det = withExtraRules{base: d}
	red = &redact.Scanner{Detector: det, Budget: Budget}
	logRed = &redact.Scanner{Detector: det, Budget: LogBudget}
	hubRed = &redact.Scanner{Detector: withKeywordRules{base: det}, Budget: Budget}
}

// Init builds the detector (it compiles every rule): call it once at startup, before anything
// is redacted. An error means Coral can't check for secrets and must not start.
func Init() error {
	once.Do(build)
	return initErr
}

// Redactor is the process's redactor; the first call builds it if Init hasn't. If it couldn't be
// built, every call refuses (nothing is passed on unchecked).
func Redactor() redact.Redactor {
	once.Do(build)
	return red
}

// LogRedactor is the redactor for coral.log lines: the same rules, within LogBudget.
func LogRedactor() redact.Redactor {
	once.Do(build)
	return logRed
}
