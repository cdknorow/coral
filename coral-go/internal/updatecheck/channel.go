package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// The release channels (#5557, design #5551): APPROVED is the release marked latest (what every earlier Coral reads); AGENTIC is every published
// release, approved or not. A release is unapproved while it is a pre-release; approving it flips that flag and marks it latest.
//
// The channel only chooses between two feed URLs that are compiled in here: no setting value, no field of a reply ever becomes part of a URL. It is
// for the NOTICE (the dashboard's toast, the tray, the Updates pane): nothing here reaches the updater, and no URL of a release asset is read or kept.
type Channel string

const (
	Approved Channel = "approved"
	Agentic  Channel = "agentic"
)

// ParseChannel reads a stored channel. Anything but the word agentic is Approved: a value nobody can explain never widens what is offered.
func ParseChannel(s string) Channel {
	if strings.EqualFold(strings.TrimSpace(s), string(Agentic)) {
		return Agentic
	}
	return Approved
}

const (
	// RepoSlug and RepoID name the repository the AGENTIC list is read from. The list is asked by the repository's numeric id, which survives a transfer and is
	// never reissued, and every release in the reply must carry a page URL of RepoSlug: a re-created name or a reply about another repository offers nothing.
	RepoSlug = "subgentic/coral"
	RepoID   = "1160696364"
	// ReleasesListAPI is the AGENTIC feed: the ten newest releases, drafts included only for a caller that may see them (this one is anonymous, so none).
	ReleasesListAPI = "https://api.github.com/repositories/" + RepoID + "/releases?per_page=10"
	// MaxListBody caps the list reply: ten releases carry their notes, a few KB each. Over it is an error, never a partial answer.
	MaxListBody = 1 << 20
	// MaxNotes is the most of a release's notes (in characters) that is kept: the notes are text written by an agent and shown as plain text.
	MaxNotes = 2000
	// MaxWaiting is the most releases listed as waiting for approval.
	MaxWaiting = 5

	tagPagePrefix = "https://github.com/" + RepoSlug + "/releases/tag/"
)

// stableTag is a plain release tag: v and three numbers, no suffix. A tag with any suffix (-rc, -beta, -dev, -forDropbox) is never offered on the
// Agentic channel, whatever its pre-release flag says.
var stableTag = regexp.MustCompile(`^v(\d{1,9})\.(\d{1,9})\.(\d{1,9})$`)

// ErrNoRelease is a list with no plain release in it: an error, never "up to date".
var ErrNoRelease = errors.New("updatecheck: the release list holds no plain release")

// Release is one release as the notice needs it.
type Release struct {
	Version     string    // without the "v"
	Prerelease  bool      // true: not approved
	PublishedAt time.Time // zero when unknown
	// ApprovedAt is read from the "Approved: <time>" line the approve job writes as the last line of the notes. It is for DISPLAY only: whether a release
	// is approved comes from the pre-release flag and releases/latest, never from this line (an agent writes the rest of the notes). Zero when absent or
	// not plausible.
	ApprovedAt time.Time
	Notes      string // plain text, at most MaxNotes characters
}

// Feed is what one check of a channel learned.
type Feed struct {
	Channel Channel
	// Offer is the release this channel offers: Approved, the one marked latest; Agentic, the highest plain version published.
	Offer Release
	// Approved is the newest approved release: on Approved the same as Offer; on Agentic the highest plain release that is not a pre-release (nil when
	// the ten newest hold none).
	Approved *Release
	// Waiting is, on Agentic only, the unapproved releases NEWER than Approved, newest first, at most MaxWaiting. An unapproved release older than the
	// newest approved one is superseded, not waiting.
	Waiting []Release
}

type apiRelease struct {
	TagName     string `json:"tag_name"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	HTMLURL     string `json:"html_url"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
}

func (a apiRelease) release(now time.Time) Release {
	r := Release{Version: strings.TrimPrefix(a.TagName, "v"), Prerelease: a.Prerelease, Notes: cleanNotes(a.Body)}
	if t, err := time.Parse(time.RFC3339, a.PublishedAt); err == nil {
		r.PublishedAt = t.UTC()
	}
	if !a.Prerelease {
		r.ApprovedAt = approvedAt(a.Body, r.PublishedAt, now)
	}
	return r
}

