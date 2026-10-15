package routes

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/cdknorow/coral/internal/board"
)

// boardStaticRoutes are /api/board/<segment> routes that aren't a board.
var boardStaticRoutes = map[string]bool{"projects": true, "tasks": true, "remotes": true}

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
			id, candidates, err := bs.ResolveBoard(r.Context(), key)
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
