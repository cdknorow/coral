package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The release channels (#5557): the Agentic list feed, the approval line, the notes, and the cache that keeps the two channels apart.

var testNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

type rel map[string]any

// release builds one entry of the list reply: a plain tag on the pinned repository, published, unapproved unless approved is true.
func release(tag string, approved bool) rel {
	return rel{"tag_name": tag, "draft": false, "prerelease": !approved, "html_url": tagPagePrefix + tag, "body": "notes of " + tag, "published_at": "2026-10-20T10:00:00Z"}
}

func (r rel) with(k string, v any) rel {
	out := rel{}
	for a, b := range r {
		out[a] = b
	}
	out[k] = v
	return out
}

func listBody(t *testing.T, rs ...rel) string {
	t.Helper()
	b, err := json.Marshal(rs)
	require.NoError(t, err)
	return string(b)
}

func feedOf(t *testing.T, rs ...rel) *Feed {
	t.Helper()
	f, err := parseList(strings.NewReader(listBody(t, rs...)), testNow)
	require.NoError(t, err)
	return f
}

func waitingVersions(f *Feed) []string {
	var out []string
	for _, w := range f.Waiting {
		out = append(out, w.Version)
	}
	return out
}

func TestParseChannelReadsOnlyTheWordAgenticAsAgentic(t *testing.T) {
	for in, want := range map[string]Channel{"agentic": Agentic, "Agentic": Agentic, " AGENTIC ": Agentic,
		"approved": Approved, "": Approved, "nightly": Approved, "all": Approved, "agentic,approved": Approved, "agent": Approved, "agentic\x00": Approved} {
		assert.Equal(t, want, ParseChannel(in), "%q", in)
	}
}

func TestTheAgenticFeedIsAskedByTheRepositoryIDAndNothingIsBuiltFromAReply(t *testing.T) {
	assert.Equal(t, "https://api.github.com/repositories/1160696364/releases?per_page=10", ReleasesListAPI)
	assert.Equal(t, "https://github.com/subgentic/coral/releases", ReleasesURL)
	assert.Equal(t, Feeds{Approved: ReleasesAPI, Agentic: ReleasesListAPI}, DefaultFeeds)
	assert.Equal(t, ReleasesURL, ReleasePageURL(Approved, "1.4.150"))
	assert.Equal(t, ReleasesURL+"/tag/v1.4.150", ReleasePageURL(Agentic, "1.4.150"))
	assert.Equal(t, ReleasesURL+"/tag/v1.4.150", ReleasePageURL(Agentic, "v1.4.150"))
	for _, bad := range []string{"", "latest", "1.4.150/../../x", "1.4.150?x=1", "1.4.x", "http://evil.example/"} {
		assert.Equal(t, ReleasesURL, ReleasePageURL(Agentic, bad), "%q", bad)
	}
}

func TestTheAgenticOfferIsTheHighestPlainVersionNotTheFirstInTheList(t *testing.T) {
	f := feedOf(t, release("v1.4.141", true), release("v1.4.144", false), release("v1.4.143", false), release("v1.4.142", true))
	assert.Equal(t, Agentic, f.Channel)
	assert.Equal(t, "1.4.144", f.Offer.Version)
	assert.True(t, f.Offer.Prerelease, "the offered release is not approved yet")
	require.NotNil(t, f.Approved)
	assert.Equal(t, "1.4.142", f.Approved.Version, "the newest approved, not the first approved in the list")
	assert.Equal(t, []string{"1.4.144", "1.4.143"}, waitingVersions(f), "unapproved and newer than approved, newest first")
	// a numeric comparison: 1.4.9 is older than 1.4.10
	g := feedOf(t, release("v1.4.9", true), release("v1.4.10", true))
	assert.Equal(t, "1.4.10", g.Offer.Version)
}

func TestAnUnapprovedReleaseOlderThanTheNewestApprovedIsSupersededNotWaiting(t *testing.T) {
	f := feedOf(t, release("v1.4.142", true), release("v1.4.140", false), release("v1.4.141", false))
	assert.Equal(t, "1.4.142", f.Offer.Version)
	assert.Empty(t, f.Waiting)
	assert.False(t, f.Offer.Prerelease)
}

func TestWhenNothingIsApprovedEveryPlainReleaseWaits(t *testing.T) {
	f := feedOf(t, release("v1.4.2", false), release("v1.4.1", false))
	assert.Nil(t, f.Approved)
	assert.Equal(t, []string{"1.4.2", "1.4.1"}, waitingVersions(f))
}

func TestTheWaitingListIsCappedAtFive(t *testing.T) {
	var rs []rel
	for i := 1; i <= 8; i++ {
		rs = append(rs, release(fmt.Sprintf("v1.5.%d", i), false))
	}
	rs = append(rs, release("v1.4.0", true))
	f := feedOf(t, rs...)
	assert.Equal(t, []string{"1.5.8", "1.5.7", "1.5.6", "1.5.5", "1.5.4"}, waitingVersions(f))
}

// Drafts, tags with a suffix, other tag shapes, another repository's release pages and a duplicate never count.
func TestTheAgenticFeedIgnoresDraftsSuffixTagsAndReleasesOfAnotherRepository(t *testing.T) {
	base := release("v1.4.100", true)
	for name, bad := range map[string]rel{
		"draft":             release("v1.4.999", false).with("draft", true),
		"-rc":               release("v1.4.999-rc1", false),
		"-beta":             release("v1.4.999-beta", false),
		"-dev":              release("v1.4.999-dev", false),
		"-forDropbox":       release("v1.4.999-forDropbox", false),
		"-rc on a release":  release("v1.4.999-rc1", true),
		"no v":              release("1.4.999", false).with("html_url", tagPagePrefix+"1.4.999"),
		"four numbers":      release("v1.4.999.1", false),
		"word":              release("vnext", false),
		"other owner":       release("v1.4.999", false).with("html_url", "https://github.com/cdknorow/coral/releases/tag/v1.4.999"),
		"look-alike repo":   release("v1.4.999", false).with("html_url", "https://github.com/subgentic/coral-evil/releases/tag/v1.4.999"),
		"look-alike host":   release("v1.4.999", false).with("html_url", "https://github.com.evil.example/subgentic/coral/releases/tag/v1.4.999"),
		"no page":           release("v1.4.999", false).with("html_url", ""),
		"another tag's url": release("v1.4.999", false).with("html_url", tagPagePrefix+"v1.4.998"),
		"http page":         release("v1.4.999", false).with("html_url", "http://github.com/subgentic/coral/releases/tag/v1.4.999"),
	} {
		f := feedOf(t, base, bad)
		assert.Equal(t, "1.4.100", f.Offer.Version, name)
		assert.Empty(t, f.Waiting, name)
	}
	dup := feedOf(t, base, release("v1.4.101", false), release("v1.4.101", true))
	assert.Equal(t, []string{"1.4.101"}, waitingVersions(dup), "the first entry of a tag wins; a second is ignored")
}

func TestTheAgenticFeedRefusesWhatHasNoPlainRelease(t *testing.T) {
	for name, body := range map[string]string{
		"empty list":    `[]`,
		"only suffixes": listBody(t, release("v1.4.1-rc1", false), release("v1.4.2-beta", true)),
		"only a draft":  listBody(t, release("v1.4.1", true).with("draft", true)),
		"another repo":  listBody(t, release("v1.4.1", true).with("html_url", "https://github.com/x/y/releases/tag/v1.4.1")),
	} {
		_, err := parseList(strings.NewReader(body), testNow)
		assert.ErrorIs(t, err, ErrNoRelease, name)
	}
	for name, body := range map[string]string{
		"an object":  `{"tag_name":"v1.4.1"}`,
		"bad json":   `[{"tag_name":`,
		"a string":   `"v1.4.1"`,
		"null":       `null`,
		"not a list": `{"message":"Not Found"}`,
	} {
		_, err := parseList(strings.NewReader(body), testNow)
		assert.Error(t, err, name)
	}
}

func TestAListOverOneMebibyteIsAnErrorNotAPartialAnswer(t *testing.T) {
	big := release("v1.4.9", true).with("body", strings.Repeat("x", MaxListBody))
	_, err := parseList(strings.NewReader(listBody(t, release("v1.4.8", true), big)), testNow)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "larger than")
	// ten ordinary releases with a few KB of notes each are well inside the cap
	var rs []rel
	for i := 0; i < 10; i++ {
		rs = append(rs, release(fmt.Sprintf("v1.4.%d", i+1), true).with("body", strings.Repeat("note line\n", 600)))
	}
	f := feedOf(t, rs...)
	assert.Equal(t, "1.4.10", f.Offer.Version)
	assert.LessOrEqual(t, len([]rune(f.Offer.Notes)), MaxNotes)
}

