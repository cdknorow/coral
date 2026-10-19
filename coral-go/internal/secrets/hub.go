package secrets

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cdknorow/coral/pkg/redact"
)

// KeywordsEnv turns the keyword rules off for Coral Hub when it is off, 0, no or false (#5141): the operator's switch, next to the setting
// hub_redact_keywords. The hub cannot set either.
const KeywordsEnv = "CORAL_REDACT_KEYWORDS"

func keywordsEnvOff() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(KeywordsEnv))) {
	case "off", "0", "no", "false":
		return true
	}
	return false
}

// withKeywordRules finds what base finds and what the keyword rules find (keyword_rules.go): a value after a name such as api_key, token or
// password, whatever the value looks like. It reads the text the way withExtraRules does, with the invisible characters dropped too.
type withKeywordRules struct{ base redact.Detector }

func (d withKeywordRules) Find(ctx context.Context, text string) ([]redact.Span, error) {
	spans, err := d.base.Find(ctx, text)
	if err != nil {
		return nil, err
	}
	spans = append(spans, keywordSpans(text)...)
	spans = append(spans, moreKeywordSpans(text)...) // round four (#5212)
	if stripped, at := dropInvisibles(text); at != nil {
		for _, sp := range keywordSpans(stripped) {
			if sp.Start < 0 || sp.End > len(at) || sp.Start >= sp.End {
				continue
			}
			spans = append(spans, redact.Span{Start: at[sp.Start], End: at[sp.End-1] + 1})
		}
	}
	return spans, nil
}

// HubRedactor is the redactor for what goes to Coral Hub (the cloud tunnel's, and only that): Redactor's rules plus the keyword rules, which
// redact the value after a name such as api_key, token or password whatever its entropy (#5141). They cost redaction speed, so every other caller
// keeps Redactor. on says whether they are on now (the operator's setting, read on each call); the environment variable KeywordsEnv turns them off too.
// With them off it is Redactor itself.
func HubRedactor(on func() bool) redact.Redactor {
	once.Do(build)
	if initErr != nil {
		return redact.Refuse
	}
	return hubSwitch{on: on}
}

// HubBudgetedRedactor is HubRedactor with a budget of the caller's choosing, as BudgetedRedactor is to Redactor: a file or a message that leaves this
// computer for another Coral through Coral Hub is scanned with the hub's rules (#5172).
func HubBudgetedRedactor(budget time.Duration, on func() bool) redact.Redactor {
	once.Do(build)
	if initErr != nil {
		return redact.Refuse
	}
	return hubSwitch{on: on, budget: budget}
}

type hubSwitch struct {
	on     func() bool
	budget time.Duration // 0: the process's own redactors and their budgets
}

func (h hubSwitch) pick() redact.Redactor {
	keywords := !keywordsEnvOff() && (h.on == nil || h.on())
	if h.budget > 0 {
		if keywords {
			return &redact.Scanner{Detector: withKeywordRules{base: det}, Budget: h.budget}
		}
		return &redact.Scanner{Detector: det, Budget: h.budget}
	}
	if keywords {
		return hubRed
	}
	return red
}

func (h hubSwitch) Text(b []byte) ([]byte, error)             { return h.pick().Text(b) }
func (h hubSwitch) Reply(raw []byte, j bool) ([]byte, error)  { return h.pick().Reply(raw, j) }
func (h hubSwitch) Document(raw []byte) ([]byte, error)       { return h.pick().Document(raw) }
func (h hubSwitch) NewStream(dst io.Writer) redact.Stream     { return h.pick().NewStream(dst) }
func (h hubSwitch) NewLiveStream(dst io.Writer) redact.Stream { return h.pick().NewLiveStream(dst) }
