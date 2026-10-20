package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/secfile"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// The send side of cross-team artifacts across Corals (C2, #4403, coral-hub's design section 3.3 and 10):
// coral-board send --address ... --attach pushes each attachment, as stored and under its stored id, to
// Coral Hub before the text message. Every attachment is resolved by C1's rule (cross_team_attach.go: the
// sending team's upload records at the time of the send; never a task result, a shared or a received row),
// then held in memory, re-checked against its id, and scanned if it is text, all before the first byte
// goes: a file swapped after the check fails the send with nothing sent, and nothing is recorded as shared
// until Coral Hub has the files and the message.

var relayMediaTypeRe = regexp.MustCompile(`^[A-Za-z0-9!#$&^_.+-]{1,127}/[A-Za-z0-9!#$&^_.+-]{1,127}$`)

// maxRelayMeta is the largest metadata object the hub and the receiver accept.
const maxRelayMeta = 4096

// RelayArtifactSender sends one file frame to Coral Hub (cloud.Manager.RelayArtifact).
type RelayArtifactSender func(ctx context.Context, n int64, body io.Reader) (tunnel.RelayResponse, error)

// SetRelayArtifact attaches the Coral Hub file relay (nil: a send with --attach answers 503 not_linked).
func (h *BoardHandler) SetRelayArtifact(fn RelayArtifactSender) {
	h.mu.Lock()
	h.relayArtifact = fn
	h.mu.Unlock()
}

func (h *BoardHandler) relayArtifactSender() RelayArtifactSender {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.relayArtifact
}

// attachBeforeLoad, when set (tests), runs after a file is opened and checked for the send and before its
// bytes are read.
var attachBeforeLoad func(path string)

// attachAfterRead, when set (tests), runs after a file's bytes are read and checked and before the name is
// checked again.
var attachAfterRead func(path string)

// loadAttachment opens an attachment once, no-follow, and returns its bytes after the checks C1 made again
// on this open: a regular file of the size C1 saw, the sidecar's digest, and the SHA-256 of what was read
// equal to the id, and the name still the same file afterwards. Anything else is attach_changed: the file was swapped or removed after the first check,
// and nothing is sent.
func (h *BoardHandler) loadAttachment(a attachedArtifact) ([]byte, *attachRefusal) {
	if !objectNameRe.MatchString(a.ID) { // an id read from a file or a message is a 64-digit hex name before it is joined onto a folder (#5305)
		return nil, &attachRefusal{http.StatusConflict, "attach_changed", "coral://artifacts/ is not an artifact id"}
	}
	shown := "coral://artifacts/" + a.ID
	changed := &attachRefusal{http.StatusConflict, "attach_changed", fmt.Sprintf("%s changed while the send was being prepared; nothing was sent", shown)}
	dir := h.artifactObjectDir()
	if dir == "" {
		return nil, changed
	}
	path := filepath.Join(dir, a.ID)
	f, err := secfile.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, changed
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != a.Size || a.Size < 1 || a.Size > maxCrossTeamAttachBytes {
		return nil, changed
	}
	if !strings.EqualFold(h.objectSidecarDigest(a.ID), "sha256:"+a.ID) {
		return nil, changed
	}
	if attachBeforeLoad != nil {
		attachBeforeLoad(path)
	}
	data, err := io.ReadAll(io.LimitReader(f, a.Size+1))
	if err != nil || int64(len(data)) != a.Size {
		return nil, changed
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != a.ID {
		return nil, changed
	}
	if attachAfterRead != nil {
		attachAfterRead(path)
	}
	// The name must still be this very file when the read is done: an object the Operator deleted, or one
	// replaced by a link or another file, during the read refuses the send (the open handle alone would
	// keep serving the bytes of a file that is already gone).
	if now, err := os.Lstat(path); err != nil || !now.Mode().IsRegular() || !os.SameFile(info, now) {
		return nil, changed
	}
	return data, nil
}

// attachLooksLikeText: the receiver's gate would call it text (not an image, a PDF, or a refused kind).
func attachLooksLikeText(data []byte) bool {
	head := data
	if len(head) > gateHead {
		head = head[:gateHead]
	}
	tail := data
	if len(tail) > gateTail {
		tail = tail[len(tail)-gateTail:]
	}
	return refusedKind(head, tail) == "" && sniffImageFamily(head) == "" && !bytes.HasPrefix(head, []byte("%PDF-")) &&
		looksLikeText(head) && textThroughout(data)
}

// scanAttachment refuses a text file with a secret in it (a token or key by the same rules that redact
// Coral Hub's replies, in the file and in a few re-encodings of it: scanViews), or one that could not be
// checked in time: a file that leaves this computer is not redacted by the hub. Images, PDFs and other binary types are not scanned (the --attach help says so).
func scanAttachment(ctx context.Context, a attachedArtifact, data []byte) *attachRefusal {
	if !attachLooksLikeText(data) {
		return nil
	}
	shown := "coral://artifacts/" + a.ID
	release, err := acquireScanSlot(ctx)
	if err != nil {
		return &attachRefusal{http.StatusServiceUnavailable, "attach_busy", fmt.Sprintf("%s was not sent: another file is being checked for secrets on this computer; try again in a minute", shown)}
	}
	defer release()
	found, why := scanSecretsWhy(data)
	switch found {
	case scanUnchecked:
		return &attachRefusal{http.StatusUnprocessableEntity, "attach_unchecked", fmt.Sprintf("%s could not be checked for secrets in time, so it was not sent; send a smaller file", shown)}
	case scanFound:
		return &attachRefusal{http.StatusUnprocessableEntity, "attach_secret", fmt.Sprintf("%s looks like it contains a secret (a token or a key)%s, so it was not sent; remove it and upload the file again", shown, why.where())}
	}
	return nil
}

// relayFileName is a display name the hub and the receiver accept: at most 200 bytes of valid UTF-8 with
// no control or format character; the receiver reduces it further and takes the extension from the type.
func relayFileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == utf8.RuneError || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > 200 {
			break
		}
		b.WriteRune(r)
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		return s
	}
	return "artifact"
}

