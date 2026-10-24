package routes

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	"github.com/subgentic/coral/internal/cloud"
)

// Artifact retention and pruning (#3554, design note v3). What can go:
//   - diffs: a task's changes.diff once the task is completed or skipped, and a session's once
//     the session is stopped (never a running or sleeping one), older than the threshold;
//   - objects: an artifact object no post, task, note, panel, workflow or knowledge file links,
//     that isn't kept, and whose newest upload is older than the threshold;
//   - diff-objects: the shared copies behind diffs (#3555) that no diff links any more.
//
// Shrink rewrites closed diffs through #3555's filter instead (the text changes stay). Nothing
// is removed unless the Operator runs it (or turns on retention); a reference scan that fails
// removes nothing.
const (
	pruneKindDiffs       = "diffs"
	pruneKindObjects     = "objects"
	pruneKindDiffObjects = "diff-objects"

	pruneScanTimeout   = 30 * time.Second
	pruneRunTimeout    = 5 * time.Minute
	pruneLargest       = 20
	pruneMinOlderThan  = 24 * time.Hour // a prune of anything younger is refused (a typo guard)
	diffObjectGrace    = time.Hour      // a diff object this young may be about to be linked
	pruneDiffReadLimit = 512 << 20      // a stored diff bigger than this isn't shrunk
)

// artifactRefRe finds an artifact id in any text: coral://artifacts/<hex>, /api/artifacts/<hex>
// (with or without ?v=2), inside Markdown, code or angle brackets alike.
var artifactRefRe = regexp.MustCompile(`(?i)(?:coral://artifacts/|/api/artifacts/)([0-9a-f]{64})`)

// A long object prune scans the references again every pruneRescanEvery deletions, and at least
// every pruneRescanAfter (Security's #3554 review). Vars so a test can make them small.
var (
	pruneRescanEvery = 100
	pruneRescanAfter = 30 * time.Second
)

// pruneAfterDeleteHook lets a test post a link after each object deletion.
var pruneAfterDeleteHook func()

// pruneRunning: one prune or shrink at a time.
var pruneRunning sync.Mutex

type pruneOptions struct {
	OlderThan time.Duration
	Kinds     map[string]bool
	DryRun    bool
	Shrink    bool
	MaxBytes  int64 // stop once this much is freed (retention's per-run cap); 0: no cap
	Now       time.Time
}

type pruneItem struct {
	Kind    string  `json:"kind"`
	ID      string  `json:"id"`
	Name    string  `json:"name,omitempty"`
	Board   string  `json:"board,omitempty"`
	Bytes   int64   `json:"bytes"`
	AgeDays float64 `json:"age_days"`
}

type pruneTotal struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

// pruneReport is what a prune or shrink did, or (dry run) would do.
type pruneReport struct {
	DryRun           bool                   `json:"dry_run"`
	Shrink           bool                   `json:"shrink"`
	OlderThanSeconds int64                  `json:"older_than_seconds"`
	Kinds            map[string]*pruneTotal `json:"kinds"`
	ByBoard          map[string]int64       `json:"by_board"`
	Largest          []pruneItem            `json:"largest"`
	Freed            int64                  `json:"freed"`   // bytes freed on disk (would be, for a dry run)
	Removed          int                    `json:"removed"` // items removed or rewritten
	Skipped          int                    `json:"skipped"` // changed since the scan: left alone
	Errors           int                    `json:"errors"`
	Capped           bool                   `json:"capped,omitempty"`
	ScanMillis       int64                  `json:"scan_ms"`
}

func newPruneReport(o pruneOptions) *pruneReport {
	return &pruneReport{DryRun: o.DryRun, Shrink: o.Shrink, OlderThanSeconds: int64(o.OlderThan / time.Second),
		Kinds: map[string]*pruneTotal{}, ByBoard: map[string]int64{}}
}

func (r *pruneReport) add(it pruneItem) {
	t := r.Kinds[it.Kind]
	if t == nil {
		t = &pruneTotal{}
		r.Kinds[it.Kind] = t
	}
	t.Count++
	t.Bytes += it.Bytes
	board := it.Board
	if board == "" {
		board = "(none)"
	}
	r.ByBoard[board] += it.Bytes
	r.Largest = append(r.Largest, it)
}

func (r *pruneReport) finish() {
	sort.SliceStable(r.Largest, func(i, j int) bool { return r.Largest[i].Bytes > r.Largest[j].Bytes })
	if len(r.Largest) > pruneLargest {
		r.Largest = r.Largest[:pruneLargest]
	}
}

// ── References ──────────────────────────────────────────────────────

