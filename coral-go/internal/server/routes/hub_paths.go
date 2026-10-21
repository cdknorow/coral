package routes

// Paths through Coral Hub (#3559, Security's #3556 findings 3 and 5). A request that came
// through the tunnel gets no absolute path under this computer's home directory, which names
// the account: the JSON answer's path fields show it as "~/..." (a display form; the hub's
// grouping by folder still works, since one folder always maps to one form), and the fields
// the hub never needs (log_path, log_file, tmux_command, source_file) are left out. A tunneled
// request's own JSON body path fields starting with "~" are expanded back, so a
// path the hub got from Coral can be sent to it again. Field names are a fixed list, not a
// text replace over the body. File lists leave out hidden files' names (hubVisibleFiles).
// Requests from this computer are unchanged.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/safefs"
	"github.com/subgentic/coral/internal/store"
)

// hubPathFields hold a path; one under the home directory is shown as "~/...". Only string
// values are rewritten: a new field holding a list of paths (dirs, roots, recent folders) needs
// its own entry and handling here. A tunneled request's nested field with one of these names
// and a "~" value is expanded too, whoever wrote it, and its body is re-marshalled.
var hubPathFields = map[string]bool{
	"working_directory": true, "working_dir": true, "default_working_dir": true, "repo_path": true,
	"worktree_path": true, "git_root": true, "cwd": true, "root": true, "dir": true,
	"directory": true, "folder": true, "path": true, "filepath": true, "file": true,
}

// hubTextFields are messages that may quote a path inside their text (an error naming the
// file it couldn't open); a home directory in them is shown as "~" wherever it starts a path.
// A fixed list of three, not a rewrite of every string (Security's #3559 review).
var hubTextFields = map[string]bool{"error": true, "detail": true, "message": true}

// hubCommandFields hold commands (a workflow step's or a scheduled job's command, its args, a
// script, step definitions): a home directory where a path starts inside them is shown as "~"
// ("cd ~/proj && make test"), in a string or a list of strings (QA's #3561 case, Security's
// named list). Prompts and other free text are shown as written.
var hubCommandFields = map[string]bool{"command": true, "cmd": true, "args": true, "script": true, "steps": true}

// hubDroppedFields name files on this computer the hub has no use for.
var hubDroppedFields = map[string]bool{
	"log_path": true, "log_file": true, "tmux_command": true, "source_file": true,
	"changes_artifact": true, // the stored diff's local file; the hub reads it at /api/sessions/{id}/changes.diff (QA's #3561 finding)
}

// hubLongText are fields of free text (what people and agents wrote, output, markup): a value
// there is left as it is even when it begins with a path, since it isn't a path field.
var hubLongText = map[string]bool{
	"content": true, "text": true, "output": true, "body": true, "html": true, "prompt": true,
	"summary": true, "description": true, "title": true, "output_tail": true, "error_tail": true,
	"diff": true, "capture": true, "instructions": true, "custom_instructions": true,
}

// hubWholePath shows a value that is itself a path under the home directory (one line,
// starting at the home at a path boundary) as "~/..."; any other value is unchanged.
// Security's value-based rule (#3561): it covers path fields whatever their names.
func hubWholePath(s string) string {
	if strings.ContainsAny(s, "\n\r") {
		return s
	}
	return hubDisplayPath(s)
}

// hubHome caches the home directory's forms, for the $HOME it was worked out from.
var hubHome struct {
	sync.Mutex
	for_  string
	homes []string // as given and with symlinks resolved, no trailing separator
}

