package teamhealth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The orchestrator's check (#4175 step 2): only what needs its judgment, ranked, each with its
// evidence and a suggested next action; then what waits on the operator; then one line on what
// moved. With nothing to judge it is one line, and it is still sent (the operator's direction:
// never silence it). It goes to its recipient alone, through the typing gate, never to the board,
// and is at most MaxCheck bytes (Security's #4176 rule 3).

// MaxCheck is the check's size cap in bytes.
const MaxCheck = 4096

// Item is one thing for the orchestrator. Its fields hold member names (Name), task numbers,
// durations and fixed phrases only; agent-written text goes in Said, already through ShownText.
type Item struct {
	Rank     int    // lower first
	Agent    string // a member name, through Name
	Problem  string // a fixed phrase with numbers ("silent on #12 for 34 min since Coral asked for a status")
	Said     string // the agent's last words, quoted (ShownText), or ""
	Evidence string // a fixed phrase with numbers, or ""
	Suggest  string // a fixed phrase
	// Key names the item stably across checks (rule, member, task) and State what it says now;
	// the orchestrator acknowledges it by ItemID(Key) and it stays off the list until State
	// changes (#4175 step 3). An item without a Key can't be acknowledged.
	Key   string
	State string
	// Back: the orchestrator acknowledged it and its state has changed since; said once.
	Back bool
}

// ItemID is the short id the check prints for an item ("coral-board health ack <id>").
func ItemID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}

// MaxQuestions is how many questions waiting on the operator the check lists, oldest first;
// the rest are counted.
const MaxQuestions = 5

// Question is a question waiting on the operator.
type Question struct {
	Asker string // a member name
	Text  string // through ShownText
	Since time.Time
}

// Progress is what moved since the last check.
type Progress struct {
	Since                      time.Time
	Completed, Reviewed, Posts int
}

// CheckInput is everything the check says.
type CheckInput struct {
	Now       time.Time
	Agents    int
	Items     []Item
	Questions []Question
	Progress  Progress
	// Parked tasks raise no item; the check counts them so it is plain that something is set
	// aside, and names, once, a task whose park ended by itself (#4175 step 3, Security's SHOULD).
	Parked       int
	BackFromPark []int64
}

// parkedNote is the team line's note on parked tasks, or "".
func parkedNote(in CheckInput) string {
	var parts []string
	if in.Parked > 0 {
		parts = append(parts, fmt.Sprintf("%d parked", in.Parked))
	}
	if len(in.BackFromPark) > 0 {
		var ids []string
		for _, id := range in.BackFromPark {
			ids = append(ids, fmt.Sprintf("#%d", id))
		}
		parts = append(parts, "back from park: "+strings.Join(ids, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}

// Compose writes the check.
func Compose(in CheckInput) string {
	since := in.Progress.Since.Format("15:04")
	team := fmt.Sprintf("Since %s: %s.", since, progressPhrase(in.Progress))
	if in.Progress.Completed+in.Progress.Reviewed+in.Progress.Posts == 0 {
		team = fmt.Sprintf("No progress since the last check (%s).", since)
	}
	note := parkedNote(in)
	team = strings.TrimSuffix(team, ".") + note + "."
	if len(in.Items) == 0 && len(in.Questions) == 0 {
		if in.Progress.Completed+in.Progress.Reviewed+in.Progress.Posts == 0 {
			return fmt.Sprintf("[Coral health check] Nothing needs you; no progress since the last check (%s)%s.", since, note)
		}
		return fmt.Sprintf("[Coral health check] All %d agents progressing; %s since %s%s.", in.Agents, progressPhrase(in.Progress), since, note)
	}
	items := append([]Item(nil), in.Items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Rank < items[j].Rank })
	var itemLines []string
	for i, it := range items {
		l := fmt.Sprintf("%d) %s: %s.", i+1, it.Agent, it.Problem)
		if it.Evidence != "" {
			l += " " + it.Evidence + "."
		}
		if it.Said != "" {
			l += " Last said, " + it.Said
		}
		if it.Suggest != "" {
			l += " Suggest: " + it.Suggest + "."
		}
		if it.Back {
			l += " (back: changed since it was acknowledged)"
		}
		if it.Key != "" {
			l += " [ack " + ItemID(it.Key) + "]"
		}
		itemLines = append(itemLines, l)
	}
	qLine := ""
	if len(in.Questions) > 0 {
		var qs []string
		for i, q := range in.Questions {
			if i == MaxQuestions {
				qs = append(qs, fmt.Sprintf("(%d more)", len(in.Questions)-MaxQuestions))
				break
			}
			qs = append(qs, fmt.Sprintf("%s asked %s ago: %s", q.Asker, age(in.Now.Sub(q.Since)), q.Text))
		}
		qLine = "Waiting on the operator: " + strings.Join(qs, "; ") + "."
	}
	head := fmt.Sprintf("[Coral health check] %d need you:", len(items))
	if len(items) == 0 {
		head = "[Coral health check]"
	}
	// Within the cap: the lowest-ranked items are left out first, and counted.
	for shown := len(itemLines); ; shown-- {
		parts := append([]string{head}, itemLines[:shown]...)
		if shown < len(itemLines) {
			parts = append(parts, fmt.Sprintf("(%d more not shown.)", len(itemLines)-shown))
		}
		if qLine != "" {
			parts = append(parts, qLine)
		}
		out := strings.Join(append(parts, team), " ")
		if len(out) <= MaxCheck || shown == 0 {
			if len(out) > MaxCheck {
				out = strings.ToValidUTF8(out[:MaxCheck], "")
			}
			return out
		}
	}
}

func progressPhrase(p Progress) string {
	var parts []string
	if p.Completed > 0 {
		parts = append(parts, plural(p.Completed, "task completed", "tasks completed"))
	}
	if p.Reviewed > 0 {
		parts = append(parts, plural(p.Reviewed, "review done", "reviews done"))
	}
	if p.Posts > 0 {
		parts = append(parts, plural(p.Posts, "post", "posts"))
	}
	if len(parts) == 0 {
		return "nothing moved"
	}
	return strings.Join(parts, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// age is a duration as the check says it ("34 min", "2 h 5 min").
func age(d time.Duration) string {
	m := int(d.Minutes())
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	if m%60 == 0 {
		return fmt.Sprintf("%d h", m/60)
	}
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}

// Age is age, for the rules that build items.
func Age(d time.Duration) string { return age(d) }