// The reference scan reads EVERY column of every table in both databases, whatever its declared
// type (deny by default, Lead's and the Orchestrator's #3554 reviews: SQLite types are only
// declarations, so a BLOB or INTEGER column can hold text too). A table or column added later is
// scanned without anyone remembering to list it. A table is skipped only when it is listed here,
// with the reason.
var referenceExclusions = map[string]string{
	"call_metrics":        "a request log: a GET of /api/artifacts/<hex> there is a fetch, not a link (and it is by far the slowest table to scan)",
	"call_metrics_hourly": "call_metrics rolled up by hour (#4098): route patterns and counts, never a link",
	"session_fts_data":    "session_fts's own index storage (binary); session_fts itself is scanned",
	"session_fts_idx":     "session_fts's own index storage; session_fts itself is scanned",
	"session_fts_content": "the same transcript text as session_fts, which is scanned",
	"session_fts_docsize": "session_fts's own index storage (sizes)",
	"session_fts_config":  "session_fts's own settings",
}

// scanArtifactReferences returns every artifact id something durable links. Any error fails the
// whole scan: a prune must never delete "what it could verify".
func (h *SessionsHandler) scanArtifactReferences(ctx context.Context) (map[string]bool, error) {
	if h.bs == nil || h.db == nil {
		return nil, errors.New("the board or session database isn't available")
	}
	ctx, cancel := context.WithTimeout(ctx, pruneScanTimeout)
	defer cancel()
	refs := map[string]bool{}
	if err := scanAllColumns(ctx, h.bs.Reader(), refs); err != nil {
		return nil, fmt.Errorf("board database: %w", err)
	}
	if err := scanAllColumns(ctx, h.db.Reader(), refs); err != nil {
		return nil, fmt.Errorf("session database: %w", err)
	}
	if err := scanKnowledgeReferences(ctx, filepath.Join(h.cfg.CoralDir(), "agent_knowledge"), refs); err != nil {
		return nil, fmt.Errorf("knowledge files: %w", err)
	}
	if err := scanCrossTeamRecordRefs(h.cfg.CoralDir(), refs); err != nil {
		return nil, fmt.Errorf("shared and received rows: %w", err)
	}
	return refs, nil
}

