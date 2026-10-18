package board

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ParseTaskQuery was split for #4599 (#4669 part 2). It reads caller text, so the old function is kept below
// (verbatim but for its name) and the two must give the same query or the same refusal for every input: 300,000
// seeded requests built from right values and hostile ones (quotes, operators, commas and empty parts, control
// characters, 199 to 201 character values, more values than allowed, malformed limits and cursors), and a fuzz
// target. A floor per outcome fails the test if one went unexercised.

var parseOutcomes = map[string]int{
	"ok": 2000, "has an empty value": 1000, "has a value over 200 characters": 500, "has more than": 100,
	"unknown field": 1000, "unknown status": 500, "unknown priority": 500, "unknown verification": 500,
	"is given more than once": 1000, "parked must be true or false": 300, "must be an RFC 3339 time": 300,
	"unknown sort": 300, "unknown order": 300, "limit must be a whole number": 300,
	"cursor is not one this server gave": 100, "cursor was made for a different sort or order": 100,
}

func queryCursor(sort string, desc bool) string {
	b, _ := json.Marshal(queryTaskCursor{Sort: sort, Desc: desc, Val: "2026-10-18T00:00:00Z", ID: 7})
	return base64.RawURLEncoding.EncodeToString(b)
}

func hostileWords() []string {
	return []string{"", " ", ",", "a,,b", "x,", ",x", "'", "\"", "' OR 1=1 --", "\"; DROP TABLE board_tasks; --", "%", "_", "*",
		"a\x00b", "line\nbreak", "tab\there", "zero\u200bwidth", "\u202eflipped", "ünïcödé", "😀", "<script>", "../../etc", strings.Repeat("a", 199),
		strings.Repeat("b", 200), strings.Repeat("c", 201), distinctValues(taskQueryMaxValues), distinctValues(taskQueryMaxValues + 1)}
}

// distinctValues is n different comma-separated values (the parser counts values after dropping repeats).
func distinctValues(n int) string {
	vals := make([]string, n)
	for i := range vals {
		vals[i] = fmt.Sprintf("v%d", i)
	}
	return strings.Join(vals, ",")
}

// pickVals is one parameter's values: none or one right value, or (hostile) up to three values mixing right and
// hostile ones, the parameter possibly repeated.
func pickVals(rng *rand.Rand, good []string, hostile bool) []string {
	if !hostile {
		if rng.Intn(2) == 0 {
			return nil
		}
		return []string{good[rng.Intn(len(good))]}
	}
	n := 1 + rng.Intn(3)
	var out []string
	for i := 0; i < n; i++ {
		var v string
		switch rng.Intn(5) {
		case 0, 1:
			v = good[rng.Intn(len(good))]
		case 2:
			v = good[rng.Intn(len(good))] + "," + good[rng.Intn(len(good))]
		default:
			w := hostileWords()
			v = w[rng.Intn(len(w))]
		}
		out = append(out, v)
	}
	return out
}

