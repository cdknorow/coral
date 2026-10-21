package routes

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/pkg/redact"
)

type taEnv struct {
	t        *testing.T
	srv      *httptest.Server
	h        *BoardHandler
	coralDir string
	ids      map[string]string // object ids by file name
}

// storeTyped stores a blob with a sidecar naming it and giving its media type.
func storeTyped(t *testing.T, coralDir, content, name, mediaType string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	id := hex.EncodeToString(sum[:])
	dir := filepath.Join(coralDir, "artifacts", "objects")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, id), []byte(content), 0o644))
	meta, _ := json.Marshal(agentArtifactMeta{Name: name, MediaType: mediaType, Size: int64(len(content)), Digest: "sha256:" + id})
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), meta, 0o644))
	return id
}

func (e *taEnv) upload(session, name, content, mediaType, at string) string {
	e.t.Helper()
	id := storeTyped(e.t, e.coralDir, content, name, mediaType)
	dir := uploadsDir(e.coralDir, session)
	require.NoError(e.t, os.MkdirAll(dir, 0o755))
	rec, _ := json.Marshal(agentUploadRecord{agentArtifactMeta: agentArtifactMeta{Name: name, MediaType: mediaType, Size: int64(len(content)), Digest: "sha256:" + id}, CreatedAt: at})
	require.NoError(e.t, os.WriteFile(filepath.Join(dir, id+".json"), rec, 0o644))
	e.ids[name] = id
	return id
}

// newTAEnv: alpha has two agents, task results of every kind, uploads over six days, a shared row and a received row.
func newTAEnv(t *testing.T) *taEnv {
	t.Helper()
	srv, h, coralDir := teamArtifactServer(t)
	e := &taEnv{t: t, srv: srv, h: h, coralDir: coralDir, ids: map[string]string{}}
	subscribeSession(t, h, "alpha", "Alice", sessAlice)
	subscribeSession(t, h, "alpha", "Bob", sessBob)
	img := storeTyped(t, coralDir, "PNGDATA", "diagram.png", "image/png")
	pdf := storeTyped(t, coralDir, "PDFDATA-longer-than-the-png", "spec.pdf", "application/pdf")
	e.ids["diagram.png"], e.ids["spec.pdf"] = img, pdf
	completeWithArtifacts(t, h, "alpha", "Build the thing", []board.TaskArtifact{
		{Name: "diagram.png", URI: "coral://artifacts/" + img, Provenance: "uploaded"},
		{Name: "spec.pdf", URI: "coral://artifacts/" + pdf, Provenance: "uploaded"},
		{Name: "report.md", Content: "hello report"},
		{Name: "archive.zip", URI: "https://example.com/archive.zip"},
		{Name: "ghost", URI: "coral://artifacts/" + strings.Repeat("cd", 32), Provenance: "unverified"},
	})
	e.upload(sessAlice, "100% done.md", "one", "text/markdown", "2026-10-01T10:00:00Z")
	e.upload(sessAlice, "a_b.md", "two two", "text/markdown", "2026-10-02T10:00:00Z")
	e.upload(sessAlice, "axb.md", "three three three", "text/markdown", "2026-10-03T10:00:00Z")
	e.upload(sessAlice, "Zeta notes.txt", "four", "text/plain", "2026-10-04T10:00:00Z")
	e.upload(sessAlice, "huge.log", strings.Repeat("x", 3000), "text/plain", "2026-10-05T10:00:00Z")
	e.upload(sessBob, "bob-file.md", "bob wrote this", "text/markdown", "2026-10-06T10:00:00Z")
	sharedID := storeTyped(t, coralDir, "shared bytes", "shared-plan.pdf", "application/pdf")
	recvID := storeTyped(t, coralDir, "received bytes", "recv-notes.txt", "text/plain")
	e.ids["shared-plan.pdf"], e.ids["recv-notes.txt"] = sharedID, recvID
	require.NoError(t, h.writeCrossTeamRecord("shared", crossTeamArtifactRecord{ID: sharedID, Name: "shared-plan.pdf", MediaType: "application/pdf", Size: 12,
		Board: "alpha", OtherBoard: "beta", Server: "local", By: "Orchestrator", At: "2026-10-07T10:00:00Z"}))
	require.NoError(t, h.writeCrossTeamRecord("received", crossTeamArtifactRecord{ID: recvID, Name: "recv-notes.txt", MediaType: "text/plain", Size: 14,
		Board: "alpha", OtherBoard: "gamma", Server: "mac-b", By: "Lead", At: "2026-10-08T10:00:00Z"}))
	return e
}

