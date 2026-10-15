package routes

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	_ "image/gif" // DecodeConfig for the dimension cap
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cdknorow/coral/internal/gitutil"
	"github.com/cdknorow/coral/internal/store"
)

// Board auto-attach (#3515): when an agent's own board post links a fresh local image or
// Markdown file with Markdown syntax (![..](path) or [..](path)), Coral stores the file as
// an ordinary artifact and points the link at coral://artifacts/<hex>, so viewers through
// Coral Hub can open it (the artifact GET is View tier; uploads stay Never). Every bound
// fails closed: the link is left as the agent wrote it and nothing is stored.
//
// It reads files on the agent's behalf, outside the agent CLI's own permissions, so it is
// held to: the agent's working directory (images also its git root), no dot-components, the
// sensitive-path list, files modified in the last day, regular files with one link, sniffed
// image types with a pixel cap (EXIF stripped), UTF-8 Markdown, per-message, per-agent and
// per-Coral caps, and the Operator's switch (setting board_auto_attach = "false").

// AutoAttachSetting turns auto-attach off when set to false (on by default).
const AutoAttachSetting = "board_auto_attach"

const (
	attachMaxPerMessage   = 8
	attachMaxMessageBytes = 25 << 20
	attachMaxImageBytes   = 10 << 20
	attachMaxMarkdown     = 1 << 20
	attachMaxPixels       = 40_000_000 // width × height
	attachMaxSide         = 16384
	attachFresh           = 24 * time.Hour
	attachWindow          = time.Hour
	attachWindowAttempts  = 50 // per agent per hour, refused attempts included
	attachWindowBytes     = 100 << 20
	attachTotalBytes      = 2 << 30 // per Coral, ever (auto-attach only)
)

// attachLinkRe matches a Markdown image or link whose target is a local path ending in an
// attachable extension: ![alt](path), [text](path), or the angle form [text](<path with
// spaces>). Bare paths in prose never match.
var attachLinkRe = regexp.MustCompile(`(!?)\[([^\]\n]*)\]\((?:<([^<>\n]+)>|([^()\s<>]+))\)`)

var attachImageTypes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}

// attachNameDeny: name fragments that are never attached, beyond sensitivePath.
var attachNameDeny = []string{"secret", "credential", "token", ".env", "passw", "private"}

// AutoAttacher rewrites an agent's board post; see the comment at the top of the file.
type AutoAttacher struct {
	coralDir string
	enabled  func(context.Context) bool
	// dirs returns the session's working directory and git root (either may be "").
	dirs   func(ctx context.Context, sessionID string) (workdir, gitRoot string)
	now    func() time.Time
	secret []byte // per-process HMAC key for path hashes in logs

	mu      sync.Mutex
	windows map[string]*attachUsage
	total   int64 // bytes auto-attached by this Coral (persisted)
}

type attachUsage struct {
	start    time.Time
	attempts int
	bytes    int64
	warned   bool
}

// NewAutoAttacher returns an attacher storing under coralDir. enabled reports the
// Operator's switch; dirs finds a session's working directory and git root.
func NewAutoAttacher(coralDir string, enabled func(context.Context) bool, dirs func(context.Context, string) (string, string)) *AutoAttacher {
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	a := &AutoAttacher{coralDir: coralDir, enabled: enabled, dirs: dirs, now: time.Now, secret: secret, windows: map[string]*attachUsage{}}
	if b, err := os.ReadFile(a.totalFile()); err == nil {
		a.total, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	}
	return a
}

// SessionDirs is the dirs function for NewAutoAttacher: the working directory Coral
// launched the session in (the live session record, not the pane's current directory,
// which the agent can change) and that directory's git root.
func SessionDirs(ss *store.SessionStore) func(context.Context, string) (string, string) {
	return func(ctx context.Context, sessionID string) (string, string) {
		live, err := ss.GetLiveSession(ctx, sessionID)
		if err != nil || live == nil || live.WorkingDir == "" {
			return "", ""
		}
		return live.WorkingDir, gitutil.ResolveGitRoot(ctx, live.WorkingDir)
	}
}

