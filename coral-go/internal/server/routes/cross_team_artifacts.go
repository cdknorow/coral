package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/diskguard"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/pkg/tunnel"
)

// A file another Coral sends through Coral Hub (cross-team artifacts, coral-hub's design section 3.3 and
// section 10 C3). The receiving Coral trusts nothing in it: the sender is the hub's X-Coral-Hub-Server
// stamp (only on a request this process's connector carried), the file's type is decided from its own
// bytes (gateReceived), its name is display text, and it reaches the shared object store only after the
// gate, the size and the digest checks, so a refused file is never in the store, not even briefly.

const (
	maxReceivedArtifact = 8 << 20 // the largest file the receiver takes: the hub's default, plus the frame
	maxReceivedMeta     = 4096    // the metadata object
	maxReceivedInFlight = 4       // transfers read into memory at once

	receivedPerSourceHour = 20
	receivedSourceDay     = 100 << 20
	receivedTargetDay     = 200 << 20
	receivedAccountDay    = 500 << 20
)

var (
	receivedDigestRe    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	receivedMediaTypeRe = regexp.MustCompile(`^[A-Za-z0-9!#$&^_.+-]{1,127}/[A-Za-z0-9!#$&^_.+-]{1,127}$`)

	// receivedMemberNames are the only members the frame's metadata may have, spelled exactly.
	receivedMemberNames = map[string]bool{"message_id": true, "to_address": true, "from_board": true, "digest": true, "size": true, "name": true, "media_type": true}
	// receivedOptionalMember may be there too (a Coral from before the allowlist does not send it).
	receivedOptionalMember = "from_cross_team_id"
)