// scannedColumns lists, per table, the columns the scan reads in db (all of them).
func scannedColumns(ctx context.Context, db *sqlx.DB) (map[string][]string, error) {
	var tables []string
	if err := db.SelectContext(ctx, &tables, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name"); err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, t := range tables {
		if _, skip := referenceExclusions[t]; skip {
			continue
		}
		rows, err := db.QueryxContext(ctx, "SELECT name FROM pragma_table_info(?)", t)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return nil, err
			}
			out[t] = append(out[t], name)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// scanAllColumns reads every column of every table (one query per table) for links.
func scanAllColumns(ctx context.Context, db *sqlx.DB, refs map[string]bool) error {
	cols, err := scannedColumns(ctx, db)
	if err != nil {
		return err
	}
	for table, names := range cols {
		sel := make([]string, len(names))
		where := make([]string, len(names))
		for i, n := range names {
			sel[i] = "CAST(" + quoteIdent(n) + " AS BLOB)"
			where[i] = quoteIdent(n) + " LIKE '%artifacts/%'"
		}
		// Names come from this database's own schema, quoted; never from a request.
		rows, err := db.QueryContext(ctx, "SELECT "+strings.Join(sel, ", ")+" FROM "+quoteIdent(table)+" WHERE "+strings.Join(where, " OR "))
		if err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
		vals := make([][]byte, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return fmt.Errorf("%s: %w", table, err)
			}
			for _, v := range vals {
				addArtifactRefs(v, refs)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
	}
	return nil
}

func addArtifactRefs(text []byte, refs map[string]bool) {
	for _, m := range artifactRefRe.FindAllSubmatch(text, -1) {
		refs[strings.ToLower(string(m[1]))] = true
	}
}

// addArtifactRefsFrom scans a reader in chunks, keeping an overlap longer than any link so one
// split across two chunks is still found (Security's #3554 review: a big file must not be skipped).
func addArtifactRefsFrom(ctx context.Context, r io.Reader, refs map[string]bool) error {
	const chunk, overlap = 1 << 20, 128
	buf := make([]byte, 0, chunk+overlap)
	tmp := make([]byte, chunk)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if len(buf) > overlap || err != nil {
			addArtifactRefs(buf, refs)
			if len(buf) > overlap {
				buf = append(buf[:0], buf[len(buf)-overlap:]...)
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func scanKnowledgeReferences(ctx context.Context, dir string, refs map[string]bool) error {
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err // unreadable: the scan can't vouch for it, so the run removes nothing
		}
		defer f.Close()
		if err := addArtifactRefsFrom(ctx, f, refs); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// scanCrossTeamRecordRefs adds every artifact a board's shared or received row lists (#4379): an
// artifact a board lists is linked, whatever its message says now.
func scanCrossTeamRecordRefs(coralDir string, refs map[string]bool) error {
	for _, kind := range crossTeamRecordKinds {
		root := filepath.Join(coralDir, "artifacts", kind)
		boards, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err // unreadable: the scan can't vouch for it, so the run removes nothing
		}
		for _, b := range boards {
			if !b.IsDir() {
				continue
			}
			entries, err := os.ReadDir(filepath.Join(root, b.Name()))
			if err != nil {
				return err
			}
			for _, e := range entries {
				if id := strings.TrimSuffix(e.Name(), ".json"); id != e.Name() && managedArtifactID("coral://artifacts/"+id) == id {
					refs[id] = true
				}
			}
		}
	}
	return nil
}

// ── Kept artifacts ──────────────────────────────────────────────────

func (h *SessionsHandler) keptArtifacts(ctx context.Context) (map[string]bool, error) {
	kept := map[string]bool{}
	if h.db == nil {
		return kept, nil
	}
	var ids []string
	if err := h.db.Reader().SelectContext(ctx, &ids, "SELECT hex FROM artifact_keep"); err != nil {
		return nil, err
	}
	for _, id := range ids {
		kept[id] = true
	}
	return kept, nil
}

// protectedArtifacts is what pruning must not remove: the artifacts the Operator keeps, and every member of an artifact series (#6357, pick 4: keep all; a version goes only by the explicit
// delete). The Operator's own list of what it keeps (keptListRoute) stays only what it marked.
func (h *SessionsHandler) protectedArtifacts(ctx context.Context) (map[string]bool, error) {
	kept, err := h.keptArtifacts(ctx)
	if err != nil {
		return nil, err
	}
	for id := range seriesMemberSet(h.cfg.CoralDir()) {
		kept[id] = true
	}
	return kept, nil
}

// isProtected is isKept for a prune's re-check under the artifact's lock: kept by the Operator, or a member of a series.
func (h *SessionsHandler) isProtected(ctx context.Context, id string) (bool, error) {
	if kept, err := h.isKept(ctx, id); err != nil || kept {
		return kept, err
	}
	_, _, inSeries := digestSeries(h.cfg.CoralDir(), id)
	return inSeries, nil
}

func (h *SessionsHandler) isKept(ctx context.Context, id string) (bool, error) {
	var n int
	err := h.db.Reader().GetContext(ctx, &n, "SELECT COUNT(*) FROM artifact_keep WHERE hex = ?", id)
	return n > 0, err
}

// ── Candidates ──────────────────────────────────────────────────────

// uploadRecordTimes: each artifact's newest upload record (uploads/<session>/<hex>.json).
func uploadRecordTimes(coralDir string) map[string]time.Time {
	out := map[string]time.Time{}
	sessions, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "uploads"))
	if err != nil {
		return out
	}
	for _, s := range sessions {
		up := uploadsDir(coralDir, s.Name())
		if up == "" || !s.IsDir() {
			continue
		}
		recs, err := os.ReadDir(up)
		if err != nil {
			continue
		}
		for _, f := range recs {
			id := strings.TrimSuffix(f.Name(), ".json")
			if !objectNameRe.MatchString(id) || !f.Type().IsRegular() {
				continue
			}
			t := recordTime(filepath.Join(up, f.Name()))
			if t.After(out[id]) {
				out[id] = t
			}
		}
	}
	return out
}

func recordTime(path string) time.Time {
	var rec struct {
		CreatedAt string `json:"created_at"`
	}
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &rec) == nil {
		if t, err := time.Parse(time.RFC3339, rec.CreatedAt); err == nil {
			return t
		}
	}
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// objectNewestUse: an object's newest upload record, else its file time.
func objectNewestUse(coralDir, id string, records map[string]time.Time) time.Time {
	if !objectNameRe.MatchString(id) { // an id read from a file or a message is a 64-digit hex name before it is joined onto a folder (#5305)
		return time.Time{}
	}
	if t, ok := records[id]; ok {
		return t
	}
	if fi, err := os.Stat(filepath.Join(coralDir, "artifacts", "objects", id)); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

type objectCandidate struct {
	id    string
	bytes int64
	name  string
	use   time.Time
}

func (h *SessionsHandler) objectCandidates(ctx context.Context, o pruneOptions, refs, kept map[string]bool) ([]objectCandidate, error) {
	coralDir := h.cfg.CoralDir()
	entries, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "objects"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	records := uploadRecordTimes(coralDir)
	var out []objectCandidate
	for _, e := range entries {
		id := e.Name()
		if !objectNameRe.MatchString(id) || !e.Type().IsRegular() || refs[id] || kept[id] {
			continue
		}
		use := objectNewestUse(coralDir, id, records)
		if o.Now.Sub(use) < o.OlderThan {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		name := ""
		if meta, ok := readSidecar(filepath.Join(coralDir, "artifacts", "objects", id+".json")); ok {
			name = meta.Name
		}
		out = append(out, objectCandidate{id: id, bytes: info.Size(), name: name, use: use})
	}
	return out, nil
}

type diffCandidate struct {
	kind     string // "task" or "session"
	id       string
	path     string
	board    string
	boardKey string // a task diff's board folder ("" for the older layout, by id alone)
	bytes    int64
	links    uint64
	inode    fileKey
	at       time.Time // when it was closed: the task's completed_at, the session's stopped_at
}

// taskDiffDir is one task's folder of stored diffs: its id, where it is, and the board key it is kept under ("" for the older layout, by id alone, which nothing serves any more but the prune still ages out).
type taskDiffDir struct {
	id, path, boardKey string
}

// taskDiffDirs lists the task folders under artifacts/tasks: <board key>/<id> (#6948) and the older <id> folders.
func taskDiffDirs(taskDir string) []taskDiffDir {
	var out []taskDiffDir
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.ParseInt(e.Name(), 10, 64); err == nil {
			out = append(out, taskDiffDir{id: e.Name(), path: filepath.Join(taskDir, e.Name())})
			continue
		}
		inner, err := os.ReadDir(filepath.Join(taskDir, e.Name()))
		if err != nil {
			continue
		}
		for _, i := range inner {
			if i.IsDir() {
				out = append(out, taskDiffDir{id: i.Name(), path: filepath.Join(taskDir, e.Name(), i.Name()), boardKey: e.Name()})
			}
		}
	}
	return out
}

// closedDiffs lists the stored task and session diffs whose task is completed or skipped, or
// whose session is stopped; olderThan 0 lists all of them (shrink).
func (h *SessionsHandler) closedDiffs(ctx context.Context, o pruneOptions) ([]diffCandidate, error) {
	coralDir := h.cfg.CoralDir()
	var out []diffCandidate

	type taskRow struct {
		ID          int64   `db:"id"`
		Status      string  `db:"status"`
		BoardID     string  `db:"board_id"`
		CompletedAt *string `db:"completed_at"`
	}
	tasks := map[string]taskRow{}
	if h.bs != nil {
		var rows []taskRow
		if err := h.bs.Reader().SelectContext(ctx, &rows, "SELECT id, status, board_id, completed_at FROM board_tasks"); err != nil {
			return nil, err
		}
		for _, t := range rows {
			tasks[strconv.FormatInt(t.ID, 10)] = t
		}
	}
	taskDir := filepath.Join(coralDir, "artifacts", "tasks")
	for _, d := range taskDiffDirs(taskDir) {
		t, known := tasks[d.id]
		if known && d.boardKey != "" && TaskBoardKey(t.BoardID) != d.boardKey {
			t, known = taskRow{}, false // another board's task of the same id (ids start again after a restore): not this one's
		}
		if known && t.Status != "completed" && t.Status != "skipped" {
			continue // an open task: its diff may still change
		}
		c, ok := diffAt(filepath.Join(d.path, "changes.diff"))
		if !ok {
			continue
		}
		c.kind, c.id, c.board, c.boardKey = "task", d.id, t.BoardID, d.boardKey
		if known {
			// Aged by when the task was closed (a shared diff's file time is when it was first
			// stored). No readable completed_at: too young to judge, so it stays.
			at, ok := closedAt(t.CompletedAt)
			if !ok {
				continue
			}
			c.at = at
		}
		if o.Now.Sub(c.at) >= o.OlderThan {
			out = append(out, c)
		}
	}

	type sessRow struct {
		SessionID string  `db:"session_id"`
		Status    *string `db:"status"`
		Sleeping  int     `db:"is_sleeping"`
		StoppedAt *string `db:"stopped_at"`
		Board     *string `db:"board_name"`
	}
	sessions := map[string]sessRow{}
	if h.db != nil {
		var rows []sessRow
		if err := h.db.Reader().SelectContext(ctx, &rows, "SELECT session_id, status, is_sleeping, stopped_at, board_name FROM live_sessions"); err != nil {
			return nil, err
		}
		for _, s := range rows {
			sessions[s.SessionID] = s
		}
	}
	sessDir := filepath.Join(coralDir, "artifacts", "sessions")
	if entries, err := os.ReadDir(sessDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			s, known := sessions[e.Name()]
			if known && (s.Sleeping != 0 || s.Status == nil || *s.Status != "inactive") {
				continue // running or sleeping: it can be resumed
			}
			c, ok := diffAt(filepath.Join(sessDir, e.Name(), "changes.diff"))
			if !ok {
				continue
			}
			c.kind, c.id = "session", e.Name()
			if known && s.Board != nil {
				c.board = *s.Board
			}
			if known {
				at, ok := closedAt(s.StoppedAt)
				if !ok {
					continue // no readable stopped_at: too young to judge
				}
				c.at = at
			}
			if o.Now.Sub(c.at) >= o.OlderThan {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

func closedAt(s *string) (time.Time, bool) {
	if s == nil || strings.TrimSpace(*s) == "" {
		return time.Time{}, false
	}
	at, err := parseDBTime(*s)
	return at, err == nil
}

func diffAt(path string) (diffCandidate, bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return diffCandidate{}, false
	}
	return diffCandidate{path: path, bytes: fi.Size(), links: fileLinks(path, fi), inode: identityOf(path, fi), at: fi.ModTime()}, true
}

// fileKey identifies a stored file across its hard links: (device, inode), or its path where
// the system gives no identity (each file is then its own).
type fileKey = string

func identityOf(path string, fi os.FileInfo) fileKey {
	if key, ok := fileIdentity(path, fi); ok {
		return key
	}
	return "path:" + path
}

var sqlNoRows = sql.ErrNoRows

func parseDBTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparsed time %q", s)
}

// diffObjectInodes: the inodes of artifacts/diff-objects, so a diff removal knows whether its
// bytes stay behind in a shared object (freed only once the sweep removes it).
func diffObjectInodes(coralDir string) map[fileKey]bool {
	out := map[fileKey]bool{}
	entries, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "diff-objects"))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() {
			out[identityOf(filepath.Join(coralDir, "artifacts", "diff-objects", e.Name()), fi)] = true
		}
	}
	return out
}

// ── The run ─────────────────────────────────────────────────────────

// runPrune prunes or shrinks as o says. A reference scan that fails fails the run before
// anything is removed.
func (h *SessionsHandler) runPrune(ctx context.Context, o pruneOptions) (*pruneReport, error) {
	if !pruneRunning.TryLock() {
		return nil, errPruneBusy
	}
	defer pruneRunning.Unlock()
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	rep := newPruneReport(o)
	coralDir := h.cfg.CoralDir()
	capped := func() bool {
		if o.MaxBytes > 0 && rep.Freed >= o.MaxBytes {
			rep.Capped = true
			return true
		}
		return false
	}

	if o.Shrink {
		diffs, err := h.closedDiffs(ctx, o)
		if err != nil {
			return nil, err
		}
		h.shrinkDiffs(ctx, o, diffs, rep)
		if !o.DryRun {
			h.sweepDiffObjects(o, rep, capped)
			invalidateStorageDetail()
		}
		rep.finish()
		return rep, nil
	}

	if o.Kinds[pruneKindDiffs] {
		diffs, err := h.closedDiffs(ctx, o)
		if err != nil {
			return nil, err
		}
		h.pruneDiffs(ctx, o, diffs, rep, capped)
	}

	if o.Kinds[pruneKindObjects] && !capped() {
		start := time.Now()
		refs, err := h.scanArtifactReferences(ctx)
		rep.ScanMillis = time.Since(start).Milliseconds()
		if err != nil {
			slog.Warn("[artifacts] prune: the reference scan failed, so no artifact object is removed", "error", err)
			return nil, fmt.Errorf("the reference scan failed (nothing removed): %w", err)
		}
		kept, err := h.protectedArtifacts(ctx)
		if err != nil {
			return nil, fmt.Errorf("could not read kept artifacts (nothing removed): %w", err)
		}
		cands, err := h.objectCandidates(ctx, o, refs, kept)
		if err != nil {
			return nil, err
		}
		if pruneAfterCandidatesHook != nil {
			pruneAfterCandidatesHook()
		}
		if !o.DryRun && len(cands) > 0 {
			// A link posted since the first scan keeps its object: scan again right before
			// deleting, then re-check each candidate under its digest lock.
			if refs, err = h.scanArtifactReferences(ctx); err != nil {
				slog.Warn("[artifacts] prune: the reference re-scan failed, so no artifact object is removed", "error", err)
				return nil, fmt.Errorf("the reference re-scan failed (nothing removed): %w", err)
			}
		}
		scanAt := time.Now()
		sinceScan := 0
		for _, c := range cands {
			if capped() {
				break
			}
			// A long run looks again every 100 deletions or 30 s, so a link posted meanwhile is seen.
			if !o.DryRun && (sinceScan >= pruneRescanEvery || time.Since(scanAt) >= pruneRescanAfter) {
				if refs, err = h.scanArtifactReferences(ctx); err != nil {
					slog.Warn("[artifacts] prune: a reference re-scan failed, so the run stops here", "error", err)
					rep.Errors++
					break
				}
				scanAt, sinceScan = time.Now(), 0
			}
			it := pruneItem{Kind: pruneKindObjects, ID: c.id, Name: c.name, Bytes: c.bytes, AgeDays: o.Now.Sub(c.use).Hours() / 24}
			if o.DryRun {
				rep.add(it)
				rep.Freed += c.bytes
				continue
			}
			freed, ok, err := h.deleteObjectIfStillUnused(ctx, coralDir, c.id, o, refs, scanAt)
			switch {
			case err != nil:
				rep.Errors++
			case !ok:
				rep.Skipped++
			default:
				rep.add(it)
				rep.Freed += freed
				rep.Removed++
				sinceScan++
				slog.Info("[artifacts] pruned an artifact object", "digest", c.id, "bytes", freed)
				if pruneAfterDeleteHook != nil {
					pruneAfterDeleteHook()
				}
			}
		}
	}

	if (o.Kinds[pruneKindDiffObjects] || o.Kinds[pruneKindDiffs]) && !o.DryRun {
		h.sweepDiffObjects(o, rep, capped)
	} else if o.Kinds[pruneKindDiffObjects] && o.DryRun {
		h.sweepDiffObjects(o, rep, capped) // dry: only counts
	}
	rep.finish()
	if !o.DryRun {
		invalidateStorageDetail()
	}
	return rep, nil
}

var errPruneBusy = errors.New("a prune or shrink is already running")

// pruneAfterCandidatesHook lets a test post a link between the first scan and the deletion.
var pruneAfterCandidatesHook func()

// pruneBeforeDiffHook lets a test reopen a task between the listing and acting on its diff.
var pruneBeforeDiffHook func(diffCandidate)

// deleteObjectIfStillUnused re-checks a candidate under its digest lock (an upload or
// auto-attach of the same bytes holds the same lock) and deletes it only if it is still old,
// unreferenced and not kept.
func (h *SessionsHandler) deleteObjectIfStillUnused(ctx context.Context, coralDir, id string, o pruneOptions, refs map[string]bool, scanAt time.Time) (int64, bool, error) {
	defer lockArtifact(id)()
	if refs[id] {
		return 0, false, nil
	}
	if kept, err := h.isProtected(ctx, id); err != nil || kept {
		return 0, false, err
	}
	use := objectNewestUse(coralDir, id, newestRecordFor(coralDir, id))
	if o.Now.Sub(use) < o.OlderThan || use.After(scanAt) {
		return 0, false, nil // uploaded again meanwhile
	}
	freed, _, found, err := deleteArtifactFilesLocked(coralDir, id)
	if !found {
		return 0, false, nil
	}
	if err == nil {
		h.dropArtifactComments(ctx, id)
	}
	return freed, err == nil, err
}

// newestRecordFor reads one artifact's upload records only.
func newestRecordFor(coralDir, id string) map[string]time.Time {
	if !objectNameRe.MatchString(id) { // an id read from a file or a message is a 64-digit hex name before it is joined onto a folder (#5305)
		return nil
	}
	out := map[string]time.Time{}
	sessions, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "uploads"))
	if err != nil {
		return out
	}
	for _, s := range sessions {
		up := uploadsDir(coralDir, s.Name())
		if up == "" || !s.IsDir() {
			continue
		}
		p := filepath.Join(up, id+".json")
		if _, err := os.Lstat(p); err == nil {
			if t := recordTime(p); t.After(out[id]) {
				out[id] = t
			}
		}
	}
	return out
}