// hubHasHome reports whether homes already holds h, as paths compare on this platform.
func hubHasHome(homes []string, h string) bool {
	for _, x := range homes {
		if hubKey(x) == hubKey(h) {
			return true
		}
	}
	return false
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
		if real, err := safefs.Resolve(home); err == nil {
			if real = strings.TrimRight(real, `/\`); real != home {
				hubHome.homes = append(hubHome.homes, real)
			}
		}
		for _, form := range append([]string(nil), hubHome.homes...) {
			// C:\Users\JOHNSM~1: os.TempDir() and some tools spell the home that way, and it names the account too (#126 F5).
			if short := strings.TrimRight(shortPathOf(form), `/\`); short != "" && !hubHasHome(hubHome.homes, short) {
				hubHome.homes = append(hubHome.homes, short)
			}
		}
	}
	return hubHome.homes
}

// hubFold and hubSlashes are how a path is compared with the home directory: without regard
// to ASCII case where the file system ignores it (Windows, and macOS by default), and with "/"
// and "\" the same on Windows, so "c:/users/alice/proj" is the home too (#3935). Test seams.
var (
	hubFold    = runtime.GOOS == "windows" || runtime.GOOS == "darwin"
	hubSlashes = runtime.GOOS == "windows"
)

// hubKey is s as the home comparison sees it: ASCII letters folded and "/" as "\" where those
// apply, byte for byte the same length, so an index in it is an index in s. A letter outside
// ASCII still has to match exactly.
func hubKey(s string) string {
	if !hubFold && !hubSlashes {
		return s
	}
	b := []byte(s)
	for i, c := range b {
		switch {
		case hubFold && 'A' <= c && c <= 'Z':
			b[i] = c + ('a' - 'A')
		case hubSlashes && c == '/':
			b[i] = '\\'
		}
	}
	return string(b)
}

// hubDisplayPath shows p with a leading home directory as "~"; any other value is unchanged.
func hubDisplayPath(p string) string {
	kp := hubKey(p)
	for _, home := range hubHomes() {
		kh := hubKey(home)
		if kp == kh {
			return "~"
		}
		if len(p) > len(home) && strings.HasPrefix(kp, kh) && (p[len(home)] == '/' || p[len(home)] == '\\') {
			return "~" + p[len(home):]
		}
	}
	return p
}

// hubDisplayText shows each home directory inside s as "~" where it is a whole path prefix:
// at the start, after a byte that can't continue a path, or right after "://" (a file:// URL,
// Security's #3559 note), and followed by a separator or the end of a path.
func hubDisplayText(s string) string {
	homes := hubHomes()
	// The longer form first, so a resolved home that contains the given one is matched whole.
	order := append([]string(nil), homes...)
	sort.Slice(order, func(i, j int) bool { return len(order[i]) > len(order[j]) })
	for _, home := range order {
		ks, kh := hubKey(s), hubKey(home) // the same lengths as s and home (#3935)
		var b strings.Builder
		last := 0 // s[:last] is written
		for from := 0; ; {
			j := strings.Index(ks[from:], kh)
			if j < 0 {
				break
			}
			i, end := from+j, from+j+len(home)
			before := i == 0 || !hubPathChar(s[i-1]) || (i >= 3 && s[i-3:i] == "://")
			after := end == len(s) || s[end] == '/' || s[end] == '\\' || !hubPathChar(s[end])
			if before && after {
				b.WriteString(s[last:i])
				b.WriteString("~")
				last = end
			}
			from = end
		}
		b.WriteString(s[last:])
		s = b.String()
	}
	return s
}

// hubUploadText shows this computer's uploads folder as "~/..." inside free text (#5400). The dashboard types "<path> coral://artifacts/<id>" into the agent for a
// pasted image, <path> is an absolute path in the uploads folder, and when that folder is under the home directory the path names the OS account; the text comes back
// in the chat fields that are shown as written. Only this one folder is rewritten (each home form it can be spelled with), so other free text is still left alone.
func hubUploadText(s string) string {
	if uploadDir == "" || !strings.ContainsAny(s, `/\`) {
		return s
	}
	norm := func(x string) string {
		if hubSlashes {
			x = strings.ReplaceAll(x, `\`, "/")
		}
		return x
	}
	rest, ok := strings.CutPrefix(norm(hubDisplayPath(uploadDir)), "~/")
	if !ok {
		return s // the uploads folder is not under the home directory: nothing in its path names the account
	}
	// A path is typed as the OS spells it: on Windows with "\" (or "/"), in any letter case, so "/" matches both separators there and the match
	// ignores ASCII case where the file system does (the same rules as hubDisplayPath: hubSlashes, hubFold).
	sep := "/"
	if hubSlashes {
		sep = `[/\\]`
	}
	for _, h := range hubHomes() {
		parts := strings.Split(norm(h)+"/"+rest, "/")
		for i := range parts {
			parts[i] = regexp.QuoteMeta(parts[i])
		}
		expr := strings.Join(parts, sep)
		if hubFold {
			expr = "(?i)" + expr
		}
		if re, err := regexp.Compile(expr); err == nil {
			s = re.ReplaceAllLiteralString(s, "~/"+rest)
		}
	}
	return s
}

// hubPathChar: a byte that can continue a file name.
func hubPathChar(c byte) bool {
	return c == '.' || c == '-' || c == '_' || c == '/' || c == '\\' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
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
// set (an answer to the hub), the fields the hub doesn't need removed, home paths inside
// messages shown as "~", and any other value that is itself a home path (in any field but free
// text, and in lists) shown as "~/...".
func hubRewrite(v any, f func(string) string, drop bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if drop && hubDroppedFields[k] {
				delete(x, k)
				continue
			}
			if s, ok := val.(string); ok {
				switch {
				case hubPathFields[k]:
					x[k] = f(s)
				case !drop:
				case hubTextFields[k], hubCommandFields[k]:
					x[k] = hubDisplayText(s)
				case !hubLongText[k]:
					x[k] = hubUploadText(hubWholePath(s))
				default: // free text is shown as written, except for the uploads folder's path (#5400)
					x[k] = hubUploadText(s)
				}
				continue
			}
			if list, ok := val.([]any); ok && drop && hubCommandFields[k] {
				for i, el := range list {
					if s, ok := el.(string); ok {
						list[i] = hubDisplayText(s)
					}
				}
			}
			hubRewrite(val, f, drop)
		}
	case []any:
		for i, val := range x {
			if s, ok := val.(string); ok {
				if drop {
					x[i] = hubUploadText(hubWholePath(s))
				}
				continue
			}
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

// hubExpandRequest expands "~" in a tunneled request's JSON body path fields, so a display path
// the hub got back from Coral works when it's sent again (resume, launch in a folder). Query
// parameters need nothing: the tunnel's allow-list refuses a file reference starting with "~".
func hubExpandRequest(r *http.Request) {
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