// receivedMeta is the frame's metadata, as the hub re-encoded it.
type receivedMeta struct {
	MessageID string `json:"message_id"`
	ToAddress string `json:"to_address"`
	FromBoard string `json:"from_board"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	// FromCrossTeamID is the sending Coral's claim of its board's cross-team id (#5508), "" when absent or malformed (a Coral from before it, or a bad claim): in force, such a file is refused.
	FromCrossTeamID string `json:"from_cross_team_id,omitempty"`
}

// parseReceivedMeta reads the metadata object strictly: one object, nothing after it, every member named
// exactly (no case variant, none unknown), none twice, size a plain integer from 1 to the cap, and every
// text member valid.
func parseReceivedMeta(raw []byte) (receivedMeta, error) {
	var m receivedMeta
	bad := errors.New("malformed metadata")
	// Before decoding: encoding/json turns invalid UTF-8 into U+FFFD, which would hide it.
	if !utf8.Valid(raw) {
		return m, bad
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return m, bad
	}
	seen := map[string]bool{}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		kt, err := dec.Token()
		key, ok := kt.(string)
		if err != nil || !ok || !(receivedMemberNames[key] || key == receivedOptionalMember) || seen[key] {
			return m, bad
		}
		seen[key] = true
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return m, bad
		}
		fields[key] = v
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return m, bad
	}
	if _, err := dec.Token(); err != io.EOF {
		return m, bad
	}
	_, hasClaim := fields[receivedOptionalMember]
	if n := len(fields); (hasClaim && n != len(receivedMemberNames)+1) || (!hasClaim && n != len(receivedMemberNames)) {
		return m, bad
	}
	if hasClaim {
		var claim string
		if json.Unmarshal(fields[receivedOptionalMember], &claim) == nil && board.ValidCrossTeamID(claim) {
			m.FromCrossTeamID = claim // anything else is no claim at all, never echoed
		}
	}
	str := func(key string, dst *string) bool {
		return json.Unmarshal(fields[key], dst) == nil && len(fields[key]) > 0 && fields[key][0] == '"'
	}
	sz := fields["size"]
	if len(sz) == 0 || sz[0] < '1' || sz[0] > '9' && sz[0] != '-' {
		return m, bad
	}
	n, err := strconv.ParseInt(string(sz), 10, 64)
	if err != nil || n < 1 || n > maxReceivedArtifact {
		return m, bad
	}
	m.Size = n
	if !str("message_id", &m.MessageID) || !str("to_address", &m.ToAddress) || !str("from_board", &m.FromBoard) ||
		!str("digest", &m.Digest) || !str("name", &m.Name) || !str("media_type", &m.MediaType) {
		return m, bad
	}
	if !relayIDRe.MatchString(m.MessageID) || !receivedDigestRe.MatchString(m.Digest) || !receivedMediaTypeRe.MatchString(m.MediaType) ||
		!plainLabel(m.ToAddress) || !plainLabel(m.FromBoard) || !plainLabel(m.Name) {
		return m, bad
	}
	return m, nil
}

// plainLabel: 1 to 200 bytes of valid UTF-8 with no control or format character (direction overrides,
// zero-width joiners) in it.
func plainLabel(s string) bool {
	if s == "" || len(s) > 200 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

// receivedLimiter is the receiver's own brake on what a linked Coral can push (the hub's quota is not the
// only one): per source server a count an hour and bytes a day, per target board bytes a day, and bytes a
// day for the whole Coral. Every admitted transfer is charged by its declared size, stored or refused.
type receivedLimiter struct {
	mu     sync.Mutex
	now    func() time.Time
	events []receivedEvent
	busy   map[string]bool // sources with a transfer in flight
	// charged are the (source, message id, file) transfers already charged, so a retry of a message
	// whose earlier attempt failed partway is not charged again (#4440).
	charged map[string]*receivedCharge
}

type receivedCharge struct {
	at      time.Time
	retries int
}

// receivedFreeRetries bounds the retries of one charged transfer that cost no quota: a sender that
// repeats the same message id and file forever pays after these.
const receivedFreeRetries = 5

// admitTransfer is admit for a file named by its message: the first transfer of (src, message id, digest)
// is charged; a repeat of it (a retry) is not, up to receivedFreeRetries a day, then it is charged again.
func (l *receivedLimiter) admitTransfer(key, src, target string, size int64) (ok bool, limit string, retry time.Duration) {
	l.mu.Lock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	for k, c := range l.charged {
		if now.Sub(c.at) >= 24*time.Hour {
			delete(l.charged, k)
		}
	}
	if c := l.charged[key]; c != nil && c.retries < receivedFreeRetries {
		c.retries++
		l.mu.Unlock()
		return true, "", 0
	}
	l.mu.Unlock()
	if ok, limit, retry = l.admit(src, target, size); !ok {
		return ok, limit, retry
	}
	l.mu.Lock()
	if l.charged == nil {
		l.charged = map[string]*receivedCharge{}
	}
	l.charged[key] = &receivedCharge{at: now}
	l.mu.Unlock()
	return true, "", 0
}

type receivedEvent struct {
	at          time.Time
	src, target string
	bytes       int64
}

// admit charges a transfer, or says which limit it hit and when the oldest charge against it expires.
func (l *receivedLimiter) admit(src, target string, size int64) (ok bool, limit string, retry time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	kept := l.events[:0]
	for _, e := range l.events {
		if now.Sub(e.at) < 24*time.Hour {
			kept = append(kept, e)
		}
	}
	l.events = kept
	var hourN int
	var srcB, tgtB, allB int64
	var oldestHour, oldestSrc, oldestTgt, oldestAll time.Time
	first := func(cur *time.Time, t time.Time) {
		if cur.IsZero() || t.Before(*cur) {
			*cur = t
		}
	}
	for _, e := range l.events {
		allB += e.bytes
		first(&oldestAll, e.at)
		if e.src == src {
			srcB += e.bytes
			first(&oldestSrc, e.at)
			if now.Sub(e.at) < time.Hour {
				hourN++
				first(&oldestHour, e.at)
			}
		}
		if e.target == target {
			tgtB += e.bytes
			first(&oldestTgt, e.at)
		}
	}
	wait := func(oldest time.Time, window time.Duration) time.Duration {
		if d := oldest.Add(window).Sub(now); d > 0 {
			return d
		}
		return time.Second
	}
	switch {
	case hourN >= receivedPerSourceHour:
		return false, "hourly", wait(oldestHour, time.Hour)
	case srcB+size > receivedSourceDay:
		return false, "daily", wait(oldestSrc, 24*time.Hour)
	case tgtB+size > receivedTargetDay:
		return false, "target", wait(oldestTgt, 24*time.Hour)
	case allB+size > receivedAccountDay:
		return false, "account", wait(oldestAll, 24*time.Hour)
	}
	l.events = append(l.events, receivedEvent{at: now, src: src, target: target, bytes: size})
	return true, "", 0
}

// begin marks src as having a transfer in flight, or reports that it already has one: one linked Coral
// cannot hold every slot.
func (l *receivedLimiter) begin(src string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.busy == nil {
		l.busy = map[string]bool{}
	}
	if l.busy[src] {
		return false
	}
	l.busy[src] = true
	return true
}

func (l *receivedLimiter) end(src string) {
	l.mu.Lock()
	delete(l.busy, src)
	l.mu.Unlock()
}

// receivedSlots bounds the transfers held in memory at once.
var receivedSlots = make(chan struct{}, maxReceivedInFlight)

func receivedRefuse(w http.ResponseWriter, status int, code, msg, limit string) {
	body := map[string]any{"ok": false, "error": code, "message": msg}
	if limit != "" {
		body["limit"] = limit
	}
	writeJSON(w, status, body)
}

// PostExternalArtifact receives a file Coral Hub relayed from another Coral to the {project} board.
// POST /api/board/{project}/external-artifacts with X-Coral-Hub-Server and the frame: a 4-byte length (1 to
// 4096), the metadata object, then exactly size bytes. It answers {"ok":true,"artifact_id"} or a
// fixed code, never text from the file or its sender.
func (h *BoardHandler) PostExternalArtifact(w http.ResponseWriter, r *http.Request) {
	src, present := relaySource(r)
	if !present || r.Header.Get(tunnel.HubUserHeader) != "" || !cloud.TunneledAuthentic(r) {
		if r.Header.Get(tunnel.HubUserHeader) != "" {
			receivedRefuse(w, http.StatusBadRequest, "ambiguous_origin", "a relayed file comes from a server, never a Coral Hub user", "")
			return
		}
		receivedRefuse(w, http.StatusForbidden, "hub_server_untrusted", "X-Coral-Hub-Server is only accepted from Coral Hub", "")
		return
	}
	if low, free := diskguard.Low(); low {
		receivedRefuse(w, http.StatusInsufficientStorage, "disk_low", fmt.Sprintf("the disk this Coral stores its data on is almost full (%d MiB free): the file was not stored", free>>20), "")
		return
	}
	src = strings.ToLower(src)
	if !relayIDRe.MatchString(src) {
		receivedRefuse(w, http.StatusBadRequest, "bad_request", "malformed source server", "")
		return
	}
	target := urlParam(r, "project")
	ctx := r.Context()
	if ok, err := h.bs.BoardExists(ctx, target); err != nil {
		errInternalServer(w, "could not check the board")
		return
	} else if !ok {
		if h.allowlistOn(ctx) { // an unknown board is the same answer as every other refusal (#5508: no oracle)
			receivedRefuse(w, http.StatusForbidden, "artifacts_off", "this board does not accept files from other computers", "")
			return
		}
		receivedRefuse(w, http.StatusNotFound, "not_found", "no such board", "")
		return
	}
	// One uniform answer for every switch that is off, so a sender cannot tell which.
	if on, err := h.receivingOn(ctx, r, target, src, "", false); err != nil {
		errInternalServer(w, "could not read the board's settings")
		return
	} else if !on {
		receivedRefuse(w, http.StatusForbidden, "artifacts_off", "this board does not accept files from other computers", "")
		return
	}
	if h.coralDir == "" {
		errInternalServer(w, "no data folder")
		return
	}
	// The declared length is checked before a byte is read: the frame is 4 + metadata + size.
	if r.ContentLength < 4+2 {
		receivedRefuse(w, http.StatusBadRequest, "bad_frame", "the frame needs a known length", "")
		return
	}
	if r.ContentLength > 4+maxReceivedMeta+maxReceivedArtifact {
		receivedRefuse(w, http.StatusRequestEntityTooLarge, "artifact_too_large", "the file is too large", "")
		return
	}
	if !h.received.begin(src) {
		httputil.WriteActionLimitBody(w, 1, map[string]any{"ok": false, "error": "rate_limited", "message": "a file from this server is already arriving; retry"})
		return
	}
	defer h.received.end(src)
	select {
	case receivedSlots <- struct{}{}:
		defer func() { <-receivedSlots }()
	default:
		httputil.WriteActionLimitBody(w, 1, map[string]any{"ok": false, "error": "rate_limited", "message": "too many files are arriving; retry"})
		return
	}
	body := r.Body
	var lb [4]byte
	if _, err := io.ReadFull(body, lb[:]); err != nil {
		receivedRefuse(w, http.StatusBadRequest, "bad_frame", "the frame is cut short", "")
		return
	}
	n := int(lb[0])<<24 | int(lb[1])<<16 | int(lb[2])<<8 | int(lb[3])
	if n < 1 || n > maxReceivedMeta {
		receivedRefuse(w, http.StatusBadRequest, "bad_frame", "the metadata length is out of range", "")
		return
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(body, raw); err != nil {
		receivedRefuse(w, http.StatusBadRequest, "bad_frame", "the frame is cut short", "")
		return
	}
	meta, err := parseReceivedMeta(raw)
	if err != nil || r.ContentLength != 4+int64(n)+meta.Size {
		receivedRefuse(w, http.StatusBadRequest, "bad_frame", "the metadata is not valid", "")
		return
	}
	if ok, limit, retry := h.received.admitTransfer(src+"\x00"+meta.MessageID+"\x00"+meta.Digest, src, target, meta.Size); !ok {
		secs := int((retry + time.Second - 1) / time.Second)
		httputil.WriteActionLimitBody(w, secs, map[string]any{"ok": false, "error": "artifact_quota", "limit": limit, "message": "too many files received; retry later"})
		return
	}
	// The sender's claimed board, now that the metadata is read: in force, a file from a sender that is not on the list is the same uniform refusal as every other "no" (#5508). It has cost the
	// sender's transfer quota, as an accepted file does.
	if on, err := h.receivingOn(ctx, r, target, src, meta.FromCrossTeamID, true); err != nil {
		errInternalServer(w, "could not read the board's settings")
		return
	} else if !on {
		receivedRefuse(w, http.StatusForbidden, "artifacts_off", "this board does not accept files from other computers", "")
		return
	}
	data := make([]byte, meta.Size)
	if _, err := io.ReadFull(body, data); err != nil {
		receivedRefuse(w, http.StatusBadRequest, "bad_frame", "the file is shorter than its size", "")
		return
	}
	if k, err := body.Read(make([]byte, 1)); k != 0 || err != io.EOF {
		receivedRefuse(w, http.StatusBadRequest, "bad_frame", "the file is longer than its size", "")
		return
	}

	// The gate and the digest, on bytes still in memory: nothing refused is ever stored.
	res, err := gateReceived(data)
	if err != nil {
		var gr gateRefusal
		if errors.As(err, &gr) {
			receivedRefuse(w, http.StatusBadRequest, gr.Code, gr.Reason, "")
			return
		}
		errInternalServer(w, "could not check the file")
		return
	}
	sum := sha256.Sum256(res.Data)
	hexID := hex.EncodeToString(sum[:])
	if "sha256:"+hexID != meta.Digest {
		receivedRefuse(w, http.StatusBadRequest, "digest_mismatch", "the file does not match its digest", "")
		return
	}
	// The Operator may have turned the switch off while the file was arriving (up to the connector's
	// deadline): read again just before anything is stored, and store nothing if it is off (#4440).
	if receivedBeforeStore != nil {
		receivedBeforeStore()
	}
	if on, err := h.receivingOn(ctx, r, target, src, meta.FromCrossTeamID, true); err != nil {
		errInternalServer(w, "could not read the board's settings")
		return
	} else if !on {
		receivedRefuse(w, http.StatusForbidden, "artifacts_off", "this board does not accept files from other computers", "")
		return
	}
	// The cap on received files (#4441): checked before the bytes are stored, for new objects only. Local uploads are not counted here and not refused by it.
	if !receivedFits(h.coralDir, hexID, int64(len(res.Data)), h.pendingNow()) {
		receivedRefuse(w, http.StatusRequestEntityTooLarge, "store_full", "this computer's limit on received files is reached", "")
		return
	}
	// No session id: the file is no agent's upload, so it gets no upload record, only the receipt below.
	// The same answer for new bytes and bytes already stored: a sender learns nothing about what this
	// computer holds.
	_, stored, err := storeArtifact(h.coralDir, "", receivedFileName(meta.Name, res), res.MediaType, res.Data)
	if err != nil {
		if errors.Is(err, errStoreFull) {
			receivedRefuse(w, http.StatusRequestEntityTooLarge, "store_full", "this computer's artifact store is full", "")
			return
		}
		errInternalServer(w, "could not store the file")
		return
	}
	// The board's received row (C1's record: its Team Artifacts list it as received, apart from the team's
	// own files and task results) is written when the message that names this file arrives
	// (promotePending), not now: until then the receipt is pending, and a file whose message never comes
	// is never listed (#4440). It is written by this Coral from the hub's stamp and the metadata, and kept
	// as first written: from_board is only what the sending Coral claimed. The name and type are the
	// stored object's (the first writer's of these bytes).
	if err := h.writePendingReceipt(src, meta.MessageID, crossTeamArtifactRecord{
		ID: hexID, Name: stored.Name, MediaType: stored.MediaType, Size: int64(len(res.Data)),
		Board: target, OtherBoard: meta.FromBoard, Server: src, At: h.pendingNow().UTC().Format(time.RFC3339),
	}); err != nil {
		errInternalServer(w, "could not record the file")
		return
	}
	// Ids, sizes and the outcome only: never the name or the content.
	slog.Info("[cloud] relayed cross-team artifact", "source_server", src, "board", target, "bytes", len(res.Data))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "artifact_id": hexID})
}

// receivedBeforeStore, when set (tests), runs after a file's gate and digest checks and before the switch is
// read for the last time and the file is stored.
var receivedBeforeStore func()

// receivingOn reports whether the board takes files from src right now. Allowlist off: messaging on, src listed (the server list), files on. In force (#5508): files on, and (once the frame's metadata
// has been read, claimKnown) the sender is on the board's list by BOTH the hub-verified server and the cross-team id it claims; before that only the files switch can be asked. Every "no" is the
// same uniform answer.
func (h *BoardHandler) receivingOn(ctx context.Context, r *http.Request, target, src, claim string, claimKnown bool) (bool, error) {
	settings, err := h.bs.GetBoardSettings(ctx, target)
	if err != nil {
		return false, err
	}
	if h.allowlistOn(ctx) {
		if !settings.CrossTeamArtifacts {
			return false, nil
		}
		return !claimKnown || settings.AllowsRelayedSender(src, claim), nil
	}
	// The door follows what the connection's hub advertised, as for a message (#5865, #5862). A links connection admits by a fresh link from the sending team as Coral Hub names it
	// (X-Coral-Hub-Source-Team, never the frame's own claim) with the board's messaging and files switches on; a connection whose hub no longer advertises links after one did admits
	// nothing; a hub that never did keeps the switch, the board's server list and the files switch. The link table is read in every mode, so the reads do not tell the modes apart.
	linked := h.links.admits(src, hubSourceTeam(r), target, h.linkNow())
	switch h.DoorMode(ctx, r) {
	case doorLinks:
		return linked && settings.CrossTeamMessaging && settings.CrossTeamArtifacts, nil
	case doorRefuse:
		return false, nil
	}
	return settings.CrossTeamMessaging && settings.AllowsServer(src) && settings.CrossTeamArtifacts, nil
}