// pruneDiffs removes closed diffs (their links). Bytes count as freed only when no other diff
// and no shared object keeps them; a shared object's bytes are freed by the sweep.
func (h *SessionsHandler) pruneDiffs(ctx context.Context, o pruneOptions, diffs []diffCandidate, rep *pruneReport, capped func() bool) {
	objects := diffObjectInodes(h.cfg.CoralDir())
	candidateLinks := map[fileKey]uint64{}
	for _, d := range diffs {
		candidateLinks[d.inode]++
	}
	counted := map[fileKey]bool{}
	for _, d := range diffs {
		if capped() {
			return
		}
		it := pruneItem{Kind: pruneKindDiffs, ID: d.kind + " " + d.id, Board: d.board, Bytes: d.bytes, AgeDays: o.Now.Sub(d.at).Hours() / 24}
		// Freed: the inode goes once every link to it is a candidate (plus its diff object,
		// which the sweep removes).
		// Signed, clamped: a link added or removed since the listing must not wrap (Lead's NIT D).
		others := int64(d.links) - int64(candidateLinks[d.inode])
		if objects[d.inode] {
			others--
		}
		if others < 0 {
			others = 0
		}
		freedHere := int64(0)
		if others == 0 && !counted[d.inode] {
			freedHere = d.bytes
			counted[d.inode] = true
		}
		if o.DryRun {
			rep.add(it)
			rep.Freed += freedHere
			continue
		}
		if pruneBeforeDiffHook != nil {
			pruneBeforeDiffHook(d)
		}
		if !h.diffStillClosed(ctx, d, o) {
			rep.Skipped++
			continue
		}
		if err := os.Remove(d.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			rep.Errors++
			continue
		}
		_ = os.Remove(filepath.Dir(d.path)) // the task's or session's folder, if now empty
		rep.add(it)
		rep.Removed++
		if !objects[d.inode] {
			rep.Freed += freedHere // a plain copy: freed now (a linked one, at the sweep)
		}
		slog.Info("[artifacts] pruned a stored diff", "kind", d.kind, "id", d.id, "bytes", d.bytes)
	}
}

