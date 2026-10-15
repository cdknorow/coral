package routes

// Paths through Coral Hub (#3559, Security's #3556 findings 3 and 5). A request that came
// through the tunnel gets no absolute path under this computer's home directory, which names
// the account: the JSON answer's path fields show it as "~/..." (a display form; the hub's
// grouping by folder still works, since one folder always maps to one form), and the fields
// the hub never needs (log_path, log_file, tmux_command, source_file) are left out. A tunneled
// request's own path fields and query parameters starting with "~" are expanded back, so a
// path the hub got from Coral can be sent to it again. Field names are a fixed list, not a
// text replace over the body. File lists leave out hidden files' names (hubVisibleFiles).
// Requests from this computer are unchanged.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/store"
)

// hubPathFields hold a path; one under the home directory is shown as "~/...".
var hubPathFields = map[string]bool{
	"working_directory": true, "working_dir": true, "default_working_dir": true, "repo_path": true,
	"worktree_path": true, "git_root": true, "cwd": true, "root": true, "dir": true,
	"directory": true, "folder": true, "path": true, "filepath": true, "file": true,
}

// hubDroppedFields name files on this computer the hub has no use for.
var hubDroppedFields = map[string]bool{
	"log_path": true, "log_file": true, "tmux_command": true, "source_file": true,
}

// hubHome caches the home directory's forms, for the $HOME it was worked out from.
var hubHome struct {
	sync.Mutex
	for_  string
	homes []string // as given and with symlinks resolved, no trailing separator
}

func hubHomes() []string {
	home, err := os.UserHomeDir()
	home = strings.TrimRight(home, `/\`)
	if err != nil || home == "" {
		return nil
	}
	hubHome.Lock()
	defer hubHome.Unlock()
	if hubHome.for_ != home {
		hubHome.for_, hubHome.homes = home, []string{home}
		if real, err := filepath.EvalSymlinks(home); err == nil {
			if real = strings.TrimRight(real, `/\`); real != home {
				hubHome.homes = append(hubHome.homes, real)
			}
		}
	}
	return hubHome.homes
}

// hubDisplayPath shows p with a leading home directory as "~"; any other value is unchanged.
func hubDisplayPath(p string) string {
	for _, home := range hubHomes() {
		if p == home {
			return "~"
		}
		if len(p) > len(home) && strings.HasPrefix(p, home) && (p[len(home)] == '/' || p[len(home)] == '\\') {
			return "~" + p[len(home):]
		}
	}
	return p
}

// hubExpandPath turns "~" or "~/..." back into the home directory; any other value is unchanged.
func hubExpandPath(p string) string {
	homes := hubHomes()
	if len(homes) == 0 {
		return p
	}
	if p == "~" {
		return homes[0]
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return homes[0] + p[1:]
	}
	return p
}

// hubRewrite edits a decoded JSON value in place: path fields through f, and, when drop is
// set, the fields the hub doesn't need removed.
func hubRewrite(v any, f func(string) string, drop bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if drop && hubDroppedFields[k] {
				delete(x, k)
				continue
			}
			if s, ok := val.(string); ok {
				if hubPathFields[k] {
					x[k] = f(s)
				}
				continue
			}
			hubRewrite(val, f, drop)
		}
	case []any:
		for _, val := range x {
			hubRewrite(val, f, drop)
		}
	}
}

// hubJSON returns a tunneled answer's JSON with its paths in display form; ok is false when
// body isn't JSON (it's then sent as it was).
func hubJSON(body []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return nil, false
	}
	hubRewrite(v, hubDisplayPath, true)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, false
	}
	return out.Bytes(), true
}

// hubFrame is hubJSON for a WebSocket frame built from Go values (shared with other
// connections, so it's rewritten on a copy).
func hubFrame(msg any) any {
	data, err := json.Marshal(msg)
	if err != nil {
		return msg
	}
	out, ok := hubJSON(data)
	if !ok {
		return msg
	}
	return json.RawMessage(out)
}

// HubPaths applies the above to requests that came through Coral Hub (cloud.Tunneled).
// WebSocket upgrades pass through (WSCoral rewrites its frames itself); an answer that isn't
// JSON (a diff, a file, an event stream) is passed through untouched as it's written.
func HubPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cloud.Tunneled(r) || strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			next.ServeHTTP(w, r)
			return
		}
		hubExpandRequest(r)
		hw := &hubPathsWriter{ResponseWriter: w}
		next.ServeHTTP(hw, r)
		hw.finish()
	})
}

// hubExpandRequest expands "~" in a tunneled request's path query parameters and JSON body
// fields, so a display path the hub got back from Coral works when it's sent again.
func hubExpandRequest(r *http.Request) {
	if q := r.URL.Query(); len(q) > 0 {
		changed := false
		for k, vals := range q {
			if !hubPathFields[k] {
				continue
			}
			for i, s := range vals {
				if e := hubExpandPath(s); e != s {
					vals[i], changed = e, true
				}
			}
		}
		if changed {
			r.URL.RawQuery = q.Encode()
		}
	}
	if r.Body == nil || r.Method == http.MethodGet || r.Method == http.MethodHead ||
		!strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return
	}
	orig := r.Body
	body, err := io.ReadAll(io.LimitReader(orig, 8<<20+1))
	if err != nil || len(body) > 8<<20 {
		// Not read whole: hand the handler what was read followed by the rest, as it came.
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), orig), orig}
		return
	}
	orig.Close()
	if !bytes.Contains(body, []byte(`"~`)) {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) == nil && !dec.More() {
		hubRewrite(v, hubExpandPath, false)
		if out, err := json.Marshal(v); err == nil {
			body = out
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))
}

// hubPathsWriter holds a JSON answer back to rewrite it; anything else streams through.
type hubPathsWriter struct {
	http.ResponseWriter
	decided, hold bool
	status        int
	buf           bytes.Buffer
}

func (w *hubPathsWriter) decide() {
	if w.decided {
		return
	}
	w.decided = true
	ct := strings.ToLower(w.Header().Get("Content-Type"))
	w.hold = strings.HasPrefix(ct, "application/json")
}

func (w *hubPathsWriter) WriteHeader(status int) {
	w.decide()
	if w.hold {
		w.status = status
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *hubPathsWriter) Write(p []byte) (int, error) {
	w.decide()
	if w.hold {
		return w.buf.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

// Flush passes through for a streamed answer (a held JSON answer is sent whole at the end).
func (w *hubPathsWriter) Flush() {
	if w.hold {
		return
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *hubPathsWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *hubPathsWriter) finish() {
	if !w.hold {
		return
	}
	body := w.buf.Bytes()
	if out, ok := hubJSON(body); ok {
		body = out
	}
	w.Header().Del("Content-Length")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(status)
	_, _ = w.ResponseWriter.Write(body)
}

// hubVisibleFiles leaves out, for a tunneled request, files whose path is hidden (a dot-file or
// dot-folder, or the sensitive-path list), as #3555 does in diffs: their names alone say what's
// there (.env.local, keys/id_rsa).
func hubVisibleFiles(r *http.Request, files []store.ChangedFile) []store.ChangedFile {
	if !cloud.Tunneled(r) {
		return files
	}
	out := make([]store.ChangedFile, 0, len(files))
	for _, f := range files {
		if !diffPathHidden(f.Filepath) {
			out = append(out, f)
		}
	}
	return out
}
