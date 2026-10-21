package routes

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/querypage"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/secrets"
	"github.com/subgentic/coral/pkg/redact"
)

// The Team Artifacts list's query (#4572): search, filters, sort, cursor paging and a total, on the existing list route. The rows
// are built in memory (task workflow records, upload records, cross-team records), so this filters, sorts and pages that slice;
// there is no SQL. Nothing a caller sends reaches a statement, a path or a pattern: q is a plain case-insensitive substring match
// (so % and _ are ordinary characters), the rest are enum words, names compared for equality, and times.

var teamArtifactParamNames = map[string]bool{"limit": true, "offset": true, "session_id": true,
	"q": true, "kind": true, "origin": true, "by": true, "board": true, "kept": true, "since": true, "until": true, "sort": true, "cursor": true}

// teamArtifactQueryParams are the parameters that make the reply a query (the others are the list's original ones).
var teamArtifactQueryParams = []string{"q", "kind", "origin", "by", "board", "kept", "since", "until", "sort", "cursor"}

var (
	teamArtifactKinds   = []string{"image", "pdf", "text", "other"}
	teamArtifactOrigins = []string{"task", "upload", "shared", "received", "unlisted"}
	teamArtifactSorts   = []string{"newest", "oldest", "name", "size"}
)

const (
	maxTeamArtifactQLen   = 200
	maxTeamArtifactValues = 50
)

type teamArtifactQuery struct {
	q        string // lower-cased
	kinds    []string
	origins  []string
	by       []string // lower-cased
	boards   []string // lower-cased
	kept     *bool
	since    *time.Time
	until    *time.Time
	sortName string
	cursor   *teamArtifactCursor
	finger   string
}

type teamArtifactCursor struct {
	Sort   string `json:"s"`
	Finger string `json:"f"`
	At     string `json:"a"`
	Name   string `json:"n"`
	Size   int64  `json:"z"`
	TaskID int64  `json:"t"`
	Source string `json:"r"`
	Index  int    `json:"x"`
	ID     string `json:"i"`
}

var errTeamArtifactQuery = errors.New("invalid team artifacts query")

func taErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errTeamArtifactQuery, fmt.Sprintf(format, a...))
}

func taMulti(name string, vals []string, allowed []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, v := range vals {
		if querypage.HasControl(v) {
			return nil, taErr("%s has a value over 200 characters or with control characters", name)
		}
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				return nil, taErr("%s has an empty value", name)
			}
			if len(p) > 200 {
				return nil, taErr("%s has a value over 200 characters or with control characters", name)
			}
			if !seen[strings.ToLower(p)] {
				seen[strings.ToLower(p)] = true
				out = append(out, p)
			}
		}
	}
	if len(out) > maxTeamArtifactValues {
		return nil, taErr("%s has more than %d values", name, maxTeamArtifactValues)
	}
	if allowed != nil {
		var bad []string
		for i, v := range out {
			ok := false
			for _, a := range allowed {
				ok = ok || v == a
			}
			if !ok {
				bad = append(bad, v)
			}
			out[i] = v
		}
		if len(bad) > 0 {
			return nil, taErr("unknown %s %s (one of %s)", name, strings.Join(bad, ", "), strings.Join(allowed, ", "))
		}
	}
	return out, nil
}

func taSingle(name string, vals []string) (string, bool, error) {
	switch len(vals) {
	case 0:
		return "", false, nil
	case 1:
		return vals[0], true, nil
	}
	return "", false, taErr("%s is given more than once", name)
}

func taTime(name string, vals []string) (*time.Time, error) {
	v, ok, err := taSingle(name, vals)
	if err != nil || !ok {
		return nil, err
	}
	t, perr := time.Parse(time.RFC3339, v)
	if perr != nil {
		return nil, taErr("%s must be an RFC 3339 time, for example 2026-10-18T00:00:00Z", name)
	}
	t = t.UTC()
	return &t, nil
}