// randomTaskQueryParams makes one parameter hostile (or none, a request of right values only) and gives the others
// right values or none, so every parameter, the late ones included, is reached.
func randomTaskQueryParams(rng *rand.Rand) TaskQueryParams {
	fields := append(append([]string{"summary", "board_label", "verification", "nope"}, TaskQueryFieldNames()...), "body", "completion_message")
	people := []string{"Dev 1", "QA", "Orchestrator", "dev 1"}
	since := []string{"2026-10-18T00:00:00Z", "2026-10-18T09:30:00+02:00", "2026-10-18", "yesterday", "2026-13-01T00:00:00Z"}
	limits := []string{"1", "25", "100", "101", "500", "501", "0", "-1", "01", "1e3", " 5", "5 ", "abc", "99999999999999999999"}
	cursors := []string{queryCursor("created_at", true), queryCursor("created_at", false), queryCursor("updated_at", true), queryCursor("updated_at", false),
		"not base64!", base64.RawURLEncoding.EncodeToString([]byte(`{"s":"title","d":true,"v":"x","i":1}`)), base64.RawURLEncoding.EncodeToString([]byte(`{"s":"created_at","d":true,"v":"","i":1}`)), strings.Repeat("A", 600)}
	bad := rng.Intn(16) // 14 parameters; 14 and 15: none hostile
	pv := func(i int, good []string) []string { return pickVals(rng, good, i == bad) }
	return TaskQueryParams{
		Fields:       pv(0, fields),
		Status:       pv(1, append(append([]string{}, taskStatuses...), "done")),
		AssignedTo:   pv(2, people),
		Board:        pv(3, []string{"team", "team-2", "other board"}),
		Priority:     pv(4, append(append([]string{}, taskPriorities...), "urgent")),
		CreatedBy:    pv(5, people),
		Verification: pv(6, []string{VerificationVerified, VerificationEvidence, VerificationUnverified, "maybe"}),
		Parked:       pv(7, []string{"true", "false", "yes", "TRUE"}),
		CreatedSince: pv(8, since),
		UpdatedSince: pv(9, since),
		Sort:         pv(10, []string{"created_at", "updated_at", "title"}),
		Order:        pv(11, []string{"asc", "desc", "up"}),
		Limit:        pv(12, limits),
		Cursor:       pv(13, cursors),
	}
}

func parseResult(q *TaskQuery, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	// The pointed-to values, not the pointers (two equal queries hold different addresses).
	cp := *q
	parked, cursor := "nil", "nil"
	if cp.Parked != nil {
		parked = fmt.Sprint(*cp.Parked)
	}
	if cp.cursor != nil {
		cursor = fmt.Sprintf("%#v", *cp.cursor)
	}
	cp.Parked, cp.cursor = nil, nil
	return fmt.Sprintf("%#v parked=%s cursor=%s", cp, parked, cursor)
}

func agreeParse(t testing.TB, p TaskQueryParams) string {
	t.Helper()
	newQ, newErr := ParseTaskQuery(p)
	oldQ, oldErr := parseTaskQueryBefore4599(p)
	got, want := parseResult(newQ, newErr), parseResult(oldQ, oldErr)
	if got != want || (newErr == nil && !reflect.DeepEqual(newQ, oldQ)) {
		t.Fatalf("ParseTaskQuery(%#v):\n new %s\n old %s", p, got, want)
	}
	return got
}

func TestParseTaskQueryAgreesWithBefore4599(t *testing.T) {
	rng := rand.New(rand.NewSource(4669))
	seen := map[string]int{}
	n := 300000
	if testing.Short() {
		n = 30000
	}
	for i := 0; i < n; i++ {
		r := agreeParse(t, randomTaskQueryParams(rng))
		if !strings.HasPrefix(r, "error: ") {
			seen["ok"]++
			continue
		}
		for outcome := range parseOutcomes {
			if strings.Contains(r, outcome) {
				seen[outcome]++
			}
		}
	}
	t.Logf("compared, per outcome: %v", seen)
	for outcome, floor := range parseOutcomes {
		require.GreaterOrEqual(t, seen[outcome], floor, "%q went (almost) unexercised", outcome)
	}
}

func FuzzParseTaskQueryAgreesWithBefore4599(f *testing.F) {
	for _, w := range hostileWords() {
		f.Add(w, w, w, w)
	}
	f.Add("summary,body", "pending,blocked", "100", queryCursor("created_at", true))
	f.Fuzz(func(t *testing.T, fields, status, limit, cursor string) {
		agreeParse(t, TaskQueryParams{Fields: []string{fields}, Status: []string{status}, Limit: []string{limit}, Cursor: []string{cursor},
			AssignedTo: []string{fields}, Sort: []string{status}, Order: []string{limit}, Parked: []string{cursor}})
	})
}

