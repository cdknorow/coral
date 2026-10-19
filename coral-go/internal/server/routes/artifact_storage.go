package routes

import (
	"context"
	"encoding/json"
	setreg "github.com/cdknorow/coral/internal/settings"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Settings "Storage" detail (#3554): the stored artifacts and diffs by age and by board,
// what the Operator keeps, and retention's state. Computed on request at most every few
// minutes (a stat walk plus one reference scan: about 0.3 s on a 23 GB store), and recomputed
// after a prune. Served only to this computer's dashboard (GET /api/system/artifact-store,
// never through the tunnel).

const storageDetailTTL = 5 * time.Minute

var storageDetailCache struct {
	sync.Mutex
	dir string
	at  time.Time
	val map[string]any
}

func invalidateStorageDetail() {
	storageDetailCache.Lock()
	storageDetailCache.val = nil
	storageDetailCache.Unlock()
}

type sizeCount struct {
	Bytes int64 `json:"bytes"`
	Count int   `json:"count"`
}

var storageAgeBuckets = []struct {
	name string
	max  time.Duration
}{{"<1d", 24 * time.Hour}, {"1-7d", 7 * 24 * time.Hour}, {"7-30d", 30 * 24 * time.Hour}, {">30d", 1 << 62}}

func ageBucket(age time.Duration) string {
	for _, b := range storageAgeBuckets {
		if age < b.max {
			return b.name
		}
	}
	return ">30d"
}

func (h *SessionsHandler) storageDetail(ctx context.Context) (map[string]any, error) {
	coralDir := h.cfg.CoralDir()
	storageDetailCache.Lock()
	if storageDetailCache.val != nil && storageDetailCache.dir == coralDir && time.Since(storageDetailCache.at) < storageDetailTTL {
		v := storageDetailCache.val
		storageDetailCache.Unlock()
		return h.withRetention(ctx, v), nil
	}
	storageDetailCache.Unlock()

	now := time.Now()
	byAge := map[string]map[string]*sizeCount{"objects": {}, "task_diffs": {}, "session_diffs": {}}
	add := func(kind string, age time.Duration, n int64) {
		b := ageBucket(age)
		if byAge[kind][b] == nil {
			byAge[kind][b] = &sizeCount{}
		}
		byAge[kind][b].Bytes += n
		byAge[kind][b].Count++
	}
	boards := map[string]int64{}

	// Diffs: every stored one (open or closed), by its task's board; a shared diff counts once.
	counted := map[fileKey]bool{}
	diffs, err := h.allDiffs(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range diffs {
		if counted[d.inode] {
			continue
		}
		counted[d.inode] = true
		kind := "task_diffs"
		if d.kind == "session" {
			kind = "session_diffs"
		}
		add(kind, now.Sub(d.at), d.bytes)
		if d.board != "" {
			boards[d.board] += d.bytes
		}
	}

	// Objects: by newest use, attributed to the first board that links each one.
	firstBoard, err := h.objectBoards(ctx)
	if err != nil {
		return nil, err
	}
	kept, err := h.keptArtifacts(ctx)
	if err != nil {
		return nil, err
	}
	var keptStat sizeCount
	records := uploadRecordTimes(coralDir)
	if entries, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "objects")); err == nil {
		for _, e := range entries {
			if !objectNameRe.MatchString(e.Name()) || !e.Type().IsRegular() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			add("objects", now.Sub(objectNewestUse(coralDir, e.Name(), records)), info.Size())
			if b := firstBoard[e.Name()]; b != "" {
				boards[b] += info.Size()
			} else {
				boards["(no board)"] += info.Size()
			}
			if kept[e.Name()] {
				keptStat.Bytes += info.Size()
				keptStat.Count++
			}
		}
	}

	labels := h.boardLabels(ctx)
	type boardSize struct {
		Board string `json:"board"`
		Label string `json:"label"`
		Bytes int64  `json:"bytes"`
	}
	var top []boardSize
	for id, n := range boards {
		top = append(top, boardSize{Board: id, Label: labels[id], Bytes: n})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].Bytes > top[j].Bytes })
	if len(top) > 10 {
		var rest int64
		for _, b := range top[10:] {
			rest += b.Bytes
		}
		top = append(top[:10], boardSize{Board: "(other boards)", Bytes: rest})
	}

	val := map[string]any{"by_age": byAge, "by_board": top, "kept": keptStat, "computed_at": now.UTC().Format(time.RFC3339)}
	storageDetailCache.Lock()
	storageDetailCache.dir, storageDetailCache.at, storageDetailCache.val = coralDir, now, val
	storageDetailCache.Unlock()
	return h.withRetention(ctx, val), nil
}

