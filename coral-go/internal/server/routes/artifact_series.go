package routes

// Artifact versions, stage 1 (#6357, #6406; the approved note, local routes only: nothing here is in the Hub's tunnel table). A series is an ordered list of artifacts (digests) with an id of
// its own (32 lowercase hex, never a digest): each version is a digest and always opens exactly its bytes; the series means "the newest version the caller can read". The facts live in the
// per-session upload records (series_id, version, previous: the sidecar is shared by every upload of the same bytes and never carries them); artifacts/series/<id>.json is an index of them
// and the records win wherever the two disagree. One lock per series, taken BEFORE a digest's lock everywhere.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/secfile"
)

// seriesVersionCap is how many versions one series may hold (a var so a test can lower it).
var seriesVersionCap = 100

const (
	// seriesHeader names the artifact an upload is a new version of: exactly a 64-hex digest link or a 32-hex series link.
	seriesHeader = "X-Artifact-New-Version-Of"
	// maxSeriesIndexBytes bounds an index file when it is read: 100 rows are a few KiB.
	maxSeriesIndexBytes = 64 << 10
)

var seriesIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// seriesLocks: one of 256 locks per series (its first byte), taken before any digest lock (lockArtifact) by every path that holds both, and after the create lock (seriesCreateLocks) a first version holds.
var seriesLocks [256]sync.Mutex

// seriesCreateLocks: one of 256 locks per artifact a first version is made of (its digest's first byte). Several first versions of one unversioned artifact have no series id yet to
// serialise on, so each made its own series (#6406, Security): the create lock is taken before the artifact's series is looked up and held until the version is committed. A separate array
// from seriesLocks (sharing one could deadlock on a bucket collision). Lock order: create, then series, then digest (lockArtifact).
var seriesCreateLocks [256]sync.Mutex

func lockSeriesCreate(digest string) func() {
	var i byte
	if len(digest) >= 2 {
		if b, err := hexByte(digest[0], digest[1]); err == nil {
			i = b
		}
	}
	seriesCreateLocks[i].Lock()
	return seriesCreateLocks[i].Unlock
}

func lockSeries(id string) func() {
	var i byte
	if len(id) >= 2 {
		if b, err := hexByte(id[0], id[1]); err == nil {
			i = b
		}
	}
	seriesLocks[i].Lock()
	return seriesLocks[i].Unlock
}

// seriesVersionRow is one version in the index: its digest, its number, the session whose record carries it and when it was added.
type seriesVersionRow struct {
	Digest  string `json:"digest"`
	N       int    `json:"n"`
	Session string `json:"session"`
	At      string `json:"at"`
}

// seriesIndex is artifacts/series/<id>.json: the owning board and the versions in the order they were added. A cache of the records, never the authority.
type seriesIndex struct {
	ID        string             `json:"id"`
	Board     string             `json:"board"`
	CreatedAt string             `json:"created_at"`
	Versions  []seriesVersionRow `json:"versions"`
}

// seriesPath is the index file of id, "" for anything that is not a 32-hex id (checked before the id is joined onto a path).
func seriesPath(coralDir, id string) string {
	if coralDir == "" || !seriesIDRe.MatchString(id) {
		return ""
	}
	return filepath.Join(coralDir, "artifacts", "series", id+".json")
}

func readSeriesIndex(coralDir, id string) (seriesIndex, bool) {
	p := seriesPath(coralDir, id)
	if p == "" {
		return seriesIndex{}, false
	}
	f, err := secfile.OpenNoFollow(p, os.O_RDONLY, 0) // the index is our own file: never read through a link
	if err != nil {
		return seriesIndex{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSeriesIndexBytes+1))
	if err != nil || len(data) > maxSeriesIndexBytes {
		return seriesIndex{}, false
	}
	var idx seriesIndex
	if json.Unmarshal(data, &idx) != nil || idx.ID != id {
		return seriesIndex{}, false
	}
	return idx, true
}