type taReply struct {
	Artifacts  []teamArtifact `json:"artifacts"`
	Total      *int           `json:"total"`
	All        *int           `json:"all"`
	NextCursor string         `json:"next_cursor"`
	HasMore    bool           `json:"has_more"`
	Offset     int            `json:"offset"`
}

func (e *taEnv) get(query string) (int, taReply, map[string]any) {
	e.t.Helper()
	resp, err := http.Get(e.srv.URL + "/api/board/alpha/artifacts" + query)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var r taReply
	var raw map[string]any
	_ = json.Unmarshal(b, &r)
	_ = json.Unmarshal(b, &raw)
	return resp.StatusCode, r, raw
}

func (e *taEnv) names(query string) []string {
	e.t.Helper()
	code, r, raw := e.get(query)
	require.Equal(e.t, 200, code, query+" "+taJSON(raw))
	return artifactNames(r.Artifacts)
}

func taJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestTeamArtifactsQuerySearch(t *testing.T) {
	e := newTAEnv(t)
	for q, want := range map[string][]string{
		"?q=100%25":               {"100% done.md"}, // % is an ordinary character
		"?q=%25":                  {"100% done.md"}, // not "everything"
		"?q=a_b":                  {"a_b.md"},       // _ is not "any one character": axb.md is not a match
		"?q=_":                    {"a_b.md"},
		"?q=ZETA":                 {"Zeta notes.txt"}, // case-insensitive
		"?q=zeta":                 {"Zeta notes.txt"},
		"?q=application%2Fpdf":    {"spec.pdf", "shared-plan.pdf"}, // the media type
		"?q=bob":                  {"bob-file.md"},                 // who produced it
		"?q=orchestrator":         {"shared-plan.pdf"},             // who sent it
		"?q=beta":                 {"shared-plan.pdf"},             // where it went
		"?q=mac-b%2Fgamma":        {"recv-notes.txt"},              // where it came from, with its server
		"?q=gamma":                {"recv-notes.txt"},
		"?q=nothing-matches-this": {},
		"?q=%27+OR+1%3D1+--":      {},                                                               // quote and comment characters are text
		"?q=build+the":            {"diagram.png", "spec.pdf", "report.md", "archive.zip", "ghost"}, // the task title
	} {
		got := e.names(q)
		sort.Strings(got)
		w := append([]string{}, want...)
		sort.Strings(w)
		if len(w) == 0 {
			assert.Empty(t, got, q)
		} else {
			assert.Equal(t, w, got, q)
		}
	}
	// an empty q is no search
	code, r, _ := e.get("?q=")
	require.Equal(t, 200, code)
	assert.Equal(t, 13, *r.Total)
	assert.Equal(t, 13, *r.All)
	// all is the size of the whole list however the query narrows it ("12 of 340")
	code, r, _ = e.get("?q=zeta&origin=upload&limit=1")
	require.Equal(t, 200, code)
	assert.Equal(t, 1, *r.Total)
	assert.Equal(t, 13, *r.All)
	code, r, _ = e.get("?q=+++")
	require.Equal(t, 200, code)
	assert.Equal(t, 13, *r.Total)
}