// AutoAttachEnabled reads the Operator's switch: on unless set to false, 0, off or disabled. A
// settings read that fails counts as off.
func AutoAttachEnabled(ss *store.SessionStore) func(context.Context) bool {
	return func(ctx context.Context) bool {
		s, err := ss.GetSettings(ctx)
		if err != nil {
			return false
		}
		// The dashboard's settingBool rule; a JSON false is stored as "False".
		switch strings.ToLower(strings.TrimSpace(s[AutoAttachSetting])) {
		case "false", "0", "off", "disabled":
			return false
		}
		return true
	}
}

func (a *AutoAttacher) totalFile() string {
	return filepath.Join(a.coralDir, "artifacts", "auto-attach-bytes")
}

// pathTag is what the logs carry instead of a path: an HMAC with a per-process key.
func (a *AutoAttacher) pathTag(p string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(p))
	return hex.EncodeToString(m.Sum(nil))[:16]
}

// errAttach is a refusal; its text is the logged reason (never a path).
type errAttach string

func (e errAttach) Error() string { return string(e) }

// Rewrite returns content with each attachable link pointed at its stored artifact. Only
// call it for a post made with the agent's own verified token.
func (a *AutoAttacher) Rewrite(ctx context.Context, project, sessionID, sessionName, content string) string {
	if a == nil || sessionID == "" || (a.enabled != nil && !a.enabled(ctx)) {
		return content
	}
	matches := attachLinkRe.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return content
	}
	workdir, gitRoot := "", ""
	if a.dirs != nil {
		workdir, gitRoot = a.dirs(ctx, sessionID)
	}
	type result struct{ hex string }
	done := map[string]result{} // per message: target as written → artifact
	byReal := map[string]string{}
	files, msgBytes := 0, int64(0)
	var out strings.Builder
	last := 0
	limited := false
	for _, m := range matches {
		ts, te := m[6], m[7]
		if ts < 0 {
			ts, te = m[8], m[9]
		}
		target := content[ts:te]
		ext := strings.ToLower(filepath.Ext(target))
		_, isImage := attachImageTypes[ext]
		if !isImage && ext != ".md" || !localTarget(target) {
			continue
		}
		res, seen := done[target]
		if !seen {
			res = result{}
			switch {
			case files >= attachMaxPerMessage:
				a.logRefusal(project, sessionName, target, errAttach("message_file_cap"))
			case !a.admit(sessionID):
				limited = true
				a.logRefusal(project, sessionName, target, errAttach("agent_hourly_cap"))
			default:
				real, data, mediaType, err := a.load(workdir, gitRoot, target, isImage, attachMaxMessageBytes-msgBytes)
				if err == nil {
					if prev, ok := byReal[real]; ok {
						res = result{prev}
						break
					}
					err = a.charge(sessionID, int64(len(data)))
				}
				if err == nil {
					var hexID string
					hexID, _, err = storeArtifact(a.coralDir, sessionID, attachName(filepath.Base(real)), mediaType, data)
					if err == nil {
						res = result{hexID}
						byReal[real] = hexID
						files++
						msgBytes += int64(len(data))
						slog.Info("[auto-attach] attached", "project", project, "session", sessionName, "path_tag", a.pathTag(real), "size", len(data), "type", mediaType, "digest", hexID)
					}
				}
				if err != nil {
					a.logRefusal(project, sessionName, target, err)
				}
			}
			done[target] = res
		}
		if res.hex == "" {
			continue
		}
		// Replace the whole destination, angle brackets included.
		ds, de := m[6]-1, m[7]+1
		if m[6] < 0 {
			ds, de = m[8], m[9]
		}
		out.WriteString(content[last:ds])
		out.WriteString("coral://artifacts/" + res.hex)
		last = de
	}
	out.WriteString(content[last:])
	if limited && a.warnOnce(sessionID) {
		out.WriteString("\n\n[Coral: auto-attach is paused for this agent until the hourly limit resets]")
	}
	return out.String()
}

func (a *AutoAttacher) logRefusal(project, session, target string, err error) {
	reason := "refused"
	var e errAttach
	if errors.As(err, &e) {
		reason = string(e)
	}
	slog.Info("[auto-attach] left as written", "project", project, "session", session, "path_tag", a.pathTag(target), "reason", reason)
}

// localTarget: a plain local path. URLs, schemes, home shorthand, anchors and (outside
// Windows) backslashes are never read.
func localTarget(t string) bool {
	if t == "" || strings.Contains(t, "://") || strings.HasPrefix(t, "~") || strings.HasPrefix(t, "#") || strings.ContainsRune(t, 0) {
		return false
	}
	if runtime.GOOS != "windows" {
		if strings.ContainsRune(t, '\\') || strings.Contains(t, ":") {
			return false
		}
	}
	return true
}