func writeSeriesIndex(coralDir string, idx seriesIndex) error {
	p := seriesPath(coralDir, idx.ID)
	if p == "" {
		return fmt.Errorf("not a series id")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	return writeFileAtomic(p, b, 0o600)
}

// newSeriesID is 128 random bits as 32 lowercase hex: it cannot be mistaken for a 64-hex digest.
func newSeriesID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// seriesRecord is an upload record that carries series fields, with the session it is in.
type seriesRecord struct {
	session string
	rec     agentUploadRecord
}

// uploadRecordWalks counts the walks of every upload record (a test pins that a read with a good index makes none).
var uploadRecordWalks atomic.Int64

// forEachUploadRecord calls fn with every session upload record in the store (uploads/<session>/<digest>.json) until fn returns false.
func forEachUploadRecord(coralDir string, fn func(session, digest string, rec agentUploadRecord) bool) {
	uploadRecordWalks.Add(1)
	sessions, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "uploads"))
	if err != nil {
		return
	}
	for _, s := range sessions {
		up := uploadsDir(coralDir, s.Name())
		if up == "" || !s.IsDir() {
			continue
		}
		files, err := os.ReadDir(up)
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if len(name) != 64+len(".json") || !strings.HasSuffix(name, ".json") {
				continue
			}
			digest := strings.TrimSuffix(name, ".json")
			if !objectNameRe.MatchString(digest) {
				continue
			}
			if rec, ok := readUploadRecord(coralDir, s.Name(), digest); ok && !fn(s.Name(), digest, rec) {
				return
			}
		}
	}
}

// recordsOfDigest are the upload records of one artifact, in session order.
func recordsOfDigest(coralDir, digest string) []seriesRecord {
	var out []seriesRecord
	sessions, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "uploads"))
	if err != nil || !objectNameRe.MatchString(digest) {
		return nil
	}
	for _, s := range sessions {
		if rec, ok := readUploadRecord(coralDir, s.Name(), digest); ok && s.IsDir() {
			out = append(out, seriesRecord{session: s.Name(), rec: rec})
		}
	}
	return out
}

// digestSeries says which series an artifact belongs to, from its records (the authority): the first record that names one.
func digestSeries(coralDir, digest string) (seriesID string, n int, ok bool) {
	for _, r := range recordsOfDigest(coralDir, digest) {
		if seriesIDRe.MatchString(r.rec.SeriesID) {
			return r.rec.SeriesID, r.rec.Version, true
		}
	}
	return "", 0, false
}

// seriesMember is a version as the records say it is.
type seriesMember struct {
	Digest   string
	N        int
	Session  string
	At       string
	Uploader string
	Role     string
	Board    string
}

// seriesMembers returns the versions of series id as their upload records say, in version order. The index is only where to look first: an entry whose record is gone or says
// another series or number is dropped (the records win), and a series whose index is missing or damaged is found by one walk of the records.
func seriesMembers(coralDir, id string) []seriesMember {
	return membersOfSeries(coralDir, id, false)
}

