// Command coral-shadow-report summarizes the automated-input gate's shadow mode from
// Coral's log (#3494, #3475 phase 1): each "automated input decision" line holds the
// screen rule's decision (old) and the signal rule's (new). The phase 2 switch to the
// signal rule waits for near-zero unexplained disagreements, and this is the data.
//
//	coral-shadow-report [-top N] [-since YYYY-MM-DD] <coral.log> [more logs]
//
// It only reads. It prints counts, rates, reason classes and session ids, never message
// text: a send's key is not printed (a board message's key is its text), and screen text
// in a reason is cut to its class ("prompt question", "selection menu", ...).
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	top := flag.Int("top", 10, "how many reason pairs and examples to list")
	since := flag.String("since", "", "only lines on or after this date (YYYY-MM-DD)")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: coral-shadow-report [-top N] [-since YYYY-MM-DD] <coral.log> [more logs]  (each log's rotated copies, <log>.1 ..., are read too)")
		os.Exit(2)
	}
	r := newReport(strings.ReplaceAll(*since, "-", "/"))
	for _, path := range withRotatedCopies(flag.Args()) {
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		r.read(f)
		f.Close()
	}
	r.print(os.Stdout, *top)
}

// withRotatedCopies adds each log's rotated copies (coral.log rotates by size: coral.log.1 is
// the newest copy, coral.log.5 the oldest; #3628), oldest first and before the log itself, so a
// day that spans a rotation loses no decision. A path that is itself a numbered copy is read as
// given, and no file is read twice.
func withRotatedCopies(paths []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range paths {
		if isRotatedCopy(p) {
			add(p)
			continue
		}
		matches, _ := filepath.Glob(p + ".*")
		var copies []string
		for _, m := range matches {
			if isRotatedCopy(m) && strings.TrimRight(m, "0123456789") == p+"." {
				copies = append(copies, m)
			}
		}
		sort.Slice(copies, func(i, j int) bool { return copyNumber(copies[i]) > copyNumber(copies[j]) })
		for _, c := range copies {
			add(c)
		}
		add(p)
	}
	return out
}

// isRotatedCopy reports whether p ends in .<number> (coral.log.3).
func isRotatedCopy(p string) bool { return copyNumber(p) > 0 }

func copyNumber(p string) int {
	i := strings.LastIndexByte(p, '.')
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(p[i+1:])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// decision is one parsed "automated input decision" line.
type decision struct {
	session, sender, agentType, cliVersion, mode string
	state, kind                                  string // the signals' state and open request kind (#3482 delta 3)
	old, new                                     string // "send" or the hold reason class
	agree                                        bool
	stateFor                                     time.Duration // how long the signals had been in their state (#3662 P6); 0 when not logged
}

type report struct {
	since         string
	decisions     []decision
	disagreements map[string]int // state_disagreement: "class|cli version" → count
	disExamples   map[string][]string
	lines         int
}

func newReport(since string) *report {
	return &report{since: since, disagreements: map[string]int{}, disExamples: map[string][]string{}}
}

func (r *report) read(in io.Reader) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		r.lines++
		if r.since != "" && len(line) >= 10 && line[:10] < r.since {
			continue
		}
		if i := strings.Index(line, "automated input decision "); i >= 0 {
			kv := parseKV(line[i+len("automated input decision "):])
			d := decision{
				session: kv["session"], sender: kv["sender"], mode: kv["mode"],
				agentType: kv["agent_type"], cliVersion: kv["cli_version"],
				state: kv["state"], kind: kv["kind"],
				old: outcome(kv["old"]), new: outcome(kv["new"]), agree: kv["agree"] == "true",
			}
			if dur, err := time.ParseDuration(kv["state_for"]); err == nil {
				d.stateFor = dur
			}
			if d.agentType == "" {
				d.agentType = agentTypeOf(d.session)
			}
			r.decisions = append(r.decisions, d)
			continue
		}
		if i := strings.Index(line, "state_disagreement"); i >= 0 {
			kv := parseKV(line[i:])
			key := reasonClass(kv["why"]) + "|" + orUnknown(kv["cli_version"])
			r.disagreements[key]++
			if ex := r.disExamples[key]; len(ex) < 3 && kv["session"] != "" && !contains(ex, kv["session"]) {
				r.disExamples[key] = append(ex, kv["session"])
			}
		}
	}
}