// Whether a release is approved comes from the pre-release flag, never from the notes an agent wrote.
func TestAnApprovedLineInTheNotesNeverApprovesAPreRelease(t *testing.T) {
	pre := release("v1.4.150", false).with("body", "notes\nApproved: 2026-10-21T10:00:00Z")
	f := feedOf(t, release("v1.4.149", true), pre)
	assert.Equal(t, "1.4.149", f.Approved.Version)
	assert.Equal(t, []string{"1.4.150"}, waitingVersions(f))
	assert.True(t, f.Waiting[0].ApprovedAt.IsZero(), "a pre-release shows no approval time")
	assert.True(t, f.Offer.Prerelease)
}

func TestTheApprovalTimeIsTheLastLineAndOnlyWhenPlausible(t *testing.T) {
	published := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	at := func(body string) time.Time { return approvedAt(body, published, testNow) }
	want := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	assert.Equal(t, want, at("notes\n\nApproved: 2026-10-21T09:30:00Z"))
	assert.Equal(t, want, at("notes\nApproved: 2026-10-21T09:30:00Z\n\n  \n"), "trailing blank lines do not hide it")
	assert.Equal(t, want, at("notes\r\nApproved: 2026-10-21T09:30:00Z\r\n"))
	assert.Equal(t, want, at("Approved: 2026-10-21 09:30:00 UTC"))
	assert.Equal(t, want, at("Approved: 2026-10-21T11:30:00+02:00"), "another offset is the same instant in UTC")
	for name, body := range map[string]string{
		"not last":   "Approved: 2026-10-21T09:30:00Z\nmore notes",
		"no line":    "notes only",
		"empty":      "",
		"no time":    "Approved:",
		"garbage":    "Approved: yesterday",
		"in future":  "Approved: 2026-10-21T14:00:00Z",
		"before pub": "Approved: 2026-10-19T09:30:00Z",
		"lower case": "approved: 2026-10-21T09:30:00Z",
		"indented":   "x\n  Approved by me: 2026-10-21T09:30:00Z",
	} {
		assert.True(t, at(body).IsZero(), name)
	}
	f := feedOf(t, release("v1.4.1", true).with("body", "n\nApproved: 2026-10-21T09:30:00Z"))
	assert.Equal(t, want, f.Approved.ApprovedAt)
}