// membersOfSeries: a read (full false) trusts a good index (one file read per version, each checked against its record) and walks every upload record only when the index is missing or damaged
// or a row fails its record. A write (full true, under the series lock) always walks, so an upload that stopped before its index was written is found and the next commit heals the index.
func membersOfSeries(coralDir, id string, full bool) []seriesMember {
	if !seriesIDRe.MatchString(id) {
		return nil // never a walk for "records of the empty series", which every unversioned artifact would match
	}
	var out []seriesMember
	seen := map[string]bool{}
	idx, indexed := readSeriesIndex(coralDir, id)
	clean := indexed
	if indexed {
		for _, v := range idx.Versions {
			if rec, ok := readUploadRecord(coralDir, v.Session, v.Digest); ok && rec.SeriesID == id && rec.Version == v.N && !seen[v.Digest] {
				seen[v.Digest] = true
				out = append(out, memberOf(v.Digest, v.Session, rec))
			} else {
				clean = false
			}
		}
	}
	if full || !clean {
		// A record that names this series and is not in the index (a lost or stale index, an upload that stopped before the index was written) is a member too: records win.
		forEachUploadRecord(coralDir, func(session, digest string, rec agentUploadRecord) bool {
			if rec.SeriesID == id && !seen[digest] {
				seen[digest] = true
				out = append(out, memberOf(digest, session, rec))
			}
			return true
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	return out
}

func memberOf(digest, session string, rec agentUploadRecord) seriesMember {
	return seriesMember{Digest: digest, N: rec.Version, Session: session, At: rec.CreatedAt, Uploader: rec.Uploader, Role: rec.Role, Board: rec.Board}
}

// seriesBoard is the board that owns series id: the board of its first member's record (the index only says the same).
func seriesBoard(coralDir, id string) string {
	ms := seriesMembers(coralDir, id)
	if len(ms) == 0 {
		return ""
	}
	return ms[0].Board
}

// seriesMemberSet is every artifact that belongs to a series: they count as kept for pruning (pick 4, keep all: only an explicit delete removes a version).
func seriesMemberSet(coralDir string) map[string]bool {
	out := map[string]bool{}
	forEachUploadRecord(coralDir, func(_, digest string, rec agentUploadRecord) bool {
		if seriesIDRe.MatchString(rec.SeriesID) {
			out[digest] = true
		}
		return true
	})
	return out
}

// newVersionRef parses the header: exactly a 64-hex digest or a 32-hex series id, bare or as a coral://artifacts/ link (series links as coral://artifacts/series/<id>); anything else is refused
// before any lookup.
func newVersionRef(v string) (digest, series string, ok bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "coral://artifacts/")
	if s, isSeries := strings.CutPrefix(v, "series/"); isSeries {
		return "", s, seriesIDRe.MatchString(s)
	}
	switch {
	case objectNameRe.MatchString(v):
		return v, "", true
	case seriesIDRe.MatchString(v):
		return "", v, true
	}
	return "", "", false
}

// seriesStamp is what a version's own upload record carries.
type seriesStamp struct {
	ID       string
	Version  int
	Previous string
}

// seriesPlan is a new version worked out under the series lock, which the caller holds until it has stored the artifact and written the index: unlock ends it.
type seriesPlan struct {
	id      string
	board   string
	create  bool
	rows    []seriesMember // the versions before this one (for a new series: the artifact it is a version of)
	base    seriesRecord   // for a new series: the record that becomes version 1
	stamp   seriesStamp
	same    *seriesMember // set when the bytes are the newest version: nothing to add
	unlock  func()
	created string
}

type seriesRefusal struct {
	status int
	code   string
	msg    string
}

func (e *seriesRefusal) Error() string { return e.code + ": " + e.msg }

var (
	errSeriesNoBoard    = &seriesRefusal{http.StatusForbidden, "series_forbidden", "only an agent on a board can add a version"}
	errSeriesNotYours   = &seriesRefusal{http.StatusNotFound, "series_not_found", "that is not an artifact or a series this agent's board uploaded"}
	errSeriesOtherOwner = &seriesRefusal{http.StatusConflict, "series_other", "these bytes already belong to another series"} // names no series, id, board or team
)

func errSeriesFull() *seriesRefusal {
	return &seriesRefusal{http.StatusConflict, "series_full", fmt.Sprintf("this series already has %d versions", seriesVersionCap)}
}

// planNewVersion works out where an upload of the bytes newDigest goes as a new version of ref (a digest or a series id), for an agent on callerBoard. On success the series lock is held (plan.unlock).
func planNewVersion(coralDir, ref, callerBoard, newDigest string) (*seriesPlan, error) {
	digest, series, ok := newVersionRef(ref)
	if !ok {
		return nil, &seriesRefusal{http.StatusBadRequest, "bad_request", "new version of: give a 64-hex artifact link or a 32-hex series link"}
	}
	if callerBoard == "" {
		return nil, errSeriesNoBoard
	}
	p := &seriesPlan{board: callerBoard}
	var unlockCreate func() // a first version holds the create lock from before the artifact's series is looked up until the version is committed (or refused)
	if series != "" {
		p.id = series
	} else {
		unlockCreate = lockSeriesCreate(digest)
		defer func() {
			if unlockCreate != nil {
				unlockCreate()
			}
		}()
		// The artifact the caller names must be one its board uploaded (a record of it names the caller's board): records decide, never the index.
		recs := recordsOfDigest(coralDir, digest)
		for _, r := range recs {
			if r.rec.Board == callerBoard && p.base.session == "" {
				p.base = r
			}
		}
		if p.base.session == "" {
			return nil, errSeriesNotYours
		}
		if sid, _, in := digestSeries(coralDir, digest); in {
			p.id = sid
			unlockCreate() // it is in a series already: the series lock serialises from here, as for a series link
			unlockCreate = nil
		}
	}
	if p.id == "" {
		id, err := newSeriesID()
		if err != nil {
			return nil, err
		}
		p.id, p.create = id, true
	}
	unlockSeries := lockSeries(p.id)
	p.unlock = unlockSeries
	if unlockCreate != nil { // a new series: the create lock is let go after the series lock, when the version is committed
		held := unlockCreate
		unlockCreate = nil
		p.unlock = func() { unlockSeries(); held() }
	}
	fail := func(e error) (*seriesPlan, error) { p.unlock(); return nil, e }
	if !p.create {
		p.rows = membersOfSeries(coralDir, p.id, true)
		if len(p.rows) == 0 || p.rows[0].Board != callerBoard {
			return fail(errSeriesNotYours) // not found and not yours look the same
		}
		if len(p.rows) >= seriesVersionCap {
			return fail(errSeriesFull())
		}
		latest := p.rows[len(p.rows)-1]
		if latest.Digest == newDigest {
			p.same = &latest
			return p, nil
		}
		for _, m := range p.rows {
			if m.Digest == newDigest {
				return fail(&seriesRefusal{http.StatusConflict, "same_as_older_version", fmt.Sprintf("these bytes are version %d of this series, and the newest is version %d: nothing was added", m.N, latest.N)})
			}
		}
	} else if digest == newDigest {
		return fail(&seriesRefusal{http.StatusOK, "same_as_latest", "these bytes are the artifact it is a version of: nothing to add"})
	}
	// Bytes already in ANOTHER series are refused, in words that name nothing of it.
	if other, _, in := digestSeries(coralDir, newDigest); in && other != p.id {
		return fail(errSeriesOtherOwner)
	}
	next := len(p.rows) + 1
	prev := ""
	if p.create {
		next, prev = 2, digest
		p.rows = []seriesMember{memberOf(digest, p.base.session, p.base.rec)}
		p.rows[0].N = 1
	} else {
		prev = p.rows[len(p.rows)-1].Digest
		next = p.rows[len(p.rows)-1].N + 1
	}
	p.stamp = seriesStamp{ID: p.id, Version: next, Previous: prev}
	p.created = time.Now().UTC().Format(time.RFC3339)
	return p, nil
}

// commit records the new version after its object and upload record are stored: for a new series the artifact it is a version of becomes version 1, and the index is written. It is the
// last step under the series lock; a failure leaves the records, which win, to be found by the next read.
func (p *seriesPlan) commit(coralDir, newDigest, session string) (versions int, err error) {
	idx := seriesIndex{ID: p.id, Board: p.board, CreatedAt: p.created}
	if !p.create {
		if old, ok := readSeriesIndex(coralDir, p.id); ok {
			idx = old
		}
		// Rebuilt from the records the plan walked, so a version whose index row was never written is in it from now on.
		idx.Versions = nil
		for _, m := range p.rows {
			idx.Versions = append(idx.Versions, seriesVersionRow{Digest: m.Digest, N: m.N, Session: m.Session, At: m.At})
		}
	} else if err := stampRecord(coralDir, p.base.session, p.rows[0].Digest, seriesStamp{ID: p.id, Version: 1}); err != nil {
		return 0, err
	}
	if p.create {
		idx.Versions = []seriesVersionRow{{Digest: p.rows[0].Digest, N: 1, Session: p.base.session, At: p.base.rec.CreatedAt}}
	}
	idx.Versions = append(idx.Versions, seriesVersionRow{Digest: newDigest, N: p.stamp.Version, Session: session, At: p.created})
	return len(idx.Versions), writeSeriesIndex(coralDir, idx)
}

// stampRecord sets the series fields on one session's record of an artifact, under the artifact's lock (the series lock is already held: series first, digest second).
func stampRecord(coralDir, session, digest string, s seriesStamp) error {
	defer lockArtifact(digest)()
	rec, ok := readUploadRecord(coralDir, session, digest)
	if !ok {
		return fmt.Errorf("no upload record to stamp")
	}
	rec.SeriesID, rec.Version, rec.Previous = s.ID, s.Version, s.Previous
	up := uploadsDir(coralDir, session)
	if up == "" {
		return fmt.Errorf("no upload folder")
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(up, digest+".json"), b, 0o600)
}

// forgetVersion removes one version from its series' index after the artifact was deleted: the others keep their numbers (the bar says "v2 removed"), and an empty series disappears.
func forgetVersion(coralDir, id, digest string) {
	idx, ok := readSeriesIndex(coralDir, id)
	if !ok {
		return
	}
	var rest []seriesVersionRow
	for _, v := range idx.Versions {
		if v.Digest != digest {
			rest = append(rest, v)
		}
	}
	if len(rest) == 0 {
		if p := seriesPath(coralDir, id); p != "" {
			_ = os.Remove(p)
		}
		return
	}
	idx.Versions = rest
	_ = writeSeriesIndex(coralDir, idx)
}