// relayMediaType is the declared type as a bare token (advisory only: the receiver sniffs).
func relayMediaType(mt string) string {
	mt, _, _ = strings.Cut(mt, ";")
	if mt = strings.TrimSpace(mt); relayMediaTypeRe.MatchString(mt) {
		return mt
	}
	return "application/octet-stream"
}

// relayArtifactFrame is the request body Coral Hub reads: a 4-byte length, the metadata, then the file.
func relayArtifactFrame(messageID, toAddress, fromBoard, fromCrossTeamID string, a attachedArtifact, data []byte) ([]byte, error) {
	fields := map[string]any{
		"message_id": messageID, "to_address": toAddress, "from_board": fromBoard,
		"digest": "sha256:" + a.ID, "size": len(data), "name": relayFileName(a.Name), "media_type": relayMediaType(a.MediaType),
	}
	if board.ValidCrossTeamID(fromCrossTeamID) { // this Coral's own board row's id (#5508); the receiver checks it against its list
		fields["from_cross_team_id"] = fromCrossTeamID
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	if len(meta) > maxRelayMeta {
		return nil, errors.New("metadata too large")
	}
	out := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(meta)+len(data)), uint32(len(meta)))
	return append(append(out, meta...), data...), nil
}

// artifactHubAnswer is the part of Coral Hub's answer to a file Coral reads.
type artifactHubAnswer struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	Limit string `json:"limit"`
}

// attachPrepared is a send's attachments, checked, scanned and held.
type attachPrepared struct {
	atts []attachedArtifact
	data [][]byte
	// auto is how many of the last atts were attached from links in the message's text, not asked for with --attach (#5501): they are sent after the others, and one the hub refuses is
	// dropped (its link stays in the warning) instead of stopping the message.
	auto int
}

// prepareRelayAttachments does everything about the attachments that can be done before a byte leaves:
// the sending board's switch, C1's resolution and limits, the second open and digest check, and the
// secret scan. On a refusal it has written the answer.
func (h *BoardHandler) prepareRelayAttachments(w http.ResponseWriter, r *http.Request, project, subscriber, sessionName string, raw []string) (*attachPrepared, bool) {
	ctx := r.Context()
	settings, err := h.bs.GetBoardSettings(ctx, project)
	if err != nil {
		errInternalServer(w, err.Error())
		return nil, false
	}
	if !settings.CrossTeamArtifacts {
		relayError(w, http.StatusForbidden, "artifacts_off", fmt.Sprintf("sending files is off on board %q; the Operator can turn on cross_team_artifacts in Team settings on this computer", project), 0)
		return nil, false
	}
	if h.relayArtifactSender() == nil {
		relayUnavailable(w, cloud.ErrNotLinked)
		return nil, false
	}
	sender := h.attachSenderFor(ctx, r, project, subscriber, sessionName)
	atts, refused := h.resolveAttachments(raw, sender)
	if refused != nil {
		refused.write(w)
		return nil, false
	}
	p := &attachPrepared{atts: atts}
	for _, a := range atts {
		data, refused := h.loadAttachment(a)
		if refused == nil {
			refused = scanAttachment(ctx, a, data)
		}
		if refused != nil {
			refused.write(w)
			return nil, false
		}
		p.data = append(p.data, data)
	}
	return p, true
}