// admit counts one attempt against the agent's hourly window.
func (a *AutoAttacher) admit(sessionID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	u := a.usage(sessionID)
	if u.attempts >= attachWindowAttempts || u.bytes >= attachWindowBytes {
		return false
	}
	u.attempts++
	return true
}

// charge reserves n bytes in the agent's window and the Coral-wide total.
func (a *AutoAttacher) charge(sessionID string, n int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	u := a.usage(sessionID)
	if u.bytes+n > attachWindowBytes {
		return errAttach("agent_hourly_bytes")
	}
	if a.total+n > attachTotalBytes {
		return errAttach("coral_total_cap")
	}
	u.bytes += n
	a.total += n
	if err := os.MkdirAll(filepath.Dir(a.totalFile()), 0o700); err == nil {
		_ = os.WriteFile(a.totalFile(), []byte(strconv.FormatInt(a.total, 10)), 0o600)
	}
	return nil
}

func (a *AutoAttacher) usage(sessionID string) *attachUsage {
	now := a.now()
	u := a.windows[sessionID]
	if u == nil || now.Sub(u.start) >= attachWindow {
		// A new window: forget every agent whose window has ended.
		for id, w := range a.windows {
			if now.Sub(w.start) >= attachWindow {
				delete(a.windows, id)
			}
		}
		u = &attachUsage{start: now}
		a.windows[sessionID] = u
	}
	return u
}

func (a *AutoAttacher) warnOnce(sessionID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	u := a.usage(sessionID)
	if u.warned {
		return false
	}
	u.warned = true
	return true
}

// attachRoots: images may come from the working directory or its git root; Markdown only
// from the working directory. A root that is /, a drive root, or the home directory (or
// contains it) is never used.
func attachRoots(workdir, gitRoot string, isImage bool) []string {
	var roots []string
	cands := []string{workdir}
	if isImage {
		cands = append(cands, gitRoot)
	}
	for _, d := range cands {
		if d == "" {
			continue
		}
		if real := realPath(d); rootAllowed(real) && attachRootAllowed(real) {
			roots = append(roots, real)
		}
	}
	return roots
}

// attachRootAllowed: beyond rootAllowed, a shared or shallow directory is never an
// auto-attach root, since anything fresh under it (other programs' and other users'
// files) would be attachable: the system temp directories themselves (their per-project
// subfolders are fine) and any root with fewer than two path components (/opt, /srv, C:\x).
func attachRootAllowed(real string) bool {
	clean := filepath.Clean(real)
	rest := strings.Trim(filepath.ToSlash(strings.TrimPrefix(clean, filepath.VolumeName(clean))), "/")
	if rest == "" || len(strings.Split(rest, "/")) < 2 {
		return false
	}
	for _, t := range []string{os.TempDir(), "/tmp", "/var/tmp", "/private/tmp", "/private/var/tmp"} {
		if t == "" {
			continue
		}
		for _, c := range []string{filepath.Clean(t), realPath(t)} {
			if c != "" && clean == c {
				return false
			}
		}
	}
	return true
}