// parseTeamArtifactQuery validates the new parameters; nil, nil when none is given (the list is the plain one).
func parseTeamArtifactQuery(vals url.Values) (*teamArtifactQuery, error) {
	active := false
	for _, k := range teamArtifactQueryParams {
		if _, ok := vals[k]; ok {
			active = true
		}
	}
	if !active {
		return nil, nil
	}
	q := &teamArtifactQuery{sortName: "newest"}
	var err error
	if v, ok, serr := taSingle("q", vals["q"]); serr != nil {
		return nil, serr
	} else if ok {
		if querypage.HasControl(v) {
			return nil, taErr("q is at most %d characters, without control characters", maxTeamArtifactQLen)
		}
		v = strings.TrimSpace(v)
		if len(v) > maxTeamArtifactQLen {
			return nil, taErr("q is at most %d characters, without control characters", maxTeamArtifactQLen)
		}
		q.q = strings.ToLower(v) // empty: no search
	}
	if q.kinds, err = taMulti("kind", vals["kind"], teamArtifactKinds); err != nil {
		return nil, err
	}
	if q.origins, err = taMulti("origin", vals["origin"], teamArtifactOrigins); err != nil {
		return nil, err
	}
	by, err := taMulti("by", vals["by"], nil)
	if err != nil {
		return nil, err
	}
	for _, b := range by {
		q.by = append(q.by, strings.ToLower(b))
	}
	boards, err := taMulti("board", vals["board"], nil)
	if err != nil {
		return nil, err
	}
	for _, b := range boards {
		q.boards = append(q.boards, strings.ToLower(b))
	}
	if v, ok, serr := taSingle("kept", vals["kept"]); serr != nil {
		return nil, serr
	} else if ok {
		switch v {
		case "true", "false":
			b := v == "true"
			q.kept = &b
		default:
			return nil, taErr("kept must be true or false")
		}
	}
	if q.since, err = taTime("since", vals["since"]); err != nil {
		return nil, err
	}
	if q.until, err = taTime("until", vals["until"]); err != nil {
		return nil, err
	}
	if v, ok, serr := taSingle("sort", vals["sort"]); serr != nil {
		return nil, serr
	} else if ok {
		found := false
		for _, s := range teamArtifactSorts {
			found = found || v == s
		}
		if !found {
			return nil, taErr("unknown sort %s (one of %s)", v, strings.Join(teamArtifactSorts, ", "))
		}
		q.sortName = v
	}
	// What a cursor is tied to: every filter and the sort (not limit, offset or the cursor itself).
	fp := sha256.New()
	fmt.Fprintf(fp, "%s|%v|%v|%v|%v|%v|%v|%v|%s|%s", q.q, q.kinds, q.origins, q.by, q.boards, q.kept, q.since, q.until, q.sortName, vals.Get("session_id"))
	q.finger = hex.EncodeToString(fp.Sum(nil))[:16]
	if v, ok, serr := taSingle("cursor", vals["cursor"]); serr != nil {
		return nil, serr
	} else if ok {
		c, cerr := decodeTeamArtifactCursor(v)
		if cerr != nil {
			return nil, taErr("cursor is not one this server gave")
		}
		if c.Sort != q.sortName || c.Finger != q.finger {
			return nil, taErr("cursor was made for other filters or another sort")
		}
		if len(vals["offset"]) > 0 {
			return nil, taErr("give a cursor or an offset, not both")
		}
		q.cursor = c
	}
	return q, nil
}

func decodeTeamArtifactCursor(s string) (*teamArtifactCursor, error) {
	if len(s) > 1024 {
		return nil, errors.New("long")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var c teamArtifactCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if c.ID == "" || c.Finger == "" {
		return nil, errors.New("bad")
	}
	return &c, nil
}

func (c teamArtifactCursor) encode() string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// teamArtifactKind: image, pdf, text or other, from the media type.
func teamArtifactKind(mediaType string) string {
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(mediaType, ";", 2)[0]))
	switch {
	case strings.HasPrefix(mt, "image/"):
		return "image"
	case mt == "application/pdf":
		return "pdf"
	case strings.HasPrefix(mt, "text/"), mt == "application/json", mt == "application/xml", mt == "application/yaml", mt == "application/x-yaml",
		mt == "application/javascript", strings.HasSuffix(mt, "+json"), strings.HasSuffix(mt, "+xml"):
		return "text"
	}
	return "other"
}

// teamArtifactOrigin: where a row came from. A coral://artifacts link the team never uploaded is "unlisted".
func teamArtifactOrigin(it teamArtifact) string {
	if it.Provenance == provenanceUnverified {
		return "unlisted"
	}
	switch it.Source {
	case "upload", "shared", "received":
		return it.Source
	}
	return "task"
}

func teamArtifactMatchesBoard(it teamArtifact, boards []string) bool {
	for _, where := range []string{it.SharedTo, it.ReceivedFrom, it.FromBoard} {
		if where == "" {
			continue
		}
		w := strings.ToLower(where)
		bare := w
		if i := strings.LastIndex(w, "/"); i >= 0 {
			bare = w[i+1:]
		}
		for _, b := range boards {
			if b == w || b == bare {
				return true
			}
		}
	}
	return false
}

func parseArtifactTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// cheapMatch applies the filters that need no file system: origin, by, board, kept and the time range.
func (q *teamArtifactQuery) cheapMatch(it teamArtifact, kept map[string]bool) bool {
	if len(q.origins) > 0 {
		o, ok := teamArtifactOrigin(it), false
		for _, w := range q.origins {
			ok = ok || w == o
		}
		if !ok {
			return false
		}
	}
	if len(q.by) > 0 {
		ok := false
		for _, who := range []string{ptrOr(it.ProducedBy), it.SentBy} {
			for _, b := range q.by {
				ok = ok || (who != "" && strings.ToLower(who) == b)
			}
		}
		if !ok {
			return false
		}
	}
	if len(q.boards) > 0 && !teamArtifactMatchesBoard(it, q.boards) {
		return false
	}
	if q.kept != nil {
		isKept := it.managed != "" && kept[it.managed]
		if isKept != *q.kept {
			return false
		}
	}
	if q.since != nil || q.until != nil {
		t, ok := parseArtifactTime(it.CreatedAt)
		if !ok {
			return false
		}
		if q.since != nil && t.Before(*q.since) {
			return false
		}
		if q.until != nil && !t.Before(*q.until) {
			return false
		}
	}
	return true
}

func ptrOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// textMatch: q is a substring of the name, the task title, who produced or sent it, where it went or came from, or its type.
func (q *teamArtifactQuery) textMatch(it teamArtifact) bool {
	if q.q == "" {
		return true
	}
	for _, f := range []string{it.Name, it.TaskTitle, ptrOr(it.ProducedBy), it.SentBy, it.SharedTo, it.ReceivedFrom, it.FromBoard, it.MediaType} {
		if f != "" && strings.Contains(strings.ToLower(f), q.q) {
			return true
		}
	}
	for _, f := range it.AlsoReceivedFrom {
		if strings.Contains(strings.ToLower(f), q.q) {
			return true
		}
	}
	return false
}

func (q *teamArtifactQuery) kindMatch(it teamArtifact) bool {
	if len(q.kinds) == 0 {
		return true
	}
	k := teamArtifactKind(it.MediaType)
	for _, w := range q.kinds {
		if w == k {
			return true
		}
	}
	return false
}

// needsEnrichment: the name (a digest-named object takes its sidecar's name), the type and the size of managed files come from the
// store, so searching, kind and the name and size sorts read each candidate's sidecar and stat its blob (never its bytes).
func (q *teamArtifactQuery) needsEnrichment() bool {
	return q.q != "" || len(q.kinds) > 0 || q.sortName == "name" || q.sortName == "size"
}

func finishTeamArtifactName(it *teamArtifact) {
	if digestLikeName.MatchString(it.Name) || strings.TrimSpace(it.Name) == "" {
		if it.TaskID == 0 {
			it.Name = "Uploaded artifact"
		} else {
			it.Name = fmt.Sprintf("Task #%d artifact", it.TaskID)
		}
	}
}

// shownRedactor is the redactor the connector uses for Coral Hub, the keyword rules included (#5172: matching must agree with what the reply shows);
// a variable so a test can make it fail.
var shownRedactor = func() redact.Redactor { return secrets.HubRedactor(keywordsOn) }

// shownText is what a viewer through Coral Hub sees of s: the connector's own rules (secrets.HubRedactor) applied. A text that cannot be
// checked is treated as hidden, never matched raw.
func shownText(s string) string {
	if s == "" {
		return s
	}
	out, err := shownRedactor().Text([]byte(s))
	if err != nil {
		return ""
	}
	return string(out)
}

// redactItemForTunnel replaces every text a query matches, sorts or pages by with what the Hub viewer sees. Through Coral Hub the
// reply is redacted AFTER the handler has matched, so matching on the raw text would let q, by, board and the name sort tell a
// viewer what the redaction hides (a prefix at a time); matching on the shown text tells them nothing more than the reply does.
func redactItemForTunnel(it *teamArtifact) {
	it.Name, it.TaskTitle, it.MediaType = shownText(it.Name), shownText(it.TaskTitle), shownText(it.MediaType)
	it.SentBy, it.SharedTo, it.ReceivedFrom, it.FromBoard = shownText(it.SentBy), shownText(it.SharedTo), shownText(it.ReceivedFrom), shownText(it.FromBoard)
	if it.ProducedBy != nil {
		p := shownText(*it.ProducedBy)
		it.ProducedBy = &p
	}
	if len(it.AlsoReceivedFrom) > 0 {
		also := make([]string, len(it.AlsoReceivedFrom))
		for i, a := range it.AlsoReceivedFrom {
			also[i] = shownText(a)
		}
		it.AlsoReceivedFrom = also
	}
}

type artifactSortKey struct {
	At     string
	Name   string
	Size   int64
	TaskID int64
	Source string
	Index  int
	ID     string
}