// diffStillClosed re-checks a diff right before it goes: its task is still completed or
// skipped (a reopened task keeps its diff), its session still stopped.
func (h *SessionsHandler) diffStillClosed(ctx context.Context, d diffCandidate, o pruneOptions) bool {
	oldEnough := func(at *string) bool {
		t, ok := closedAt(at)
		return ok && o.Now.Sub(t) >= o.OlderThan
	}
	switch d.kind {
	case "task":
		if h.bs == nil {
			return false
		}
		var t struct {
			Status      string  `db:"status"`
			BoardID     string  `db:"board_id"`
			CompletedAt *string `db:"completed_at"`
		}
		err := h.bs.Reader().GetContext(ctx, &t, "SELECT status, board_id, completed_at FROM board_tasks WHERE id = ?", d.id)
		if errors.Is(err, sqlNoRows) {
			return true // the task is gone
		}
		if err == nil && d.boardKey != "" && TaskBoardKey(t.BoardID) != d.boardKey {
			return true // another board's task of this id (ids start again after a restore): this diff's task is gone (#6948)
		}
		// Still closed AND still old: a task reopened and closed again since the listing has a
		// new diff, which isn't due yet (Security's #3554 review).
		return err == nil && (t.Status == "completed" || t.Status == "skipped") && oldEnough(t.CompletedAt)
	case "session":
		var s struct {
			Status    *string `db:"status"`
			Sleeping  int     `db:"is_sleeping"`
			StoppedAt *string `db:"stopped_at"`
		}
		err := h.db.Reader().GetContext(ctx, &s, "SELECT status, is_sleeping, stopped_at FROM live_sessions WHERE session_id = ?", d.id)
		if errors.Is(err, sqlNoRows) {
			return true
		}
		return err == nil && s.Sleeping == 0 && s.Status != nil && *s.Status == "inactive" && oldEnough(s.StoppedAt)
	}
	return false
}

