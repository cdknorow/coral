package routes

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/subgentic/coral/internal/board"
)

// boardStaticRoutes are /api/board/<segment> routes that aren't a board.
var boardStaticRoutes = map[string]bool{"projects": true, "tasks": true, "remotes": true}

// boardResolveCalls counts the database resolves resolveBoard made (tests: one per request).
var boardResolveCalls atomic.Int64

// BoardResolveCount is how many database resolves resolveBoard has made (tests).
func BoardResolveCount() int64 { return boardResolveCalls.Load() }

// boardMemoKey carries a request's resolved boards in its context.
type boardMemoKey struct{}

type boardMemoEntry struct {
	id         string
	candidates []board.Board
	err        error
}

// boardMemo remembers what bs.ResolveBoard answered for each key within ONE request, so the
// identity layer (which resolves the board to check an agent's claim) and BoardKeyResolver (which
// rewrites the URL) share one resolve instead of asking the database twice (#4303).
type boardMemo struct {
	mu sync.Mutex
	m  map[string]boardMemoEntry
}

// BoardResolveMemo makes a request's board resolves share one answer. Mount it before the
// identity middleware.
func BoardResolveMemo(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/board/") {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), boardMemoKey{}, &boardMemo{m: map[string]boardMemoEntry{}})))
	})
}

// resolveBoard is bs.ResolveBoard through the request's memo, when it has one. An error other than
// "not found" or "ambiguous" is returned as it came and is remembered too: both callers treat it
// as "leave the key alone", and a second ask a few microseconds later would not answer better.
func resolveBoard(r *http.Request, bs *board.Store, key string) (string, []board.Board, error) {
	memo, _ := r.Context().Value(boardMemoKey{}).(*boardMemo)
	if memo == nil {
		boardResolveCalls.Add(1)
		return bs.ResolveBoard(r.Context(), key)
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	if e, ok := memo.m[key]; ok {
		return e.id, e.candidates, e.err
	}
	boardResolveCalls.Add(1)
	id, cands, err := bs.ResolveBoard(r.Context(), key)
	memo.m[key] = boardMemoEntry{id, cands, err}
	return id, cands, err
}

// BoardKeyResolver turns the board segment of /api/board/<key>/... into the board's id
// (#3536). An exact id always wins; otherwise a label that names exactly one board is
// rewritten to that board's id, so a script or an older CLI that uses a board's name keeps
// working. A label that names several boards is answered 409 ambiguous_board with the
// candidates; a key nothing uses is left alone (a subscribe or post may start that board,
// under the name rule). A key that can't be a board id in a URL ("." , "..", control or
// bidi characters) is refused. Mounted after the identity middleware, so only an
// authenticated caller ever sees the candidates.
func BoardKeyResolver(bs *board.Store) func(http.Handler) http.Handler {
	const prefix = "/api/board/"
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			esc := r.URL.EscapedPath()
			if bs == nil || !strings.HasPrefix(esc, prefix) {
				next.ServeHTTP(w, r)
				return
			}
			rest := esc[len(prefix):]
			escSeg, escTail := rest, ""
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				escSeg, escTail = rest[:i], rest[i:]
			}
			key, err := url.PathUnescape(escSeg)
			if err != nil || escSeg == "" {
				next.ServeHTTP(w, r)
				return
			}
			if boardStaticRoutes[key] {
				next.ServeHTTP(w, r)
				return
			}
			if !board.UsableBoardKey(key) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_board_key", "code": "invalid_board_key",
					"message": "That board key can't be used in a URL ('.', '..', '/' or a control character)"})
				return
			}
			id, candidates, err := resolveBoard(r, bs, key)
			switch {
			case err == nil && id != key:
				tail, terr := url.PathUnescape(escTail)
				if terr != nil {
					next.ServeHTTP(w, r)
					return
				}
				r2 := r.Clone(r.Context())
				r2.URL.Path = prefix + id + tail
				r2.URL.RawPath = prefix + url.PathEscape(id) + escTail
				next.ServeHTTP(w, r2)
				return
			case errors.Is(err, board.ErrAmbiguousBoard):
				writeJSON(w, http.StatusConflict, map[string]any{"error": "ambiguous_board", "code": "ambiguous_board",
					"message":    "More than one board is named \"" + key + "\": use its id",
					"candidates": boardCandidates(r, bs, candidates)})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// boardCandidates describes boards for a 409 answer (ambiguous_board, board_name_exists).
func boardCandidates(r *http.Request, bs *board.Store, boards []board.Board) []map[string]any {
	out := make([]map[string]any, 0, len(boards))
	for _, b := range boards {
		out = append(out, map[string]any{"board_id": b.ID, "board_label": b.Label, "created_at": b.CreatedAt, "members": bs.BoardMembers(r.Context(), b.ID)})
	}
	return out
}