// load resolves target, checks every bound, and reads the file. budget is what is left of
// the message's byte cap.
func (a *AutoAttacher) load(workdir, gitRoot, target string, isImage bool, budget int64) (string, []byte, string, error) {
	roots := attachRoots(workdir, gitRoot, isImage)
	if len(roots) == 0 {
		return "", nil, "", errAttach("no_root")
	}
	p := filepath.FromSlash(target)
	if !filepath.IsAbs(p) {
		if workdir == "" {
			return "", nil, "", errAttach("no_root")
		}
		p = filepath.Join(workdir, p)
	}
	p = filepath.Clean(p)
	real := realPath(p)
	if real == "" {
		return "", nil, "", errAttach("not_found")
	}
	root := ""
	for _, r := range roots {
		if within(r, real) {
			root = r
			break
		}
	}
	if root == "" {
		return "", nil, "", errAttach("outside_roots")
	}
	rel, err := filepath.Rel(root, real)
	if err != nil {
		return "", nil, "", errAttach("outside_roots")
	}
	for _, c := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(c, ".") {
			return "", nil, "", errAttach("dot_component")
		}
	}
	lowerBase := strings.ToLower(filepath.Base(real))
	if sensitivePath(real) {
		return "", nil, "", errAttach("sensitive_path")
	}
	for _, d := range attachNameDeny {
		if strings.Contains(lowerBase, d) {
			return "", nil, "", errAttach("sensitive_name")
		}
	}
	limit := int64(attachMaxMarkdown)
	if isImage {
		limit = attachMaxImageBytes
	}
	if budget < limit {
		limit = budget
	}
	data, err := readAttachable(real, limit, a.now())
	if err != nil {
		return "", nil, "", err
	}
	// The path still resolves to the same file: a parent directory swapped for a symlink
	// while it was read is caught here.
	if again := realPath(p); again != real {
		return "", nil, "", errAttach("path_changed")
	}
	ext := strings.ToLower(filepath.Ext(real))
	if !isImage {
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return "", nil, "", errAttach("markdown_not_utf8")
		}
		return real, data, "text/markdown", nil
	}
	want := attachImageTypes[ext]
	if sniffImage(data) != want {
		return "", nil, "", errAttach("type_mismatch")
	}
	if err := checkPixels(data, want); err != nil {
		return "", nil, "", err
	}
	data, err = stripImageMetadata(data, want)
	if err != nil {
		return "", nil, "", err
	}
	return real, data, want, nil
}

// attachTestHook lets tests change the file system between the checks and the read
// (stage "before_open", then "after_stat"); nil outside tests.
var attachTestHook func(stage, path string)

// readAttachable opens real without following a final symlink and without blocking on a
// FIFO, and reads at most limit bytes from a regular file with one link, modified within
// attachFresh, that is the same file a fresh Lstat names and that didn't change while read.
func readAttachable(real string, limit int64, now time.Time) ([]byte, error) {
	if attachTestHook != nil {
		attachTestHook("before_open", real)
	}
	f, err := os.OpenFile(real, os.O_RDONLY|attachOpenFlags, 0)
	if err != nil {
		return nil, errAttach("open_failed")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, errAttach("not_regular")
	}
	if n, ok := linkCount(fi); ok && n != 1 {
		return nil, errAttach("hardlink")
	}
	if li, err := os.Lstat(real); err != nil || !os.SameFile(fi, li) {
		return nil, errAttach("path_changed")
	}
	if now.Sub(fi.ModTime()) > attachFresh {
		return nil, errAttach("not_fresh")
	}
	if fi.Size() > limit {
		return nil, errAttach("too_large")
	}
	if attachTestHook != nil {
		attachTestHook("after_stat", real)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, errAttach("read_failed")
	}
	if int64(len(data)) > limit {
		return nil, errAttach("too_large")
	}
	after, err := f.Stat()
	if err != nil || after.Size() != fi.Size() || !after.ModTime().Equal(fi.ModTime()) || int64(len(data)) != fi.Size() {
		return nil, errAttach("changed_while_read")
	}
	return data, nil
}

// sniffImage is the content's image type by its magic bytes ("" if not one of ours).
func sniffImage(data []byte) string {
	switch t := http.DetectContentType(data); t {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return t
	}
	return ""
}

// checkPixels refuses an image larger than attachMaxSide on a side or attachMaxPixels in all.
func checkPixels(data []byte, mediaType string) error {
	var w, h int
	if mediaType == "image/webp" {
		var ok bool
		if w, h, ok = webpSize(data); !ok {
			return errAttach("bad_image")
		}
	} else {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return errAttach("bad_image")
		}
		w, h = cfg.Width, cfg.Height
	}
	if w <= 0 || h <= 0 || w > attachMaxSide || h > attachMaxSide || int64(w)*int64(h) > attachMaxPixels {
		return errAttach("too_many_pixels")
	}
	return nil
}