// withRetention copies the cached detail and adds retention's state, read fresh each time (a
// settings read: cheap, and it changes as the Operator turns retention on or reviews it).
func (h *SessionsHandler) withRetention(ctx context.Context, cached map[string]any) map[string]any {
	out := make(map[string]any, len(cached)+1)
	for k, v := range cached {
		out[k] = v
	}
	settings := map[string]string{}
	if h.ss != nil {
		if s, err := h.ss.GetSettings(ctx); err == nil {
			settings = s
		}
	}
	var last any
	_ = json.Unmarshal([]byte(setreg.Raw(settings, RetentionLastKey)), &last)
	out["retention"] = map[string]any{
		"days": setreg.Raw(settings, RetentionDaysSetting), "kinds": setreg.Raw(settings, RetentionKindsSetting),
		"enabled_at": setreg.Raw(settings, retentionEnabledAtKey), "acknowledged_at": setreg.Raw(settings, RetentionAckAtKey), "last": last,
	}
	return out
}

// allDiffs lists every stored task and session diff, open or closed.
func (h *SessionsHandler) allDiffs(ctx context.Context) ([]diffCandidate, error) {
	coralDir := h.cfg.CoralDir()
	boardOf := map[string]string{}
	if h.bs != nil {
		var rows []struct {
			ID      int64  `db:"id"`
			BoardID string `db:"board_id"`
		}
		if err := h.bs.Reader().SelectContext(ctx, &rows, "SELECT id, board_id FROM board_tasks"); err != nil {
			return nil, err
		}
		for _, r := range rows {
			boardOf[strconv.FormatInt(r.ID, 10)] = r.BoardID
		}
	}
	var out []diffCandidate
	for _, kind := range []string{"tasks", "sessions"} {
		dir := filepath.Join(coralDir, "artifacts", kind)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			c, ok := diffAt(filepath.Join(dir, e.Name(), "changes.diff"))
			if !ok {
				continue
			}
			c.kind, c.id = strings.TrimSuffix(kind, "s"), e.Name()
			if kind == "tasks" {
				c.board = boardOf[e.Name()]
			}
			out = append(out, c)
		}
	}
	return out, nil
}

// objectBoards maps each linked artifact to the first board that links it: a post's board, or
// a task's (its text or its results).
func (h *SessionsHandler) objectBoards(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	if h.bs == nil {
		return out, nil
	}
	db := h.bs.Reader()
	for _, q := range []string{
		"SELECT project, CAST(content AS BLOB) FROM board_messages WHERE content LIKE '%artifacts/%' ORDER BY id",
		"SELECT board_id, CAST(COALESCE(body,'') || ' ' || COALESCE(completion_message,'') AS BLOB) FROM board_tasks WHERE body LIKE '%artifacts/%' OR completion_message LIKE '%artifacts/%' ORDER BY id",
		"SELECT t.board_id, CAST(w.data AS BLOB) FROM task_workflows w JOIN board_tasks t ON t.id = w.task_id WHERE w.data LIKE '%artifacts/%' ORDER BY w.task_id",
	} {
		rows, err := db.QueryxContext(ctx, q)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var board string
			var text []byte
			if err := rows.Scan(&board, &text); err != nil {
				rows.Close()
				return nil, err
			}
			refs := map[string]bool{}
			addArtifactRefs(text, refs)
			for id := range refs {
				if out[id] == "" {
					out[id] = board
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (h *SessionsHandler) boardLabels(ctx context.Context) map[string]string {
	out := map[string]string{}
	if h.bs == nil {
		return out
	}
	var rows []struct {
		ID    string `db:"id"`
		Label string `db:"label"`
	}
	if err := h.bs.Reader().SelectContext(ctx, &rows, "SELECT id, label FROM boards"); err == nil {
		for _, r := range rows {
			out[r.ID] = r.Label
		}
	}
	return out
}