func (r *report) print(w io.Writer, top int) {
	fmt.Fprintf(w, "Shadow-mode report: %d log lines read, %d decisions", r.lines, len(r.decisions))
	if r.since != "" {
		fmt.Fprintf(w, " (since %s)", strings.ReplaceAll(r.since, "/", "-"))
	}
	fmt.Fprintln(w)
	if len(r.decisions) == 0 {
		fmt.Fprintln(w, "No 'automated input decision' lines: is the server on a build with shadow mode (#3482), and did any automated send happen?")
	}
	sessions, modes := map[string]bool{}, map[string]int{}
	for _, d := range r.decisions {
		sessions[d.session] = true
		modes[orUnknown(d.mode)]++
	}
	if len(r.decisions) > 0 {
		fmt.Fprintf(w, "Sessions: %d   Mode: %s\n", len(sessions), joinCounts(modes))
	}

	agreementTable(w, r.decisions, "Agreement by agent type and CLI version:", func(d decision) string {
		return d.agentType + " " + orUnknown(d.cliVersion)
	})
	// By the state the signals saw: where in a turn the two rules part ways.
	agreementTable(w, r.decisions, "Agreement by state (agent type, signals' state, request kind):", func(d decision) string {
		k := d.agentType + " " + orUnknown(d.state)
		if d.kind != "" {
			k += " (" + d.kind + ")"
		}
		return k
	})

	// Disagreements by class and reason pair.
	classes := []struct{ name, desc string }{
		{"old=send new=hold", "the signal rule would HOLD what the screen rule sent (stricter)"},
		{"old=hold new=send", "the signal rule would SEND what the screen rule held (looser: check each one)"},
	}
	for _, c := range classes {
		pairs, examples := map[string]int{}, map[string][]string{}
		n := 0
		for _, d := range r.decisions {
			if d.agree || class(d) != c.name {
				continue
			}
			n++
			p := d.agentType + ": " + d.old + "  ->  " + d.new
			pairs[p]++
			if ex := examples[p]; len(ex) < 3 && !contains(ex, d.session) {
				examples[p] = append(ex, d.session)
			}
		}
		fmt.Fprintf(w, "\n%s: %d, %s\n", c.name, n, c.desc)
		for i, p := range sortedKeys(pairs, func(a, b string) bool { return pairs[a] > pairs[b] || (pairs[a] == pairs[b] && a < b) }) {
			if i == top {
				break
			}
			fmt.Fprintf(w, "  %5d  %s   e.g. %s\n", pairs[p], p, strings.Join(examples[p], ", "))
		}
	}

	// Same send/hold, different reasons: not counted as disagreement, but useful.
	reasons := map[string]int{}
	for _, d := range r.decisions {
		if d.agree && d.old != d.new {
			reasons[d.agentType+": "+d.old+"  ->  "+d.new]++
		}
	}
	if len(reasons) > 0 {
		fmt.Fprintln(w, "\nAgreeing, with different reasons (top):")
		for i, p := range sortedKeys(reasons, func(a, b string) bool { return reasons[a] > reasons[b] || (reasons[a] == reasons[b] && a < b) }) {
			if i == top {
				break
			}
			fmt.Fprintf(w, "  %5d  %s\n", reasons[p], p)
		}
	}

	r.printLongHolds(w, top)

	fmt.Fprintf(w, "\nstate_disagreement (signals idle, a prompt on screen): %d\n", sum(r.disagreements))
	for i, k := range sortedKeys(r.disagreements, func(a, b string) bool {
		return r.disagreements[a] > r.disagreements[b] || (r.disagreements[a] == r.disagreements[b] && a < b)
	}) {
		if i == top {
			break
		}
		cls, ver, _ := strings.Cut(k, "|")
		fmt.Fprintf(w, "  %5d  %s (CLI %s)   e.g. %s\n", r.disagreements[k], cls, ver, strings.Join(r.disExamples[k], ", "))
	}
}

// longHoldLimit: a signal hold with no prompt on screen longer than this is looked at by hand
// (#3662 P6, Security: a Blocked or an in-turn state that never clears would hold routine sends).
const longHoldLimit = 10 * time.Minute

// printLongHolds lists, per session, the oldest signal state that held a send while the screen
// showed no prompt (the screen rule sent it): a lower bound on how long the signal rule would
// have held it (#3662 P6).
func (r *report) printLongHolds(w io.Writer, top int) {
	longest := map[string]decision{}
	logged := false
	for _, d := range r.decisions {
		if d.old != "send" || !strings.HasPrefix(d.new, "hold") {
			continue
		}
		if d.stateFor > 0 {
			logged = true
		}
		if cur, ok := longest[d.session]; !ok || d.stateFor > cur.stateFor {
			longest[d.session] = d
		}
	}
	over := 0
	for _, d := range longest {
		if d.stateFor > longHoldLimit {
			over++
		}
	}
	fmt.Fprintf(w, "\nLongest signal hold with no prompt on screen, per session (P6; over %s: %d)\n", longHoldLimit, over)
	if len(longest) == 0 {
		fmt.Fprintln(w, "  none: the signal rule held nothing the screen rule sent")
		return
	}
	if !logged {
		fmt.Fprintln(w, "  no state_for in the log: the server predates #3662 P6")
		return
	}
	sessions := make([]string, 0, len(longest))
	for s := range longest {
		sessions = append(sessions, s)
	}
	sort.Slice(sessions, func(i, j int) bool {
		a, b := longest[sessions[i]], longest[sessions[j]]
		return a.stateFor > b.stateFor || (a.stateFor == b.stateFor && sessions[i] < sessions[j])
	})
	for i, s := range sessions {
		if i == top {
			break
		}
		d := longest[s]
		flag := ""
		if d.stateFor > longHoldLimit {
			flag = "   <- over " + longHoldLimit.String() + ": check by hand"
		}
		fmt.Fprintf(w, "  %9s  %s  %s (%s)%s\n", d.stateFor, s, d.new, orUnknown(d.state), flag)
	}
}