func TestNotesAreCleanedToPlainTextAndCut(t *testing.T) {
	got := cleanNotes("a\u202eb\u200dc\x00d\x1b[31me\r\nline2\tx\xffy")
	assert.Equal(t, "abcd[31me\nline2\txy", got, "override, joiner, NUL, ESC and invalid bytes are dropped; line break and tab stay")
	long := strings.Repeat("\u00e9", MaxNotes+50)
	assert.Equal(t, MaxNotes, len([]rune(cleanNotes(long))), "cut by characters, not bytes")
	assert.Equal(t, "", cleanNotes("   \n\t "))
	html := "<script>alert(1)</script> [x](javascript:alert(1))"
	assert.Equal(t, html, cleanNotes(html), "markup is not interpreted here: the dashboard shows it as text")
}

func TestTheApprovedFeedReadsOneReleaseAndRefusesAPreRelease(t *testing.T) {
	obj := func(r rel) string { b, _ := json.Marshal(r); return string(b) }
	f, err := parseLatest(strings.NewReader(obj(release("v1.4.142", true).with("body", "n\nApproved: 2026-10-20T11:00:00Z"))), testNow)
	require.NoError(t, err)
	assert.Equal(t, Approved, f.Channel)
	assert.Equal(t, "1.4.142", f.Offer.Version)
	require.NotNil(t, f.Approved)
	assert.Equal(t, f.Offer, *f.Approved)
	assert.Empty(t, f.Waiting, "Approved users get no waiting list")
	assert.False(t, f.Approved.ApprovedAt.IsZero())
	// the old fixture shape: a tag and nothing else is still a release
	f, err = parseLatest(strings.NewReader(`{"tag_name":"v0.14.0"}`), testNow)
	require.NoError(t, err)
	assert.Equal(t, "0.14.0", f.Offer.Version)
	_, err = parseLatest(strings.NewReader(obj(release("v1.4.143", false))), testNow)
	assert.Error(t, err, "releases/latest never names a pre-release; if it does, nothing is offered")
	_, err = parseLatest(strings.NewReader(obj(release("v1.4.143", true).with("draft", true))), testNow)
	assert.Error(t, err)
	_, err = parseLatest(strings.NewReader(`{"tag_name":"latest"}`), testNow)
	assert.ErrorIs(t, err, ErrBadTag)
}

// ---- over HTTP ----