// shrinkDiffs rewrites each closed diff through #3555's filter (the text changes stay; secrets,
// dot-files, binaries and oversized sections become one-line notes). The new content is a new
// diff object and the diff is relinked to it; a shared object is never edited in place.
func (h *SessionsHandler) shrinkDiffs(ctx context.Context, o pruneOptions, diffs []diffCandidate, rep *pruneReport) {
	done := map[fileKey]bool{}
	// The old bytes are freed only when every link to them is rewritten (Lead's NIT E): a diff
	// shared with an open task, which isn't shrunk, keeps them on disk.
	objects := diffObjectInodes(h.cfg.CoralDir())
	candidateLinks := map[fileKey]int64{}
	for _, d := range diffs {
		candidateLinks[d.inode]++
	}
	allLinksShrunk := func(d diffCandidate) bool {
		others := int64(d.links) - candidateLinks[d.inode]
		if objects[d.inode] {
			others--
		}
		return others <= 0
	}
	for _, d := range diffs {
		if d.bytes > pruneDiffReadLimit {
			rep.Errors++
			continue
		}
		data, err := os.ReadFile(d.path)
		if err != nil {
			rep.Errors++
			continue
		}
		small := safeDiff(data)
		if len(small) >= len(data) {
			continue // nothing to drop
		}
		if !bytes.Equal(safeDiff(small), small) {
			rep.Errors++ // the result must itself be a clean, filtered patch
			continue
		}
		saved := int64(len(data) - len(small))
		it := pruneItem{Kind: "shrink", ID: d.kind + " " + d.id, Board: d.board, Bytes: saved, AgeDays: o.Now.Sub(d.at).Hours() / 24}
		freedHere := int64(0)
		if !done[d.inode] && allLinksShrunk(d) {
			freedHere = saved
			done[d.inode] = true
		}
		if o.DryRun {
			rep.add(it)
			rep.Freed += freedHere
			continue
		}
		if pruneBeforeDiffHook != nil {
			pruneBeforeDiffHook(d)
		}
		if !h.diffStillClosed(ctx, d, o) {
			rep.Skipped++
			continue
		}
		if err := writeChangesArtifact(d.path, small); err != nil {
			rep.Errors++
			continue
		}
		rep.add(it)
		rep.Removed++
		rep.Freed += freedHere
		slog.Info("[artifacts] shrank a stored diff", "kind", d.kind, "id", d.id, "from", len(data), "to", len(small))
	}
}

