// Package betterleaks runs betterleaks' rules (github.com/betterleaks/betterleaks, MIT) as a
// redact.Detector: its embedded default rule set, with validation off, so no rule makes a
// network request, reads a file or the environment (Security's review, #3815).
package betterleaks

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/betterleaks/betterleaks/config"
	"github.com/betterleaks/betterleaks/detect"
	"github.com/betterleaks/betterleaks/logging"
	"github.com/betterleaks/betterleaks/report"
	"github.com/betterleaks/betterleaks/sources"
	"github.com/rs/zerolog"

	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/pkg/redact"
)

// Version is the betterleaks release whose rules this package runs: the version go.mod pins.
// A rule change comes with a new version, so it shows in the redaction notes.
const Version = "v1.9.0"

// The embedded global filter discards a finding whose secret matches `(?i)^true|false|null$`, which reads as "the secret is the word true, false
// or null" but, with the | binding looser than ^ and $, means: starts with "true", OR contains "false" anywhere, OR ends with "null". So a
// secret holding "false" in any case anywhere (a random 1,600-character private key body does about one time in 3,600), or starting with "true" or
// ending with "null", was never redacted, whatever rule found it (#5179). fixGlobalFilter anchors the alternation as intended.
const (
	unanchoredBoolWords = "`(?i)^true|false|null$`"
	anchoredBoolWords   = "`(?i)^(?:true|false|null)$`"
)

// The same filter drops a finding whose secret CONTAINS the lowercase alphabet (or a known example UUID): containsAny where the intent was "the secret is the placeholder", so a real
// token that holds abcdefghijklmnopqrstuvwxyz anywhere in it (a key someone typed, a generated one with that run) was never redacted, whatever rule found it (#5190).
// The placeholders themselves are still dropped, as whole values.
const (
	containsPlaceholders = "containsAny(finding[\"secret\"], [\n  \"abcdefghijklmnopqrstuvwxyz\",\n  \"014df517-39d1-4453-b7b3-9930c563627c\"\n])"
	equalsPlaceholders   = "matchesAny(finding[\"secret\"], [\n  `^abcdefghijklmnopqrstuvwxyz$`,\n  `^014df517-39d1-4453-b7b3-9930c563627c$`\n])"
)

// fixGlobalFilter repairs cfg's global filter: the unanchored alternation (#5179) and the alphabet suppression (#5190). A library whose filter no longer has a fault (fixed upstream) is left as it
// is; one that still has it after the replacement is refused, so the fault cannot come back unseen.
func fixGlobalFilter(cfg *config.Config) error {
	cfg.Filter = strings.Replace(cfg.Filter, unanchoredBoolWords, anchoredBoolWords, 1)
	if strings.Contains(cfg.Filter, "^true|false|null$") {
		return errors.New("betterleaks rules: the global filter still discards any secret that contains false (#5179)")
	}
	cfg.Filter = strings.Replace(cfg.Filter, containsPlaceholders, equalsPlaceholders, 1)
	if strings.Contains(cfg.Filter, "\"abcdefghijklmnopqrstuvwxyz\"") {
		return errors.New("betterleaks rules: the global filter still discards any secret that contains the lowercase alphabet (#5190)")
	}
	return nil
}

// Detector finds what betterleaks' rules find. One is safe for any number of goroutines.
type Detector struct {
	d *detect.Detector
	// slots bounds the scans running at once. A slot is given back when betterleaks returns,
	// not when the caller gives up: a scan abandoned at its deadline still runs to its next
	// rule boundary, so callers past the bound wait (within their deadline) instead of piling
	// more scans on the machine (Security on #3808).
	slots chan struct{}
}

// Slots is how many scans a Detector runs at once: twice the CPUs Go uses.
func Slots() int { return 2 * runtime.GOMAXPROCS(0) }

var _ redact.Detector = (*Detector)(nil)

var quiet sync.Once

// New builds the detector from betterleaks' embedded rules (never a file). Build it once:
// building compiles every rule. betterleaks exits the process on a rule that doesn't compile;
// with the embedded, pinned rules that can't happen at run time, and TestNewBuildsTheDetector
// builds it in every test run.
func New() (*Detector, error) {
	cfg, err := config.Default()
	if err != nil {
		return nil, fmt.Errorf("betterleaks rules: %w", err)
	}
	if err := fixGlobalFilter(cfg); err != nil {
		return nil, err
	}
	// Its global logger writes to stderr and, at debug level, logs each finding with its secret.
	quiet.Do(func() { logging.Logger = zerolog.Nop() })
	d := detect.NewDetectorContext(context.Background(), cfg, detect.ValidationOptions{})
	if d == nil {
		return nil, errors.New("betterleaks: no detector")
	}
	return &Detector{d: d, slots: make(chan struct{}, Slots())}, nil
}