// sendRelayAttachments pushes each file, in order, and stops at the first failure with the answer written:
// which attachment and why, never text from the hub. It returns false after a refusal. refundable reports
// that nothing reached Coral Hub (not linked, or files not relayed), so the send's rate token is given back.
func (h *BoardHandler) sendRelayAttachments(w http.ResponseWriter, ctx context.Context, p *attachPrepared, messageID, toAddress, fromBoard string) (ok, refundable bool) {
	send := h.relayArtifactSender()
	for i, a := range p.atts {
		which := fmt.Sprintf("attachment %d of %d (coral://artifacts/%s)", i+1, len(p.atts), a.ID)
		retry := fmt.Sprintf("; nothing after it was sent and the message was not sent: retry with --message-id %s", messageID)
		// The switch is read again before each file: the Operator may turn it off while earlier files
		// are on their way, and the files already held in memory must not follow (#4440).
		if on, err := h.bs.GetBoardSettings(ctx, fromBoard); err != nil || !on.CrossTeamArtifacts {
			relayError(w, http.StatusForbidden, "artifacts_off", fmt.Sprintf("sending files was turned off on board %q while the files were being sent; %s was not sent and the message was not sent", fromBoard, which), 0)
			return false, false
		}
		ownID, _ := h.senderIDOf(ctx, fromBoard)
		frame, err := relayArtifactFrame(messageID, toAddress, fromBoard, ownID, a, p.data[i])
		if err != nil {
			relayError(w, http.StatusInternalServerError, "attach_frame", which+" could not be prepared", 0)
			return false, false
		}
		resp, err := send(ctx, int64(len(frame)), bytes.NewReader(frame))
		if err != nil {
			switch {
			case errors.Is(err, cloud.ErrNotLinked):
				relayError(w, http.StatusServiceUnavailable, "not_linked", "this Coral isn't connected to Coral Hub", 0)
				return false, true
			case errors.Is(err, tunnel.ErrRelayArtifactUnsupported):
				relayError(w, http.StatusNotImplemented, "relay_unsupported", "Coral Hub doesn't relay files for this server (its file relay is switched off, or the hub is too old); the message was not sent", 0)
				return false, true
			}
			relayError(w, http.StatusBadGateway, "hub_unreachable", which+" could not be sent to Coral Hub"+retry, 0)
			return false, false
		}
		var ans artifactHubAnswer
		_ = json.Unmarshal(resp.Body, &ans)
		if resp.Status == http.StatusOK && ans.OK {
			continue
		}
		code := ans.Error
		if !hubErrorCodeRe.MatchString(code) {
			code = "hub_error"
		}
		status := resp.Status
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		var why string
		switch {
		case status == http.StatusTooManyRequests:
			code = "artifact_quota"
			why = "Coral Hub's limit on files is used up"
			if hubErrorCodeRe.MatchString(ans.Limit) {
				why += " (limit: " + ans.Limit + ")"
			}
			if resp.RetryAfter > 0 {
				why += fmt.Sprintf("; retry in %d s", resp.RetryAfter)
			}
			why += "; a retry resends the files the hub already has"
		case code == "artifacts_off":
			why = "files are off for this address (the receiving board, or the account's file switch in Coral Hub)"
		case code == "not_found":
			why = "there is no such address, or no team link to it"
		case code == "artifact_too_large":
			why = "the file is over the limit for one file"
		default:
			why = "Coral Hub refused the file (" + code + ")"
		}
		relayError(w, status, code, which+": "+why+retry, resp.RetryAfter)
		return false, false
	}
	return true, false
}

// recordSharedAttachments writes the sender's shared rows, once the hub has the files and the message.
func (h *BoardHandler) recordSharedAttachments(atts []attachedArtifact, project, server, team, by string, messageID int64, at string) {
	for _, a := range atts {
		_ = h.writeCrossTeamRecord("shared", crossTeamArtifactRecord{
			ID: a.ID, Name: a.Name, MediaType: a.MediaType, Size: a.Size,
			Board: project, OtherBoard: team, Server: server, By: by, MessageID: messageID, At: at,
		})
	}
}

// maxLinkedConsidered is how many distinct linked files one send looks at: the send takes at most maxCrossTeamAttachments, so a few more than that is enough to skip some that cannot go.
const maxLinkedConsidered = 20