// sweepDiffObjects removes the shared diff objects no diff links any more (link count 1: only
// the object's own name), older than an hour so one just written for a link isn't taken.
func (h *SessionsHandler) sweepDiffObjects(o pruneOptions, rep *pruneReport, capped func() bool) {
	dir := filepath.Join(h.cfg.CoralDir(), "artifacts", "diff-objects")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if capped() {
			return
		}
		if !objectNameRe.MatchString(e.Name()) || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if fileLinks(filepath.Join(dir, e.Name()), fi) != 1 || o.Now.Sub(fi.ModTime()) < diffObjectGrace {
			continue
		}
		it := pruneItem{Kind: pruneKindDiffObjects, ID: e.Name(), Bytes: fi.Size(), AgeDays: o.Now.Sub(fi.ModTime()).Hours() / 24}
		if o.DryRun {
			rep.add(it)
			rep.Freed += fi.Size()
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			rep.Errors++
			continue
		}
		rep.add(it)
		rep.Removed++
		rep.Freed += fi.Size()
		slog.Info("[artifacts] removed an unused diff object", "digest", e.Name(), "bytes", fi.Size())
	}
}

// ── Routes ──────────────────────────────────────────────────────────

// isOperatorRequest is true for a person at this computer (the dashboard or the signed command line), false for an agent's token, a script and
// Coral Hub: what refuseNonOperator lets through, for a route that answers everyone but shows the Operator more (#4489).
func isOperatorRequest(r *http.Request) bool {
	return !cloud.Tunneled(r) && humanInputCaller(r)
}