// webpSize reads a WebP's canvas size from its first chunk (VP8X, VP8 or VP8L).
func webpSize(d []byte) (int, int, bool) {
	if len(d) < 30 || string(d[0:4]) != "RIFF" || string(d[8:12]) != "WEBP" {
		return 0, 0, false
	}
	c := d[20:]
	switch string(d[12:16]) {
	case "VP8X":
		w := int(c[4]) | int(c[5])<<8 | int(c[6])<<16
		h := int(c[7]) | int(c[8])<<8 | int(c[9])<<16
		return w + 1, h + 1, true
	case "VP8 ":
		if c[3] != 0x9d || c[4] != 0x01 || c[5] != 0x2a {
			return 0, 0, false
		}
		return int(binary.LittleEndian.Uint16(c[6:8]) & 0x3fff), int(binary.LittleEndian.Uint16(c[8:10]) & 0x3fff), true
	case "VP8L":
		if c[0] != 0x2f {
			return 0, 0, false
		}
		b := binary.LittleEndian.Uint32(c[1:5])
		return int(b&0x3fff) + 1, int((b>>14)&0x3fff) + 1, true
	}
	return 0, 0, false
}

// stripImageMetadata removes location and camera metadata without re-encoding:
//   - JPEG: only whitelisted segments (jpegKeep), cut at the EOI;
//   - PNG: every chunk but the critical ones and those that only affect display (pngKeep),
//     cut at IEND;
//   - WebP: an EXIF or XMP chunk is refused rather than rewritten (the container's
//     flags would need editing);
//   - GIF: comment, plain-text and application extensions dropped, cut at the trailer.
func stripImageMetadata(data []byte, mediaType string) ([]byte, error) {
	switch mediaType {
	case "image/jpeg":
		return stripJPEG(data)
	case "image/gif":
		return stripGIF(data)
	case "image/png":
		return stripPNGText(data)
	case "image/webp":
		for off := 12; off+8 <= len(data); {
			id := string(data[off : off+4])
			n := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
			if id == "EXIF" || id == "XMP " {
				return nil, errAttach("webp_metadata")
			}
			if n < 0 || n > len(data) {
				return nil, errAttach("bad_image")
			}
			off += 8 + n + n&1
		}
	}
	return data, nil
}

// jpegKeep reports whether a JPEG marker segment is kept: APP0 (JFIF), APP2 only with an
// ICC profile, APP14 (Adobe colour transform), and the table, frame, restart-interval and
// scan segments. Everything else (APP1 EXIF/XMP, APP3-13 incl. IPTC, MPF thumbnails in
// APP2, APP11 C2PA, APP15, COM, ...) is dropped.
func jpegKeep(marker byte, payload []byte) bool {
	switch {
	case marker == 0xE0, marker == 0xEE: // APP0, APP14
		return true
	case marker == 0xE2: // APP2: only an ICC profile
		return bytes.HasPrefix(payload, []byte("ICC_PROFILE\x00"))
	case marker == 0xDB, marker == 0xC4, marker == 0xCC, marker == 0xDD, marker == 0xDA: // DQT, DHT, DAC, DRI, SOS
		return true
	case marker >= 0xC0 && marker <= 0xCF: // SOFn (C4, C8, CC handled above or reserved)
		return marker != 0xC8
	}
	return false
}

// stripJPEG keeps only the whitelisted segments (jpegKeep) and the entropy-coded scan data,
// and ends the output at the EOI that ends the image: anything appended after it (another
// file, a polyglot, trailing data) is dropped. Nothing is re-encoded.
func stripJPEG(d []byte) ([]byte, error) {
	if len(d) < 4 || d[0] != 0xFF || d[1] != 0xD8 {
		return nil, errAttach("bad_image")
	}
	out := make([]byte, 0, len(d))
	out = append(out, 0xFF, 0xD8)
	i := 2
	scanned := false
	for i+2 <= len(d) {
		if d[i] != 0xFF {
			return nil, errAttach("bad_image")
		}
		marker := d[i+1]
		switch {
		case marker == 0xFF: // fill byte
			i++
			continue
		case marker == 0xD9: // EOI: the end of the image
			if !scanned {
				return nil, errAttach("bad_image")
			}
			return append(out, 0xFF, 0xD9), nil
		case marker >= 0xD0 && marker <= 0xD7, marker == 0x01: // no length
			return nil, errAttach("bad_image")
		}
		if i+4 > len(d) {
			return nil, errAttach("bad_image")
		}
		n := int(binary.BigEndian.Uint16(d[i+2 : i+4]))
		if n < 2 || i+2+n > len(d) {
			return nil, errAttach("bad_image")
		}
		seg := d[i : i+2+n]
		if jpegKeep(marker, d[i+4:i+2+n]) {
			out = append(out, seg...)
		}
		i += 2 + n
		if marker != 0xDA {
			continue
		}
		// Entropy-coded data after SOS runs to the next marker that isn't a stuffed 0x00 or
		// a restart marker.
		start := i
		for i+1 < len(d) {
			if d[i] == 0xFF && d[i+1] != 0x00 && !(d[i+1] >= 0xD0 && d[i+1] <= 0xD7) {
				break
			}
			i++
		}
		if i+1 >= len(d) {
			return nil, errAttach("bad_image") // no EOI
		}
		out = append(out, d[start:i]...)
		scanned = true
	}
	return nil, errAttach("bad_image")
}

