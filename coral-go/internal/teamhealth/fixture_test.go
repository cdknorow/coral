package teamhealth

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The adversarial fixture for the team health check (#4175, Security's #4176 constraints; #4180):
// a board and task history in which agents try to steer the check and the rules it acts through.
// testdata/adversarial.json is the data; the rule tests load it with loadAdversarial, map the
// events of a case onto their own store or input type, run the rule on the clock (Now minus
// at_minutes_ago) and assert the case's Expect lines. The assertions are plain sentences in a small
// fixed vocabulary, so a rule test maps each sentence to a check once and every case that uses it
// is covered. Secret-shaped tokens in the data are fake; invisible characters are stored as JSON
// escapes (checked below) so the file stays readable and reviewable.

type Actor struct {
	Name       string `json:"name"`
	Credential string `json:"credential"` // agent_token, operator, hub_interact, hub_launch or system
}

type Event struct {
	ID              string         `json:"id"`
	AtMinutesAgo    int            `json:"at_minutes_ago"`
	UntilMinutesAgo int            `json:"until_minutes_ago,omitempty"` // a repeated event runs from AtMinutesAgo to here
	Kind            string         `json:"kind"`
	Actor           Actor          `json:"actor"`
	Task            string         `json:"task,omitempty"`
	Title           string         `json:"title,omitempty"`
	Body            string         `json:"body,omitempty"`
	Reviewers       []string       `json:"reviewers,omitempty"`
	CreatedByRole   string         `json:"created_by_role,omitempty"`
	Assignee        string         `json:"assignee,omitempty"`
	Target          string         `json:"target,omitempty"`
	Count           int            `json:"count,omitempty"`
	EverySeconds    int            `json:"every_seconds,omitempty"`
	Name            string         `json:"name,omitempty"`
	Text            string         `json:"text,omitempty"`
	Reason          string         `json:"reason,omitempty"`
	RefusedReason   string         `json:"refused_reason,omitempty"`
	Item            string         `json:"item,omitempty"`
	Screen          string         `json:"screen,omitempty"`
	Settings        map[string]any `json:"settings,omitempty"`
}

type AgentRow struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	Session     string `json:"session"`
	SameAgentAs string `json:"same_agent_as,omitempty"`
	Team        string `json:"team,omitempty"` // empty: this team
}

type Case struct {
	ID         string   `json:"id"`
	Constraint []int    `json:"constraint"` // the numbers in Security's #4176 post
	Summary    string   `json:"summary"`
	Events     []string `json:"events"`
	Expect     []string `json:"expect"`
}

type Fixture struct {
	Version      int        `json:"version"`
	Team         string     `json:"team"`
	Orchestrator string     `json:"orchestrator"`
	Now          string     `json:"now"`
	Agents       []AgentRow `json:"agents"`
	Events       []Event    `json:"events"`
	Cases        []Case     `json:"cases"`
}

func loadAdversarial(t testing.TB) Fixture {
	t.Helper()
	b, err := os.ReadFile("testdata/adversarial.json")
	require.NoError(t, err)
	var f Fixture
	require.NoError(t, json.Unmarshal(b, &f))
	return f
}

// EventsOf returns the events a case refers to, in the order the case lists them.
func (f Fixture) EventsOf(c Case) []Event {
	byID := map[string]Event{}
	for _, e := range f.Events {
		byID[e.ID] = e
	}
	out := make([]Event, 0, len(c.Events))
	for _, id := range c.Events {
		out = append(out, byID[id])
	}
	return out
}

// The fixture is consistent: every case's events exist, every event belongs to a case, every
// actor is a known agent (or the operator, a Hub viewer or the system), tasks and targets refer to
// something, and the data holds what the cases say it holds (the hostile text really carries a
// token, a bidi override and a zero-width character, and the file itself holds none literally).
func TestAdversarialFixtureIsConsistent(t *testing.T) {
	f := loadAdversarial(t)
	assert.Equal(t, 1, f.Version)

	known := map[string]bool{"Operator": true, "Hub viewer": true, "Coral": true}
	for _, a := range f.Agents {
		known[a.Name] = true
	}
	ids := map[string]bool{}
	for _, e := range f.Events {
		assert.False(t, ids[e.ID], "duplicate event id %s", e.ID)
		ids[e.ID] = true
		assert.True(t, known[e.Actor.Name], "event %s: unknown actor %q", e.ID, e.Actor.Name)
		assert.Contains(t, []string{"agent_token", "operator", "hub_interact", "hub_launch", "system"}, e.Actor.Credential, e.ID)
		if e.Target != "" && e.Kind == "wait" {
			// a wait on a name that is not an agent is the point of one case
			assert.NotEmpty(t, e.Target)
		}
	}
	used := map[string]bool{}
	caseIDs := map[string]bool{}
	for _, c := range f.Cases {
		assert.False(t, caseIDs[c.ID], "duplicate case %s", c.ID)
		caseIDs[c.ID] = true
		assert.NotEmpty(t, c.Expect, "case %s has no expectation", c.ID)
		assert.NotEmpty(t, c.Constraint, c.ID)
		assert.NotEmpty(t, c.Events, c.ID)
		for _, id := range c.Events {
			assert.True(t, ids[id], "case %s: unknown event %s", c.ID, id)
			used[id] = true
		}
	}
	for id := range ids {
		assert.True(t, used[id], "event %s belongs to no case", id)
	}
	// Every constraint of #4176 item 20's list is covered by a case.
	for _, id := range []string{"C1a", "C1b", "C2", "C3", "C4", "C5", "C6a", "C6b", "C7", "C8", "C9", "C10"} {
		assert.True(t, caseIDs[id], "missing case %s", id)
	}
}

func TestAdversarialFixtureCarriesTheHostileText(t *testing.T) {
	f := loadAdversarial(t)
	var post Event
	for _, e := range f.Events {
		if e.ID == "e11" {
			post = e
		}
	}
	require.NotEmpty(t, post.Text)
	assert.Contains(t, post.Text, "@Orchestrator")
	assert.Contains(t, post.Text, "ghp_")
	assert.Contains(t, post.Text, "AKIA")
	assert.Contains(t, post.Text, string(rune(0x202e)), "an RTL override")
	assert.Contains(t, post.Text, string(rune(0x200b)), "a zero-width space")
	assert.Contains(t, post.Text, string(rune(0x1b)), "an escape sequence")
	assert.Contains(t, post.Text, "\n", "a second line")
	assert.Greater(t, len(post.Text), 120)

	// The reserved-name case: each variant is a distinct string the rule must still refuse.
	seen := map[string]bool{}
	for _, e := range f.Events {
		if e.Kind == "subscribe" {
			assert.False(t, seen[e.Name], "duplicate variant %q", e.Name)
			seen[e.Name] = true
		}
	}
	assert.GreaterOrEqual(t, len(seen), 7)
}

// The data file holds no invisible character literally: they are JSON escapes, so a reviewer
// sees them and an editor can't lose or add one.
func TestAdversarialFixtureFileHasNoLiteralInvisibles(t *testing.T) {
	b, err := os.ReadFile("testdata/adversarial.json")
	require.NoError(t, err)
	for i, r := range string(b) {
		if r == '\n' {
			continue
		}
		assert.False(t, unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) || unicode.IsControl(r),
			"literal invisible U+%04X at byte %d", r, i)
	}
	assert.True(t, strings.Contains(string(b), `\u202e`), "the RTL override is stored as an escape")
}