func refuseNonOperator(w http.ResponseWriter, r *http.Request, what string) bool {
	if !isOperatorRequest(r) {
		writeError(w, http.StatusForbidden, "operator_only", "only the Operator can "+what+", from this computer's dashboard or the signed command line")
		return true
	}
	return false
}

// pruneArtifactsRoute: POST /api/artifacts/prune {older_than_seconds, kinds, dry_run, shrink}.
// Operator only (a person: the dashboard or the signed command line), never through Coral Hub.
// dry_run defaults to true.
func (h *SessionsHandler) pruneArtifactsRoute(w http.ResponseWriter, r *http.Request) {
	if refuseNonOperator(w, r, "prune artifacts") {
		return
	}
	var body struct {
		OlderThanSeconds int64    `json:"older_than_seconds"`
		Kinds            []string `json:"kinds"`
		DryRun           *bool    `json:"dry_run"`
		Shrink           bool     `json:"shrink"`
	}
	if err := decodeJSONLimit(r, &body, 4096); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	o := pruneOptions{OlderThan: time.Duration(body.OlderThanSeconds) * time.Second, Kinds: map[string]bool{}, DryRun: body.DryRun == nil || *body.DryRun, Shrink: body.Shrink}
	if body.OlderThanSeconds < 0 {
		errBadRequest(w, "older_than_seconds must not be negative")
		return
	}
	if !o.Shrink && o.OlderThan < pruneMinOlderThan {
		errBadRequest(w, "a prune needs older_than_seconds of at least one day (86400)")
		return
	}
	if len(body.Kinds) == 0 {
		body.Kinds = []string{pruneKindDiffs}
	}
	for _, k := range body.Kinds {
		switch k {
		case pruneKindDiffs, pruneKindObjects, pruneKindDiffObjects:
			o.Kinds[k] = true
		case "all":
			o.Kinds[pruneKindDiffs], o.Kinds[pruneKindObjects], o.Kinds[pruneKindDiffObjects] = true, true, true
		default:
			errBadRequest(w, "kinds may be diffs, objects, diff-objects or all")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), pruneRunTimeout)
	defer cancel()
	rep, err := h.runPrune(ctx, o)
	switch {
	case errors.Is(err, errPruneBusy):
		writeError(w, http.StatusConflict, "prune_running", err.Error())
		return
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, "prune_failed", err.Error())
		return
	}
	if !o.DryRun {
		slog.Info("[artifacts] prune finished", "shrink", o.Shrink, "removed", rep.Removed, "freed", rep.Freed, "skipped", rep.Skipped, "errors", rep.Errors)
	}
	writeJSON(w, http.StatusOK, rep)
}

// keepArtifactRoute: PUT /api/artifacts/{id}/keep marks an artifact the Operator keeps (never
// pruned); DELETE removes the mark. Operator only.
func (h *SessionsHandler) keepArtifactRoute(w http.ResponseWriter, r *http.Request) {
	if refuseNonOperator(w, r, "keep or release an artifact") {
		return
	}
	id := chi.URLParam(r, "id")
	if !objectNameRe.MatchString(id) {
		errBadRequest(w, "invalid artifact id")
		return
	}
	var err error
	if r.Method == http.MethodDelete {
		_, err = h.db.ExecContext(r.Context(), "DELETE FROM artifact_keep WHERE hex = ?", id)
	} else {
		if _, statErr := os.Stat(filepath.Join(h.cfg.CoralDir(), "artifacts", "objects", id)); statErr != nil {
			errNotFound(w, "artifact not found")
			return
		}
		_, err = h.db.ExecContext(r.Context(), "INSERT INTO artifact_keep (hex, kept_at) VALUES (?, ?) ON CONFLICT(hex) DO NOTHING", id, time.Now().UTC().Format(time.RFC3339))
	}
	if err != nil {
		errInternalServer(w, "could not update the keep mark")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "kept": r.Method != http.MethodDelete})
}

// keptListRoute: GET /api/system/artifact-store/kept, the artifacts the Operator keeps (for the Keep toggle on
// this computer's dashboard; never through Coral Hub).
func (h *SessionsHandler) keptListRoute(w http.ResponseWriter, r *http.Request) {
	if refuseNonOperator(w, r, "list kept artifacts") {
		return
	}
	kept, err := h.keptArtifacts(r.Context())
	if err != nil {
		errInternalServer(w, "could not read kept artifacts")
		return
	}
	ids := make([]string, 0, len(kept))
	for id := range kept {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	writeJSON(w, http.StatusOK, map[string]any{"kept": ids})
}