// pngKeep: the critical chunks and the ancillary ones that only affect how the image
// looks; every other chunk (eXIf, tEXt, zTXt, iTXt, tIME, private chunks) is dropped.
var pngKeep = map[string]bool{
	"IHDR": true, "PLTE": true, "IDAT": true, "IEND": true,
	"tRNS": true, "gAMA": true, "cHRM": true, "sRGB": true, "iCCP": true, "sBIT": true, "bKGD": true, "pHYs": true,
	"acTL": true, "fcTL": true, "fdAT": true, // APNG frames
}

func stripPNGText(d []byte) ([]byte, error) {
	const sig = "\x89PNG\r\n\x1a\n"
	if len(d) < 8 || string(d[:8]) != sig {
		return nil, errAttach("bad_image")
	}
	out := append(make([]byte, 0, len(d)), d[:8]...)
	for i := 8; i < len(d); {
		if i+12 > len(d) {
			return nil, errAttach("bad_image")
		}
		n := int(binary.BigEndian.Uint32(d[i : i+4]))
		if n < 0 || i+12+n > len(d) {
			return nil, errAttach("bad_image")
		}
		typ := string(d[i+4 : i+8])
		if pngKeep[typ] {
			out = append(out, d[i:i+12+n]...)
		}
		i += 12 + n
		if typ == "IEND" {
			return out, nil // nothing after IEND is kept
		}
	}
	return nil, errAttach("bad_image") // no IEND
}

// stripGIF drops comment, plain-text and application extensions (NETSCAPE looping
// included, so an animation plays once) and ends the output at the trailer: anything
// appended after it is dropped. Image data and graphic control extensions are kept as is.
func stripGIF(d []byte) ([]byte, error) {
	if len(d) < 13 || (string(d[:6]) != "GIF87a" && string(d[:6]) != "GIF89a") {
		return nil, errAttach("bad_image")
	}
	i := 13
	if d[10]&0x80 != 0 { // global colour table
		i += 3 << (d[10]&0x07 + 1)
	}
	if i > len(d) {
		return nil, errAttach("bad_image")
	}
	out := append(make([]byte, 0, len(d)), d[:i]...)
	// subBlocks returns the end of the data sub-blocks starting at j.
	subBlocks := func(j int) (int, bool) {
		for j < len(d) {
			n := int(d[j])
			j++
			if n == 0 {
				return j, true
			}
			j += n
		}
		return 0, false
	}
	for i < len(d) {
		switch d[i] {
		case 0x3B: // trailer
			return append(out, 0x3B), nil
		case 0x21: // extension
			if i+2 > len(d) {
				return nil, errAttach("bad_image")
			}
			end, ok := subBlocks(i + 2)
			if !ok {
				return nil, errAttach("bad_image")
			}
			if d[i+1] == 0xF9 { // graphic control: timing and transparency
				out = append(out, d[i:end]...)
			}
			i = end
		case 0x2C: // image descriptor
			j := i + 10
			if j > len(d) {
				return nil, errAttach("bad_image")
			}
			if d[i+9]&0x80 != 0 { // local colour table
				j += 3 << (d[i+9]&0x07 + 1)
			}
			j++ // LZW minimum code size
			end, ok := subBlocks(j)
			if !ok || j > len(d) {
				return nil, errAttach("bad_image")
			}
			out = append(out, d[i:end]...)
			i = end
		default:
			return nil, errAttach("bad_image")
		}
	}
	return nil, errAttach("bad_image") // no trailer
}

// attachName is the artifact's stored name: letters, digits, dot, dash, underscore and
// space; anything else becomes _, at most 64 characters.
func attachName(base string) string {
	var b strings.Builder
	for _, r := range base {
		if b.Len() >= 64 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "attachment"
	}
	return b.String()
}