// Find returns the spans of text that hold what betterleaks' rules find: each finding's secret
// within its match (or the whole match, for one found in decoded text), and every other place
// the same secret appears. It returns when ctx ends, with ctx's error: betterleaks checks its
// context between rules only, and a stopped scan is never taken as a full one. With every slot
// taken it waits for one until ctx ends. The Scanner gives it at most a chunk at a time, which
// bounds what an abandoned scan still costs.
func (d *Detector) Find(ctx context.Context, text string) ([]redact.Span, error) {
	select {
	case d.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("betterleaks: every scan slot is busy: %w", ctx.Err())
	}
	type result struct {
		findings []report.Finding
		err      error
	}
	done := make(chan result, 1)
	go func() {
		defer safego.Recover("redact/betterleaks/betterleaks:80")
		defer func() { <-d.slots }()
		defer func() {
			if p := recover(); p != nil {
				done <- result{err: fmt.Errorf("betterleaks panicked: %v", p)}
			}
		}()
		f := d.d.DetectContext(ctx, sources.Fragment{Raw: text}) //nolint:staticcheck // the one call that takes a context
		done <- result{findings: f}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return spans(text, r.findings), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// RuleIDs names the betterleaks rules that find something in text (each once, in the order found): what a refusal says it was stopped by (#5212). It never returns a secret, only rule names.
func (d *Detector) RuleIDs(ctx context.Context, text string) ([]string, error) {
	select {
	case d.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("betterleaks: every scan slot is busy: %w", ctx.Err())
	}
	defer func() { <-d.slots }()
	var ids []string
	seen := map[string]bool{}
	for _, f := range d.d.DetectContext(ctx, sources.Fragment{Raw: text}) { //nolint:staticcheck // the one call that takes a context
		if !seen[f.RuleID] {
			seen[f.RuleID] = true
			ids = append(ids, f.RuleID)
		}
	}
	return ids, ctx.Err()
}

// spans maps findings to byte ranges of text.
func spans(text string, findings []report.Finding) []redact.Span {
	if len(findings) == 0 {
		return nil
	}
	lines := lineOffsets(text)
	var out []redact.Span
	for _, f := range findings {
		start, end, ok := matchRange(text, lines, f)
		switch {
		case ok && f.Secret != "" && strings.Contains(text[start:end], f.Secret):
			at := start + strings.Index(text[start:end], f.Secret)
			out = append(out, redact.Span{Start: at, End: at + len(f.Secret)})
		case ok:
			out = append(out, redact.Span{Start: start, End: end})
		default:
			// A location that doesn't fit the text: the finding's lines go whole.
			out = append(out, lineRange(text, lines, f))
		}
		// The same secret anywhere else in the text (a value echoed later).
		if s := f.Secret; len(s) >= 8 {
			for i := 0; ; {
				k := strings.Index(text[i:], s)
				if k < 0 {
					break
				}
				out = append(out, redact.Span{Start: i + k, End: i + k + len(s)})
				i += k + len(s)
			}
		}
	}
	return out
}

// lineOffsets is where each line of s starts, as betterleaks counts them.
func lineOffsets(s string) []int {
	offsets := []int{0}
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' && i+1 < len(s) {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

// matchRange is a finding's match as a byte range of text: its lines are 1-based, its start
// column 1-based and its end column the match's last byte on its line, 1-based.
func matchRange(text string, lines []int, f report.Finding) (start, end int, ok bool) {
	if f.StartLine < 1 || f.EndLine < f.StartLine || f.EndLine > len(lines) {
		return 0, 0, false
	}
	start = lines[f.StartLine-1] + f.StartColumn - 1
	end = lines[f.EndLine-1] + f.EndColumn
	if start < 0 || end > len(text) || start >= end {
		return 0, 0, false
	}
	return start, end, true
}

// lineRange is the whole of the lines a finding names, clamped to text.
func lineRange(text string, lines []int, f report.Finding) redact.Span {
	first := min(max(f.StartLine, 1), len(lines))
	last := min(max(f.EndLine, first), len(lines))
	end := len(text)
	if last < len(lines) {
		end = lines[last]
	}
	return redact.Span{Start: lines[first-1], End: end}
}