func TestTeamArtifactsQueryFilters(t *testing.T) {
	e := newTAEnv(t)
	set := func(q string, want ...string) {
		t.Helper()
		got := e.names(q)
		sort.Strings(got)
		sort.Strings(want)
		if len(want) == 0 {
			assert.Empty(t, got, q)
			return
		}
		assert.Equal(t, want, got, q)
	}
	set("?kind=image", "diagram.png")
	set("?kind=pdf", "spec.pdf", "shared-plan.pdf")
	set("?kind=text", "report.md", "100% done.md", "a_b.md", "axb.md", "Zeta notes.txt", "huge.log", "bob-file.md", "recv-notes.txt")
	set("?kind=other", "archive.zip", "ghost")
	set("?kind=image,pdf", "diagram.png", "spec.pdf", "shared-plan.pdf")
	set("?kind=image&kind=other", "diagram.png", "archive.zip", "ghost")
	set("?origin=unlisted", "ghost")
	set("?origin=task", "diagram.png", "spec.pdf", "report.md", "archive.zip")
	set("?origin=upload", "100% done.md", "a_b.md", "axb.md", "Zeta notes.txt", "huge.log", "bob-file.md")
	set("?origin=shared", "shared-plan.pdf")
	set("?origin=received", "recv-notes.txt")
	set("?origin=shared,received", "shared-plan.pdf", "recv-notes.txt")
	set("?by=alice", "100% done.md", "a_b.md", "axb.md", "Zeta notes.txt", "huge.log")
	set("?by=Bob", "bob-file.md")
	set("?by=orchestrator", "shared-plan.pdf")
	set("?by=lead", "recv-notes.txt", "diagram.png", "spec.pdf", "report.md", "archive.zip", "ghost") // the sender of the received file, and who completed the task
	set("?by=alice,bob", "100% done.md", "a_b.md", "axb.md", "Zeta notes.txt", "huge.log", "bob-file.md")
	set("?by=ali") // exact names only
	set("?board=beta", "shared-plan.pdf")
	set("?board=gamma", "recv-notes.txt")
	set("?board=mac-b/gamma", "recv-notes.txt")
	set("?board=mac-b") // a server alone is not a board
	set("?board=BETA", "shared-plan.pdf")
	set("?since=2026-10-03T00:00:00Z&until=2026-10-06T00:00:00Z", "axb.md", "Zeta notes.txt", "huge.log") // until is exclusive
	set("?since=2026-10-06T10:00:00Z&until=2026-10-09T00:00:00Z", "bob-file.md", "shared-plan.pdf", "recv-notes.txt")
	set("?until=2026-10-02T00:00:00Z", "100% done.md")
	set("?since=2026-10-06T10:00:00Z&until=2026-10-07T10:00:00Z", "bob-file.md")          // since is inclusive, until exclusive, at the very second
	set("?since=2026-10-03T12:00:00%2B02:00&until=2026-10-03T13:00:00%2B02:00", "axb.md") // an offset is read as that instant (10:00Z)
	// combinations
	set("?kind=text&origin=upload&by=alice&since=2026-10-02T00:00:00Z", "a_b.md", "axb.md", "Zeta notes.txt", "huge.log")
	set("?q=notes&kind=text", "Zeta notes.txt", "recv-notes.txt")
	set("?q=notes&origin=received", "recv-notes.txt")
	set("?q=nope&origin=upload")
	// session_id still narrows, and combines
	assert.Equal(t, []string{"axb.md"}, func() []string { n := e.names("?session_id=" + sessAlice + "&q=axb"); return n }())
}