func keyOf(it teamArtifact) artifactSortKey {
	size := int64(-1)
	if it.Size != nil {
		size = *it.Size
	}
	// The id is a tie-break only, and an external link's id is "uri:<the link>", which can hold a secret: the key (and so the cursor) holds its hash.
	sum := sha256.Sum256([]byte(it.ID))
	return artifactSortKey{At: it.CreatedAt, Name: strings.ToLower(it.Name), Size: size, TaskID: it.TaskID, Source: it.Source, Index: it.index, ID: hex.EncodeToString(sum[:])}
}

// newestAfter: a comes after b in the plain list's order (newest first; ties by task id, source, position, then id, so it is total).
func newestAfter(a, b artifactSortKey) bool {
	switch {
	case a.At != b.At:
		return a.At < b.At
	case a.TaskID != b.TaskID:
		return a.TaskID < b.TaskID
	case a.Source != b.Source:
		return a.Source > b.Source
	case a.Index != b.Index:
		return a.Index > b.Index
	}
	return a.ID > b.ID
}

// artifactAfter: a sorts after b in the order of sortName. Every order falls back to the plain list's, so sort=newest is the plain
// list's order and the others are stable inside equal values.
func artifactAfter(sortName string, a, b artifactSortKey) bool {
	switch sortName {
	case "oldest":
		return newestAfter(b, a)
	case "name":
		if a.Name != b.Name {
			return a.Name > b.Name
		}
	case "size":
		if a.Size != b.Size {
			return a.Size < b.Size // largest first; rows with no size (-1) last
		}
	}
	return newestAfter(a, b)
}

// queryTeamArtifacts answers a query over items (the collected, merged list). It writes the reply.
func (h *BoardHandler) queryTeamArtifacts(w http.ResponseWriter, r *http.Request, project string, items []teamArtifact, truncated bool, limit, offset int, sessionFilter string, q *teamArtifactQuery) {
	var kept map[string]bool
	if q.kept != nil {
		var err error
		if kept, err = h.bs.KeptArtifactIDs(r.Context()); err != nil {
			errInternalServer(w, "could not list artifacts")
			return
		}
	}
	dir := h.artifactObjectDir()
	tunneled := cloud.Tunneled(r)
	if tunneled {
		for i := range items {
			redactItemForTunnel(&items[i])
		}
	}
	var set []teamArtifact
	for _, it := range items {
		if q.cheapMatch(it, kept) {
			set = append(set, it)
		}
	}
	enriched := false
	if q.needsEnrichment() {
		enriched = true
		for i := range set {
			enrichTeamArtifact(&set[i], dir)
			finishTeamArtifactName(&set[i])
			if tunneled {
				redactItemForTunnel(&set[i]) // the sidecar's name and type are new text
			}
		}
		var out []teamArtifact
		for _, it := range set {
			if q.textMatch(it) && q.kindMatch(it) {
				out = append(out, it)
			}
		}
		set = out
	}
	sort.SliceStable(set, func(i, j int) bool { return artifactAfter(q.sortName, keyOf(set[j]), keyOf(set[i])) })
	total := len(set)
	start := offset
	if q.cursor != nil {
		ck := artifactSortKey{At: q.cursor.At, Name: q.cursor.Name, Size: q.cursor.Size, TaskID: q.cursor.TaskID, Source: q.cursor.Source, Index: q.cursor.Index, ID: q.cursor.ID}
		start = sort.Search(len(set), func(i int) bool { return artifactAfter(q.sortName, keyOf(set[i]), ck) })
	}
	if start > len(set) {
		start = len(set)
	}
	end := start + limit
	if end > len(set) {
		end = len(set)
	}
	hasMore := end < len(set)
	page := set[start:end]
	if !enriched {
		for i := range page {
			enrichTeamArtifact(&page[i], dir)
			finishTeamArtifactName(&page[i])
			if tunneled {
				redactItemForTunnel(&page[i])
			}
		}
	}
	if page == nil {
		page = []teamArtifact{}
	}
	resp := map[string]any{
		"project": project, "artifacts": page, "limit": limit, "offset": start,
		"has_more": hasMore, "truncated": truncated, "total": total, "all": len(items),
	}
	if hasMore && len(page) > 0 {
		k := keyOf(page[len(page)-1])
		if q.sortName != "name" {
			k.Name = "" // only the name sort compares names: a cursor carries no text it does not need
		}
		resp["next_cursor"] = teamArtifactCursor{Sort: q.sortName, Finger: q.finger, At: k.At, Name: k.Name, Size: k.Size, TaskID: k.TaskID, Source: k.Source, Index: k.Index, ID: k.ID}.encode()
	}
	if sessionFilter != "" {
		resp["session_id"] = sessionFilter
	}
	writeJSON(w, http.StatusOK, resp)
}