// agreementTable prints the agreement rate of the decisions grouped by key, largest first.
func agreementTable(w io.Writer, decisions []decision, title string, key func(decision) string) {
	type group struct{ total, agree int }
	groups := map[string]*group{}
	for _, d := range decisions {
		k := key(d)
		if groups[k] == nil {
			groups[k] = &group{}
		}
		groups[k].total++
		if d.agree {
			groups[k].agree++
		}
	}
	if len(groups) == 0 {
		return
	}
	fmt.Fprintln(w, "\n"+title)
	for _, k := range sortedKeys(groups, func(a, b string) bool {
		return groups[a].total > groups[b].total || (groups[a].total == groups[b].total && a < b)
	}) {
		g := groups[k]
		fmt.Fprintf(w, "  %-40s %6d decisions  %5.1f%% agree  %d disagree\n", k, g.total, 100*float64(g.agree)/float64(g.total), g.total-g.agree)
	}
}

// class is a disagreement's direction.
func class(d decision) string {
	switch {
	case d.old == "send" && d.new != "send":
		return "old=send new=hold"
	case d.old != "send" && d.new == "send":
		return "old=hold new=send"
	}
	return ""
}

// outcome is "send", or a hold's reason with screen text cut to its class.
func outcome(v string) string {
	if v == "" {
		return "?"
	}
	if v == "send" {
		return v
	}
	return "hold: " + reasonClass(strings.TrimPrefix(v, "hold: "))
}

// screenClasses are the detector reasons that carry screen text after a colon.
var screenClasses = []string{"prompt question", "prompt footer", "selection menu", "trust menu"}

// reasonClass keeps a reason's words but cuts any screen text quoted after a detector
// class ("prompt question: Do you want to run rm ...?" becomes "prompt question").
func reasonClass(why string) string {
	why = strings.TrimSpace(why)
	if why == "" {
		return "?"
	}
	for _, c := range screenClasses {
		if i := strings.Index(why, c+":"); i >= 0 {
			why = why[:i] + c
		}
	}
	if len(why) > 120 {
		why = why[:120] + "…"
	}
	return why
}

// agentTypeOf reads the agent type from a Coral session name ("claude-<uuid>").
func agentTypeOf(session string) string {
	if t, _, ok := strings.Cut(session, "-"); ok && t != "" {
		return t
	}
	return "unknown"
}

// parseKV reads slog's text form: key=value pairs, a value quoted (strconv) when it has
// spaces or quotes.
func parseKV(s string) map[string]string {
	out := map[string]string{}
	for {
		s = strings.TrimLeft(s, " ")
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			return out
		}
		key := s[:eq]
		if strings.ContainsAny(key, " \"") {
			// Not a key: skip one word (message text before the pairs).
			if sp := strings.IndexByte(s, ' '); sp >= 0 {
				s = s[sp+1:]
				continue
			}
			return out
		}
		rest := s[eq+1:]
		if strings.HasPrefix(rest, `"`) {
			end := 1
			for end < len(rest) {
				if rest[end] == '\\' {
					end += 2
					continue
				}
				if rest[end] == '"' {
					break
				}
				end++
			}
			if end >= len(rest) {
				end = len(rest) - 1
			}
			if v, err := strconv.Unquote(rest[:end+1]); err == nil {
				out[key] = v
			} else {
				out[key] = strings.Trim(rest[:end+1], `"`)
			}
			s = rest[min(end+1, len(rest)):]
			continue
		}
		val, next, _ := strings.Cut(rest, " ")
		out[key] = val
		s = next
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func joinCounts(m map[string]int) string {
	var parts []string
	for _, k := range sortedKeys(m, func(a, b string) bool { return m[a] > m[b] || (m[a] == m[b] && a < b) }) {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func sortedKeys[V any](m map[string]V, less func(a, b string) bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return less(keys[i], keys[j]) })
	return keys
}