func feedServer(t *testing.T, body string, hits *atomic.Int32, seen *atomic.Value) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if seen != nil {
			seen.Store(r.Header.Get("Authorization") + "|" + r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func TestFetchFeedSendsNoCredentialAndFollowsOnlySameHostRedirects(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_secret_token_value")
	var seen atomic.Value
	s := feedServer(t, listBody(t, release("v1.4.1", true)), nil, &seen)
	f, err := FetchFeed(context.Background(), Agentic, s.URL+"/repositories/1160696364/releases?per_page=10", testNow)
	require.NoError(t, err)
	assert.Equal(t, "1.4.1", f.Offer.Version)
	assert.Equal(t, "|/repositories/1160696364/releases?per_page=10", seen.Load(), "no Authorization header, the URL as given")

	other := feedServer(t, listBody(t, release("v9.9.9", true)), nil, nil)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+strings.TrimPrefix(other.URL, "http://127.0.0.1:")+"/x", http.StatusFound)
	}))
	defer redirect.Close()
	_, err = FetchFeed(context.Background(), Agentic, redirect.URL, testNow)
	assert.Error(t, err, "a redirect to another host is refused")
}

func TestFetchFeedAnswersOnlyAnOKStatus(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError, http.StatusNotModified} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(listBody(t, release("v1.4.1", true))))
		}))
		_, err := FetchFeed(context.Background(), Agentic, s.URL, testNow)
		s.Close()
		assert.Error(t, err, "%d", code)
	}
}

// ---- the cache keeps the channels apart ----

func TestTheTwoChannelsNeverShareACacheEntryAndAChangeNeedsNoFlush(t *testing.T) {
	var aHits, gHits atomic.Int32
	approved := feedServer(t, `{"tag_name":"v1.4.142","draft":false,"prerelease":false}`, &aHits, nil)
	agentic := feedServer(t, listBody(t, release("v1.4.143", false), release("v1.4.142", true)), &gHits, nil)
	feeds := Feeds{Approved: approved.URL, Agentic: agentic.URL}
	c := NewCache()
	ctx := context.Background()

	fa, err := c.Feed(ctx, Approved, feeds, false)
	require.NoError(t, err)
	assert.Equal(t, "1.4.142", fa.Offer.Version)
	fg, err := c.Feed(ctx, Agentic, feeds, false)
	require.NoError(t, err)
	assert.Equal(t, "1.4.143", fg.Offer.Version, "the other channel is answered by its own feed")
	for i := 0; i < 5; i++ {
		_, _ = c.Feed(ctx, Approved, feeds, false)
		_, _ = c.Feed(ctx, Agentic, feeds, false)
	}
	assert.EqualValues(t, 1, aHits.Load())
	assert.EqualValues(t, 1, gHits.Load())
}

func TestPeekFeedNeverAsksAndWarmFeedFetchesOnceInTheBackground(t *testing.T) {
	var hits atomic.Int32
	s := feedServer(t, listBody(t, release("v1.4.5", true)), &hits, nil)
	feeds := Feeds{Approved: s.URL, Agentic: s.URL}
	c := NewCache()
	_, _, ok := c.PeekFeed(Agentic, feeds)
	assert.False(t, ok)
	assert.EqualValues(t, 0, hits.Load(), "a peek makes no request")

	c.WarmFeed(Agentic, feeds)
	c.WarmFeed(Agentic, feeds)
	require.Eventually(t, func() bool { _, _, ok := c.PeekFeed(Agentic, feeds); return ok }, 5*time.Second, 10*time.Millisecond)
	f, err, ok := c.PeekFeed(Agentic, feeds)
	require.True(t, ok)
	require.NoError(t, err)
	assert.Equal(t, "1.4.5", f.Offer.Version)
	c.WarmFeed(Agentic, feeds)
	time.Sleep(50 * time.Millisecond)
	assert.EqualValues(t, 1, hits.Load(), "two warms and a warm of a fresh entry made one request")
}

func TestAFeedFailureIsKeptShortAndARefreshIsThrottled(t *testing.T) {
	var hits atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(http.StatusForbidden) }))
	defer s.Close()
	feeds := Feeds{Approved: s.URL, Agentic: s.URL}
	c := NewCache()
	now := testNow
	c.now = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, err := c.Feed(ctx, Agentic, feeds, false)
		assert.Error(t, err)
	}
	assert.EqualValues(t, 1, hits.Load())
	now = now.Add(CacheFail + time.Second)
	_, _ = c.Feed(ctx, Agentic, feeds, false)
	assert.EqualValues(t, 2, hits.Load(), "after five minutes a failure is asked again")
	now = now.Add(RefreshAfter / 2)
	_, _ = c.Feed(ctx, Agentic, feeds, true)
	assert.EqualValues(t, 2, hits.Load(), "a refresh under a minute after the last answer gets the cached one")
}