// cleanNotes makes a release's notes safe to show as plain text: invalid UTF-8 and control or format characters (a right-to-left override, a zero-width
// joiner) are dropped, line breaks and tabs kept, and the result cut at MaxNotes characters.
func cleanNotes(s string) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	n := 0
	for _, r := range s {
		switch {
		case r == '\r':
			continue
		case r == '\n' || r == '\t':
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			continue
		}
		if n == MaxNotes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

var approvedLine = regexp.MustCompile(`^Approved: (\S.*)$`)

var approvedLayouts = []string{time.RFC3339, "2006-01-02T15:04Z", "2006-01-02 15:04:05 UTC", "2006-01-02 15:04 UTC", "2006-01-02 15:04:05Z"}

// approvedAt reads the approval time from the LAST non-empty line of the notes, and only when it is plausible: not after now plus an hour and not before
// the release was published. It is shown, never trusted (see Release.ApprovedAt).
func approvedAt(body string, published, now time.Time) time.Time {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(body, "\r", ""), " \t\n"), "\n")
	m := approvedLine.FindStringSubmatch(strings.TrimSpace(lines[len(lines)-1]))
	if m == nil {
		return time.Time{}
	}
	for _, layout := range approvedLayouts {
		if t, err := time.Parse(layout, strings.TrimSpace(m[1])); err == nil {
			t = t.UTC()
			if t.After(now.Add(time.Hour)) || (!published.IsZero() && t.Before(published.Add(-time.Minute))) {
				return time.Time{}
			}
			return t
		}
	}
	return time.Time{}
}

// parseLatest reads the Approved feed's reply (one release object).
func parseLatest(r io.Reader, now time.Time) (*Feed, error) {
	var a apiRelease
	if err := json.NewDecoder(io.LimitReader(r, MaxBody)).Decode(&a); err != nil {
		return nil, fmt.Errorf("updatecheck: %w", err)
	}
	if !tagShape.MatchString(a.TagName) {
		return nil, ErrBadTag
	}
	if a.Draft || a.Prerelease {
		return nil, errors.New("updatecheck: the latest release is not an approved release")
	}
	rel := a.release(now)
	return &Feed{Channel: Approved, Offer: rel, Approved: &rel}, nil
}

// parseList reads the Agentic feed's reply (an array of releases).
func parseList(r io.Reader, now time.Time) (*Feed, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxListBody+1))
	if err != nil {
		return nil, fmt.Errorf("updatecheck: %w", err)
	}
	if len(raw) > MaxListBody {
		return nil, fmt.Errorf("updatecheck: the release list is larger than %d bytes", MaxListBody)
	}
	var list []apiRelease
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("updatecheck: %w", err)
	}
	type entry struct {
		v   version
		rel Release
	}
	var all, approved []entry
	seen := map[string]bool{}
	for _, a := range list {
		if a.Draft || !stableTag.MatchString(a.TagName) || a.HTMLURL != tagPagePrefix+a.TagName || seen[a.TagName] {
			continue
		}
		seen[a.TagName] = true
		v, ok := parse(a.TagName)
		if !ok {
			continue
		}
		e := entry{v: v, rel: a.release(now)}
		all = append(all, e)
		if !a.Prerelease {
			approved = append(approved, e)
		}
	}
	if len(all) == 0 {
		return nil, ErrNoRelease
	}
	byVersionDesc := func(es []entry) { sort.SliceStable(es, func(i, j int) bool { return compare(es[i].v, es[j].v) > 0 }) }
	byVersionDesc(all)
	byVersionDesc(approved)
	f := &Feed{Channel: Agentic, Offer: all[0].rel}
	var newestApproved *version
	if len(approved) > 0 {
		rel := approved[0].rel
		f.Approved = &rel
		newestApproved = &approved[0].v
	}
	for _, e := range all {
		if e.rel.Prerelease && (newestApproved == nil || compare(e.v, *newestApproved) > 0) && len(f.Waiting) < MaxWaiting {
			f.Waiting = append(f.Waiting, e.rel)
		}
	}
	return f, nil
}

// Feeds are the two feed URLs: production's are the constants above; a test names its own server.
type Feeds struct{ Approved, Agentic string }

// DefaultFeeds is what the program asks.
var DefaultFeeds = Feeds{Approved: ReleasesAPI, Agentic: ReleasesListAPI}

func (f Feeds) url(ch Channel) string {
	if ch == Agentic {
		return f.Agentic
	}
	return f.Approved
}

// FetchFeed asks one channel's feed, anonymously, with the same client, limits and redirect rule as Latest.
func FetchFeed(ctx context.Context, ch Channel, apiURL string, now time.Time) (*Feed, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("updatecheck: GitHub answered %d", resp.StatusCode)
	}
	if ch == Agentic {
		return parseList(resp.Body, now)
	}
	return parseLatest(resp.Body, now)
}

// ValidVersion reports whether s is a version a notice may show: three numbers and an optional suffix, with or without the "v".
func ValidVersion(s string) bool { return tagShape.MatchString(s) }

// ReleasePageURL is the page the notice links to: the releases page for Approved, the release's own page for Agentic (built from the compiled-in
// repository and a version that passed ValidVersion, never from a field of the reply).
func ReleasePageURL(ch Channel, version string) string {
	if ch == Agentic && ValidVersion(version) {
		return ReleasesURL + "/tag/v" + strings.TrimPrefix(version, "v")
	}
	return ReleasesURL
}
