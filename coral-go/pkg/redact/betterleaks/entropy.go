package betterleaks

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/betterleaks/betterleaks/config"
)

// RuleEntropy is one betterleaks rule that leaves a value showing when it does not look random enough, whatever its pattern matched (#5132): a
// finding is discarded when its Shannon entropy (bits per character, over the secret group) is at or below Entropy, and for a rule with
// TokenEfficiency also when its BPE tokenisation says it is common words. Coral's own keyword rules (internal/secrets) have no such gate.
type RuleEntropy struct {
	ID              string
	Entropy         float64 // 0: the rule has no entropy threshold
	TokenEfficiency bool
}

var entropyFilter = regexp.MustCompile(`entropy\(finding\["secret"\]\)\s*<=\s*([0-9.]+)`)

// EntropyGatedRules lists the embedded rules that have an entropy threshold or the token-efficiency gate, by rule id, so that what a library
// update changes is a diff the docs test shows (agent_docs/hub.md holds the table).
func EntropyGatedRules() ([]RuleEntropy, error) {
	cfg, err := config.Default()
	if err != nil {
		return nil, fmt.Errorf("betterleaks rules: %w", err)
	}
	var out []RuleEntropy
	for id, r := range cfg.Rules {
		g := RuleEntropy{ID: id, TokenEfficiency: r.TokenEfficiency}
		if m := entropyFilter.FindStringSubmatch(r.Filter); m != nil {
			g.Entropy, _ = strconv.ParseFloat(m[1], 64)
		}
		if g.Entropy > 0 || g.TokenEfficiency {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