func TestTeamArtifactsQueryKept(t *testing.T) {
	e := newTAEnv(t)
	db, err := sqlx.Open("sqlite", filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE artifact_keep (hex TEXT PRIMARY KEY, kept_at TEXT NOT NULL)`)
	require.NoError(t, err)
	for _, name := range []string{"diagram.png", "shared-plan.pdf", "huge.log"} {
		_, err = db.Exec(`INSERT INTO artifact_keep (hex, kept_at) VALUES (?, ?)`, e.ids[name], time.Now().UTC().Format(time.RFC3339))
		require.NoError(t, err)
	}
	e.h.bs.SetSessionsDB(db)
	kept := e.names("?kept=true")
	sort.Strings(kept)
	assert.Equal(t, []string{"diagram.png", "huge.log", "shared-plan.pdf"}, kept)
	code, r, _ := e.get("?kept=false")
	require.Equal(t, 200, code)
	assert.Equal(t, 10, *r.Total)
	assert.NotContains(t, artifactNames(r.Artifacts), "diagram.png")
	assert.Equal(t, []string{"shared-plan.pdf"}, e.names("?kept=true&origin=shared"))
	assert.Empty(t, e.names("?kept=true&origin=received"))
}

func TestTeamArtifactsQuerySortAndPaging(t *testing.T) {
	e := newTAEnv(t)
	up := "&origin=upload"
	assert.Equal(t, []string{"bob-file.md", "huge.log", "Zeta notes.txt", "axb.md", "a_b.md", "100% done.md"}, e.names("?sort=newest"+up))
	assert.Equal(t, []string{"100% done.md", "a_b.md", "axb.md", "Zeta notes.txt", "huge.log", "bob-file.md"}, e.names("?sort=oldest"+up))
	assert.Equal(t, []string{"100% done.md", "a_b.md", "axb.md", "bob-file.md", "huge.log", "Zeta notes.txt"}, e.names("?sort=name"+up), "by name, ignoring case")
	size := e.names("?sort=size" + up)
	assert.Equal(t, "huge.log", size[0], "largest first")
	assert.Equal(t, "100% done.md", size[len(size)-1], "smallest last")
	// rows with no size (an external link, an unlisted link) come last
	all := e.names("?sort=size")
	assert.Equal(t, "huge.log", all[0])
	assert.ElementsMatch(t, []string{"archive.zip", "ghost"}, all[len(all)-2:])

	// every sort, paged by cursor with a small limit: no gaps, no repeats, the same order as one big page
	for _, sortBy := range []string{"newest", "oldest", "name", "size"} {
		base := "?sort=" + sortBy + "&q=%2E" // q="." keeps everything that has a dot (all but "ghost")
		full := e.names(base)
		require.NotEmpty(t, full)
		var paged []string
		cursor := ""
		for pages := 0; ; pages++ {
			require.Less(t, pages, 20, sortBy)
			qs := base + "&limit=3"
			if cursor != "" {
				qs += "&cursor=" + url.QueryEscape(cursor)
			}
			code, r, raw := e.get(qs)
			require.Equal(t, 200, code, taJSON(raw))
			require.NotNil(t, r.Total)
			assert.Equal(t, len(full), *r.Total, "total is the filtered count on every page")
			paged = append(paged, artifactNames(r.Artifacts)...)
			if r.NextCursor == "" {
				assert.False(t, r.HasMore)
				break
			}
			assert.True(t, r.HasMore)
			assert.Len(t, r.Artifacts, 3)
			cursor = r.NextCursor
		}
		assert.Equal(t, full, paged, sortBy)
	}

	// offset paging still works with a query
	_, r1, _ := e.get("?sort=oldest&origin=upload&limit=2")
	_, r2, _ := e.get("?sort=oldest&origin=upload&limit=2&offset=2")
	assert.Equal(t, []string{"100% done.md", "a_b.md"}, artifactNames(r1.Artifacts))
	assert.Equal(t, []string{"axb.md", "Zeta notes.txt"}, artifactNames(r2.Artifacts))
	assert.Equal(t, 6, *r1.Total)

	// a file added between two pages does not shift the next page (a cursor is a position in the order, not a count)
	_, p1, _ := e.get("?sort=newest&origin=upload&limit=2")
	require.NotEmpty(t, p1.NextCursor)
	e.upload(sessAlice, "later.md", "later", "text/markdown", "2026-10-09T10:00:00Z")
	_, p2, _ := e.get("?sort=newest&origin=upload&limit=2&cursor=" + url.QueryEscape(p1.NextCursor))
	assert.Equal(t, []string{"Zeta notes.txt", "axb.md"}, artifactNames(p2.Artifacts))
	assert.Equal(t, 7, *p2.Total)
}

func TestTeamArtifactsQueryRefusals(t *testing.T) {
	e := newTAEnv(t)
	_, first, _ := e.get("?sort=name&limit=2")
	require.NotEmpty(t, first.NextCursor)
	for q, want := range map[string]string{
		"?nope=1":                        "unknown parameter nope",
		"?nope=1&zip=2&q=a":              "unknown parameter nope, zip",
		"?kind=video":                    "unknown kind video",
		"?kind=":                         "kind has an empty value",
		"?origin=elsewhere":              "unknown origin elsewhere",
		"?origin=task,pending":           "unknown origin pending",
		"?kept=yes":                      "kept must be true or false",
		"?kept=true&kept=false":          "kept is given more than once",
		"?since=yesterday":               "since must be an RFC 3339 time",
		"?until=2026-10-01":              "until must be an RFC 3339 time",
		"?sort=biggest":                  "unknown sort biggest",
		"?sort=name&sort=size":           "sort is given more than once",
		"?q=a&q=b":                       "q is given more than once",
		"?q=" + strings.Repeat("a", 201): "q is at most 200 characters",
		"?cursor=garbage":                "cursor is not one this server gave",
		"?sort=name&cursor=" + url.QueryEscape(first.NextCursor) + "&offset=2": "give a cursor or an offset, not both",
		"?sort=newest&cursor=" + url.QueryEscape(first.NextCursor):             "cursor was made for other filters or another sort",
		"?sort=name&q=x&cursor=" + url.QueryEscape(first.NextCursor):           "cursor was made for other filters or another sort",
		"?q=a;b":                          "the query string is not valid",
		"?q=%zz":                          "the query string is not valid",
		"?limit=0":                        "limit must be an integer between 1 and 200",
		"?by=" + strings.Repeat("x", 201): "over 200 characters",
	} {
		code, _, raw := e.get(q)
		assert.Equal(t, 400, code, q)
		assert.Contains(t, taJSON(raw["error"]), want, q)
	}
}

// With none of the new parameters the reply is the plain list's: the same keys (no total, no next_cursor) and the same rows.
func TestTeamArtifactsPlainListUnchanged(t *testing.T) {
	e := newTAEnv(t)
	code, plain, raw := e.get("")
	require.Equal(t, 200, code)
	assert.Nil(t, plain.Total)
	for _, k := range []string{"project", "artifacts", "limit", "offset", "has_more", "truncated"} {
		assert.Contains(t, raw, k)
	}
	assert.NotContains(t, raw, "total")
	assert.NotContains(t, raw, "all")
	assert.NotContains(t, raw, "next_cursor")
	assert.Len(t, plain.Artifacts, 13)
	// the original parameters still behave
	code, r, raw := e.get("?limit=5&offset=10")
	require.Equal(t, 200, code)
	assert.Len(t, r.Artifacts, 3)
	assert.NotContains(t, raw, "total")
	// the plain list's order is newest first, which is what sort=newest gives
	code, q, _ := e.get("?sort=newest")
	require.Equal(t, 200, code)
	assert.Equal(t, artifactNames(plain.Artifacts), artifactNames(q.Artifacts))
	assert.Equal(t, 13, *q.Total)
	// a parameter this route never read is now refused by name, as the tunnel already did
	code, _, raw = e.get("?cache=1")
	assert.Equal(t, 400, code)
	assert.Contains(t, taJSON(raw["error"]), "unknown parameter cache")
}

// A digest-named object shows the name of its sidecar (or "Uploaded artifact"), and the search sees that name.
func TestTeamArtifactsQuerySearchesTheDisplayedName(t *testing.T) {
	e := newTAEnv(t)
	id := storeTyped(t, e.coralDir, "payload for a digest named upload", "real-name.md", "text/markdown")
	dir := uploadsDir(e.coralDir, sessAlice)
	rec, _ := json.Marshal(agentUploadRecord{agentArtifactMeta: agentArtifactMeta{Name: id, MediaType: "text/markdown", Size: 5, Digest: "sha256:" + id}, CreatedAt: "2026-10-10T10:00:00Z"})
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), rec, 0o644))
	assert.Equal(t, []string{"real-name.md"}, e.names("?q=real-name"))
	assert.Empty(t, e.names("?q="+id[:16]), "the digest is not what is shown or searched")
}

var _ = context.Background

// Through Coral Hub the reply is redacted after the handler matched, so a query that matched on the raw text would tell a viewer what the
// redaction hides, a prefix at a time (Security's #4572 MUST). Tunneled, q, by, board and the name sort see what the viewer sees, and a
// cursor carries no text (an external link's id holds the link).
func TestTeamArtifactsQueryCannotSeeBehindTheRedaction(t *testing.T) {
	// serial: setHubOwner writes the package-level hubOwnerLabel (#5667)
	const tokA = "ghp_aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	const tokB = "ghp_zZ3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	e := newTAEnv(t)
	// names and a task title that hold a token; a sender and a board that are one; an external link with a token in its address
	e.upload(sessAlice, "notes "+tokA+".txt", "token a", "text/plain", "2026-10-10T10:00:00Z")
	e.upload(sessAlice, "notes "+tokB+".txt", "token b", "text/plain", "2026-10-11T10:00:00Z")
	completeWithArtifacts(t, e.h, "alpha", "deploy "+tokA, []board.TaskArtifact{{Name: "link-with-secret", URI: "https://example.com/dl?token=" + tokA}})
	recvID := storeTyped(t, e.coralDir, "from a secret board", "from-secret.txt", "text/plain")
	require.NoError(t, e.h.writeCrossTeamRecord("received", crossTeamArtifactRecord{ID: recvID, Name: "from-secret.txt", MediaType: "text/plain", Size: 5,
		Board: "alpha", OtherBoard: tokA, Server: "local", By: tokB, At: "2026-10-12T10:00:00Z"}))

	tunneled := func(query string) (int, taReply, string) {
		req, err := http.NewRequest("GET", e.srv.URL+"/api/board/alpha/artifacts"+query, nil)
		require.NoError(t, err)
		setHubOwner(t, req)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var r taReply
		_ = json.Unmarshal(b, &r)
		return resp.StatusCode, r, string(b)
	}
	total := func(query string) int {
		t.Helper()
		code, r, body := tunneled(query)
		require.Equal(t, 200, code, query+" "+body)
		return *r.Total
	}

	// The shown name has no token in it, and a prefix of the token finds nothing, however close it is: the oracle sees only what the viewer sees.
	_, r, _ := tunneled("?q=" + url.QueryEscape("notes [redacted") + "&sort=oldest&origin=upload")
	require.Equal(t, 2, len(r.Artifacts))
	for _, a := range r.Artifacts {
		assert.NotContains(t, a.Name, "ghp_")
		assert.Equal(t, "notes [redacted token].txt", a.Name)
	}
	for _, probe := range []string{"ghp_", "ghp_aB3dE5", "ghp_aB3dE6", "ghp_zZ", tokA, tokB, "aB3dE5"} {
		assert.Equal(t, 0, total("?q="+url.QueryEscape(probe)), "q="+probe)
	}
	assert.Equal(t, 3, total("?q=notes&origin=upload"), "the part of a name that is not secret still matches (the fixture's Zeta notes too)")
	assert.Equal(t, 2, total("?q="+url.QueryEscape("[redacted token]")+"&origin=upload"), "what the viewer sees can be searched")
	// the task title, the sender and the other board, the same way
	assert.Equal(t, 0, total("?q="+url.QueryEscape(tokA[:10])))
	assert.Equal(t, 0, total("?by="+url.QueryEscape(tokB)))
	assert.Equal(t, 0, total("?by="+url.QueryEscape(tokB[:12])))
	assert.Equal(t, 0, total("?board="+url.QueryEscape(tokA)))
	assert.Equal(t, 1, total("?by="+url.QueryEscape("[redacted token]")), "the sender as shown")
	// the same probes without the tunnel, for contrast: a local caller keeps raw matching
	assert.ElementsMatch(t, []string{"notes " + tokA + ".txt", "link-with-secret", "from-secret.txt"}, e.names("?q="+url.QueryEscape("ghp_aB3dE5")), "raw: the name, the task title and the other board all hold the token")
	assert.Equal(t, 1, len(e.names("?by="+url.QueryEscape(tokB))))

	// the name sort orders what is shown: two names that differ only inside the token are equal, so the order is the plain one (newest first),
	// where a local caller gets the raw order (tokA < tokB)
	_, sorted, _ := tunneled("?sort=name&origin=upload&q=" + url.QueryEscape("notes [redacted"))
	require.Len(t, sorted.Artifacts, 2)
	assert.Equal(t, "2026-10-11T10:00:00Z", sorted.Artifacts[0].CreatedAt, "tied shown names: newest first, which says nothing of the token")
	local := e.names("?sort=name&origin=upload&q=" + url.QueryEscape("notes ghp_"))
	assert.Equal(t, []string{"notes " + tokA + ".txt", "notes " + tokB + ".txt"}, local)

	// no cursor holds text: not a name, not a link (its id is "uri:<link>"), not a token
	for _, sortBy := range []string{"newest", "oldest", "name", "size"} {
		cursor := ""
		for pages := 0; pages < 30; pages++ {
			qs := "?sort=" + sortBy + "&limit=1"
			if cursor != "" {
				qs += "&cursor=" + url.QueryEscape(cursor)
			}
			code, page, body := tunneled(qs)
			require.Equal(t, 200, code, body)
			_ = body
			if page.NextCursor == "" {
				break
			}
			raw, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
			require.NoError(t, err)
			for _, banned := range []string{"ghp_", "https://", "uri:", "deploy", tokA[:8], tokB[:8]} { // (a name as shown, "notes [redacted token].txt", is what the viewer sees anyway)
				assert.NotContains(t, string(raw), banned, "cursor "+sortBy)
			}
			cursor = page.NextCursor
		}
	}
	// all is the whole list, whatever q is
	_, withQ, _ := tunneled("?q=" + url.QueryEscape("ghp_aB3dE5"))
	_, noQ, _ := tunneled("?q=" + url.QueryEscape("notes"))
	assert.Equal(t, *withQ.All, *noQ.All)
}

// A digest-named upload takes its name from its sidecar, which is read after the first redaction: that name is matched as shown too.
func TestTeamArtifactsQueryRedactsTheSidecarName(t *testing.T) {
	const tok = "ghp_aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	e := newTAEnv(t)
	id := storeTyped(t, e.coralDir, "a digest named upload", "sidecar "+tok+".md", "text/markdown")
	dir := uploadsDir(e.coralDir, sessAlice)
	rec, _ := json.Marshal(agentUploadRecord{agentArtifactMeta: agentArtifactMeta{Name: id, MediaType: "text/markdown", Size: 5, Digest: "sha256:" + id}, CreatedAt: "2026-10-13T10:00:00Z"})
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), rec, 0o644))
	req := func(query string) taReply {
		r, err := http.NewRequest("GET", e.srv.URL+"/api/board/alpha/artifacts"+query, nil)
		require.NoError(t, err)
		setHubOwner(t, r)
		resp, err := http.DefaultClient.Do(r)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out taReply
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return out
	}
	assert.Equal(t, 0, *req("?q=" + url.QueryEscape("sidecar ghp_aB3")).Total)
	hit := req("?q=sidecar")
	require.Equal(t, 1, *hit.Total)
	assert.Equal(t, "sidecar [redacted token].md", hit.Artifacts[0].Name)
	assert.Equal(t, []string{"sidecar " + tok + ".md"}, e.names("?q="+url.QueryEscape("sidecar ghp_aB3")), "locally the raw name matches")
}

// A text the redactor cannot check is hidden from the match, never matched raw.
func TestTeamArtifactsQueryTunneledMatchFailsClosed(t *testing.T) {
	e := newTAEnv(t)
	prev := shownRedactor
	shownRedactor = func() redact.Redactor { return redact.Refuse }
	t.Cleanup(func() { shownRedactor = prev })
	r, err := http.NewRequest("GET", e.srv.URL+"/api/board/alpha/artifacts?q=zeta", nil)
	require.NoError(t, err)
	setHubOwner(t, r)
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out taReply
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, 0, *out.Total, "nothing is matched on text that could not be checked")
	assert.Equal(t, 13, *out.All)
	// a local caller is not affected
	assert.Equal(t, []string{"Zeta notes.txt"}, e.names("?q=zeta"))
}

// A cursor carries a name only when the sort is by name (a local cursor holds no text it does not need).
func TestTeamArtifactsQueryCursorCarriesNoNeedlessText(t *testing.T) {
	e := newTAEnv(t)
	for _, sortBy := range []string{"newest", "oldest", "size"} {
		_, r, _ := e.get("?sort=" + sortBy + "&limit=1")
		require.NotEmpty(t, r.NextCursor)
		raw, err := base64.RawURLEncoding.DecodeString(r.NextCursor)
		require.NoError(t, err)
		var c teamArtifactCursor
		require.NoError(t, json.Unmarshal(raw, &c))
		assert.Empty(t, c.Name, sortBy)
	}
	_, r, _ := e.get("?sort=name&limit=1")
	raw, _ := base64.RawURLEncoding.DecodeString(r.NextCursor)
	var c teamArtifactCursor
	require.NoError(t, json.Unmarshal(raw, &c))
	assert.NotEmpty(t, c.Name)
}