// linkedArtifactIDs are the ids of the coral://artifacts and /api/artifacts links in text, in order, once each.
func linkedArtifactIDs(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range artifactLinkRe.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// autoAttachLinked attaches the files a message's text names by link (#5501), after the ones --attach named, so that the other Coral gets what the text points at. Only when the sending
// board's cross_team_artifacts switch is on and this Coral can relay files; each file goes through the very checks --attach does (the team's own upload, the stored object, 8 MiB, the
// hourly upload window, the secret scan), within the same limits for the whole send (4 files, 16 MiB, the --attach ones counted). A link that fails any of them is simply not attached: it
// stays in the reply's unattached_links warning (#5499), and the send is never refused for it.
func (h *BoardHandler) autoAttachLinked(ctx context.Context, r *http.Request, project string, b *relayRequest, prepared *attachPrepared) *attachPrepared {
	ids := linkedArtifactIDs(b.Content)
	if len(ids) == 0 {
		return prepared
	}
	if len(ids) > maxLinkedConsidered { // a text of thousands of links must not cost a store lookup for each (#5504 review): the rest only stay in the warning
		ids = ids[:maxLinkedConsidered]
	}
	settings, err := h.bs.GetBoardSettings(ctx, project)
	if err != nil || !settings.CrossTeamArtifacts || h.relayArtifactSender() == nil {
		return prepared
	}
	var have map[string]bool
	var count int
	var total int64
	if prepared != nil {
		have = map[string]bool{}
		for _, a := range prepared.atts {
			have[a.ID] = true
			total += a.Size
		}
		count = len(prepared.atts)
	}
	sender := h.attachSenderFor(ctx, r, project, b.SubscriberID, b.SessionName)
	var added *attachPrepared
	for _, id := range ids {
		if have[id] {
			continue
		}
		if count >= maxCrossTeamAttachments {
			break
		}
		meta, ok := h.objectMeta(id)
		if !ok || meta.Size < 1 || meta.Size > maxCrossTeamAttachBytes || total+meta.Size > maxCrossTeamAttachTotal {
			continue
		}
		atts, refused := h.resolveAttachments([]string{"coral://artifacts/" + id}, sender)
		if refused != nil || len(atts) != 1 {
			continue
		}
		// The size that counts is the one read from the file itself (the sidecar's is only a first filter).
		if atts[0].Size > maxCrossTeamAttachBytes || total+atts[0].Size > maxCrossTeamAttachTotal {
			continue
		}
		data, refused := h.loadAttachment(atts[0])
		if refused == nil {
			refused = scanAttachment(ctx, atts[0], data)
		}
		if refused != nil {
			continue
		}
		if added == nil {
			added = &attachPrepared{}
		}
		added.atts = append(added.atts, atts[0])
		added.data = append(added.data, data)
		total += atts[0].Size
		count++
	}
	if added == nil {
		return prepared
	}
	if prepared == nil {
		prepared = &attachPrepared{}
	}
	prepared.atts = append(prepared.atts, added.atts...)
	prepared.data = append(prepared.data, added.data...)
	prepared.auto = len(added.atts)
	return prepared
}

// discardResponse is a response writer that keeps nothing: the answer sendRelayAttachments would write for a file the send can do without.
type discardResponse struct{ h http.Header }

func (d *discardResponse) Header() http.Header {
	if d.h == nil {
		d.h = http.Header{}
	}
	return d.h
}
func (d *discardResponse) Write(p []byte) (int, error) { return len(p), nil }
func (d *discardResponse) WriteHeader(int)             {}

// sendAttachments sends a send's files, in order, to Coral Hub: the ones --attach named must all go (the first failure ends the send with its answer written, as before), the ones
// attached from links in the text (prepared.auto, last) are each tried alone, and one the hub does not take is dropped from prepared, not a reason to stop the message.
func (h *BoardHandler) sendAttachments(w http.ResponseWriter, ctx context.Context, p *attachPrepared, messageID, toAddress, fromBoard string) (ok, refundable bool) {
	explicit := len(p.atts) - p.auto
	if p.auto == 0 {
		return h.sendRelayAttachments(w, ctx, p, messageID, toAddress, fromBoard)
	}
	if explicit > 0 {
		head := &attachPrepared{atts: p.atts[:explicit], data: p.data[:explicit]}
		if ok, refundable := h.sendRelayAttachments(w, ctx, head, messageID, toAddress, fromBoard); !ok {
			return false, refundable
		}
	}
	keptAtts, keptData := append([]attachedArtifact(nil), p.atts[:explicit]...), append([][]byte(nil), p.data[:explicit]...)
	for i := explicit; i < len(p.atts); i++ {
		one := &attachPrepared{atts: p.atts[i : i+1], data: p.data[i : i+1]}
		if ok, _ := h.sendRelayAttachments(&discardResponse{}, ctx, one, messageID, toAddress, fromBoard); ok {
			keptAtts, keptData = append(keptAtts, p.atts[i]), append(keptData, p.data[i])
		}
	}
	p.auto = len(keptAtts) - explicit
	p.atts, p.data = keptAtts, keptData
	return true, false
}