// parseTaskQueryBefore4599 is ParseTaskQuery as it was before #4599 (#4669 part 2).
func parseTaskQueryBefore4599(p TaskQueryParams) (*TaskQuery, error) {
	q := &TaskQuery{Desc: true}
	var err error
	if f, ferr := splitMulti("fields", p.Fields); ferr != nil {
		return nil, ferr
	} else if f != nil {
		var bad, names []string
		seen := map[string]bool{}
		add := func(n string) {
			if !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
		for _, n := range f {
			if n == "summary" {
				for _, m := range TaskQuerySummaryFields {
					add(m)
				}
				continue
			}
			if _, ok := taskQueryColumns[n]; !ok && n != "board_label" && n != "verification" {
				bad = append(bad, n)
				continue
			}
			add(n)
		}
		if len(bad) > 0 {
			return nil, taskQueryErr("unknown field %s (summary, or one of %s)", strings.Join(bad, ", "), strings.Join(TaskQueryFieldNames(), ", "))
		}
		q.Fields = names
	}
	if q.Statuses, err = oneOf("status", p.Status, taskStatuses); err != nil {
		return nil, err
	}
	if q.Priorities, err = oneOf("priority", p.Priority, taskPriorities); err != nil {
		return nil, err
	}
	if q.Verifications, err = oneOf("verification", p.Verification, []string{VerificationVerified, VerificationEvidence, VerificationUnverified}); err != nil {
		return nil, err
	}
	if q.AssignedTo, err = splitMulti("assigned_to", p.AssignedTo); err != nil {
		return nil, err
	}
	if q.Boards, err = splitMulti("board", p.Board); err != nil {
		return nil, err
	}
	if q.CreatedBy, err = splitMulti("created_by", p.CreatedBy); err != nil {
		return nil, err
	}
	if v, ok, perr := single("parked", p.Parked); perr != nil {
		return nil, perr
	} else if ok {
		switch v {
		case "true":
			b := true
			q.Parked = &b
		case "false":
			b := false
			q.Parked = &b
		default:
			return nil, taskQueryErr("parked must be true or false")
		}
	}
	if q.CreatedSince, err = parseSince("created_since", p.CreatedSince); err != nil {
		return nil, err
	}
	if q.UpdatedSince, err = parseSince("updated_since", p.UpdatedSince); err != nil {
		return nil, err
	}
	if v, ok, serr := single("sort", p.Sort); serr != nil {
		return nil, serr
	} else if ok {
		switch v {
		case "created_at":
		case "updated_at":
			q.SortUpdated = true
		default:
			return nil, taskQueryErr("unknown sort %s (created_at or updated_at)", v)
		}
	}
	if v, ok, serr := single("order", p.Order); serr != nil {
		return nil, serr
	} else if ok {
		switch v {
		case "desc":
		case "asc":
			q.Desc = false
		default:
			return nil, taskQueryErr("unknown order %s (asc or desc)", v)
		}
	}
	large := q.Fields == nil // the whole row carries the long text
	for _, f := range q.Fields {
		if f == "body" || f == "completion_message" {
			large = true
		}
	}
	max := TaskQueryMaxLimit
	if large {
		max = TaskQueryMaxLimitLarge
	}
	q.Limit = TaskQueryDefaultLimit
	if v, ok, serr := single("limit", p.Limit); serr != nil {
		return nil, serr
	} else if ok {
		n := 0
		if _, scanErr := fmt.Sscanf(v, "%d", &n); scanErr != nil || fmt.Sprint(n) != v || n < 1 || n > max {
			return nil, taskQueryErr("limit must be a whole number from 1 to %d", max)
		}
		q.Limit = n
	} else if q.Limit > max {
		q.Limit = max
	}
	if v, ok, serr := single("cursor", p.Cursor); serr != nil {
		return nil, serr
	} else if ok {
		c, cerr := decodeQueryTaskCursor(v)
		if cerr != nil {
			return nil, taskQueryErr("cursor is not one this server gave")
		}
		sortName := "created_at"
		if q.SortUpdated {
			sortName = "updated_at"
		}
		if c.Sort != sortName || c.Desc != q.Desc {
			return nil, taskQueryErr("cursor was made for a different sort or order")
		}
		q.cursor = c
	}
	return q, nil
}
