package routes

import (
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/subgentic/coral/internal/store"
)

// Comments across versions (#6357, #6459; the approved note, section 4): a comment belongs to the version it was written on, forever. Reading a newer version of a series with ?series=1 also
// lists the comments of its earlier versions, each marked with the version it was written on (from_version) and where its text stands in THIS version:
//
//	found      the quoted text is in this version exactly once, or more than once and its context (the text before and after it) picks exactly one place: carried_anchor says where
//	ambiguous  the text is here more than once and nothing picks one place: never pinned to a guess
//	gone       the text is not in this version
//	unchecked  this version is too large to search
//
// A comment that is not found stays with its old version in the "From earlier versions" group; it is never moved. Replying, resolving, deleting and sending always act on the ORIGINAL comment by
// its own artifact id (a carried comment's id on the newer version's URL is a 404), so a reply and an open ask stay in the thread of the version they were made on.

const carryMaxText = 4 << 20 // bytes of a version searched for carried quotes

// The cost of one ?series=1 read is bounded (Security's #6459 finding: 50,000 carried comments over a 4 MiB version took 45 s): an anchor is looked for once however many comments share it,
// and past carryMaxScans distinct anchors or carryMaxScanned bytes searched the rest are "unchecked" (left with their old version, not guessed).
const (
	carryMaxScans   = 2000
	carryMaxScanned = 64 << 20
)

// carryScans counts the searches of a version's text for a carried anchor (a test pins that the count is bounded and memoised).
var carryScans atomic.Int64

const (
	carryFound     = "found"
	carryAmbiguous = "ambiguous"
	carryGone      = "gone"
	carryUnchecked = "unchecked"
)

// reanchor says where an anchor's text stands in text: the state and, when found, the 1-based lines it now covers.
func reanchor(text string, a commentAnchor) (state string, start, end int) {
	if a.Whole {
		return carryFound, 0, 0 // a comment on the whole document is on the whole document of any version
	}
	q := a.Quote
	if q == "" {
		return carryGone, 0, 0
	}
	var at []int
	for from := 0; ; {
		i := strings.Index(text[from:], q)
		if i < 0 {
			break
		}
		at = append(at, from+i)
		if len(at) > 64 {
			break
		}
		from += i + 1
	}
	pos := -1
	switch {
	case len(at) == 0:
		return carryGone, 0, 0
	case len(at) == 1:
		pos = at[0]
	default:
		for _, p := range at {
			if strings.HasSuffix(text[:p], a.Prefix) && strings.HasPrefix(text[p+len(q):], a.Suffix) && (a.Prefix != "" || a.Suffix != "") {
				if pos >= 0 {
					return carryAmbiguous, 0, 0 // two places have the same text and the same surroundings
				}
				pos = p
			}
		}
		if pos < 0 {
			return carryAmbiguous, 0, 0
		}
	}
	start = 1 + strings.Count(text[:pos], "\n")
	end = start + strings.Count(q, "\n")
	return carryFound, start, end
}

// carriedComment is a comment of an earlier version with where it stands in the version being read.
type carriedComment struct {
	t       store.ArtifactComment
	from    int
	state   string
	anchor  *commentAnchor // where it is in this version, for found
	srcLock bool           // the lock of the version it was written on
}

// carriedComments are the comments of the versions before this one in its series, for a caller who may read each of them. id is the version being read, n its number.
func (h *SessionsHandler) carriedComments(r *http.Request, id string, n int, members []seriesMember, state string) []carriedComment {
	var text string
	checked := false
	if b, err := readSmallFile(filepath.Join(h.artifactDir(), id), carryMaxText); err == nil {
		text, checked = string(b), true
	}
	var out []carriedComment
	type found struct {
		state  string
		anchor *commentAnchor
	}
	memo := map[commentAnchor]found{} // one search per distinct anchor
	scans, scanned := 0, 0
	for _, m := range members {
		if m.N >= n || m.Digest == id || !artifactReadable(r, h.cfg.CoralDir(), m.Digest) { // per digest, as every artifact read
			continue
		}
		list, err := h.ss.ListArtifactComments(r.Context(), m.Digest, state, nil)
		if err != nil {
			continue
		}
		locked, _ := h.ss.ArtifactCommentsLocked(r.Context(), m.Digest, "")
		rootState := map[int64]carriedComment{}
		for _, t := range list {
			c := carriedComment{t: t, from: m.N, srcLock: locked}
			switch {
			case t.ParentID != nil:
				if root, ok := rootState[*t.ParentID]; ok { // a reply goes where its thread goes
					c.state, c.anchor = root.state, root.anchor
				} else {
					c.state = carryGone
				}
			case !checked:
				c.state = carryUnchecked
			default:
				a := anchorOfComment(t)
				if a == nil {
					c.state = carryGone
					break
				}
				key := *a
				key.StartLine, key.EndLine = 0, 0 // where it was is not what is looked for
				f, seen := memo[key]
				if !seen {
					if scans >= carryMaxScans || scanned+len(text) > carryMaxScanned {
						f = found{state: carryUnchecked}
					} else {
						scans++
						scanned += len(text)
						carryScans.Add(1)
						st, s, e := reanchor(text, *a)
						f = found{state: st}
						if st == carryFound {
							na := *a
							na.StartLine, na.EndLine = s, e
							f.anchor = &na
						}
					}
					memo[key] = f
				}
				c.state = f.state
				if f.anchor != nil {
					na := *f.anchor
					c.anchor = &na
				}
			}
			if t.ParentID == nil {
				rootState[t.ID] = c
			}
			out = append(out, c)
		}
	}
	return out
}
