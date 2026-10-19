// Redacting a forwarded response: the streaming and whole-JSON paths and their budgets (moved out of local.go, #5091; no behaviour change).

package tunnel

import (
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"

	"github.com/cdknorow/coral/pkg/redact"
)

func (l *LocalHandler) redacting() bool { return l.RedactTokens == nil || l.RedactTokens() }

func (l *LocalHandler) redactor() redact.Redactor {
	if l.Redactor == nil {
		return redact.Refuse
	}
	return l.Redactor
}

// redactFailed is the whole answer when a reply couldn't be scanned: it never passes unscanned.
const redactFailed = `{"error":"this reply could not be checked for secrets, so it is not shown through Coral Hub; open it on the computer"}` + "\n"

var redactFailLog sync.Once

// serveRedacted relays a View reply with its tokens replaced (#3560). A reply of known size up
// to wholeReplyMax goes in one write, as unredacted ones do; a larger, open-ended or decoded one
// streams through the redactor's Stream, which holds back only what it can't check yet. Range
// is dropped per route, not per content type, so a binary reply on a redacted View route (an
// image the agent's page serves, say) is a 200 with the whole body: accepted (Security's #3560
// note 2), since only artifact downloads need byte ranges and they are exempt.
func (l *LocalHandler) serveRedacted(s *Stream, resp *http.Response, body io.Reader, hdr http.Header, decoded bool, progress func()) {
	// The whole-reply path reads exactly Content-Length bytes, which for a decoded (gzip) reply is
	// the compressed size, not the body's: such a reply streams instead (#3564, as the unredacted
	// path does).
	if resp.ContentLength >= 0 && resp.ContentLength <= wholeReplyMax && !decoded {
		raw := make([]byte, resp.ContentLength)
		if _, err := io.ReadFull(body, raw); err != nil {
			s.Reject(CodeUpstream, "local response ended early", 0)
			return
		}
		out, ok := l.redactReply(resp.Header, raw)
		if !ok || !redactedJSONOK(resp.Header, raw, out) {
			l.respondRedactFailed(s)
			return
		}
		_ = s.RespondWhole(ResponseHeader{Status: resp.StatusCode, Headers: hdr}, out)
		return
	}
	if jsonReply(resp.Header) {
		// A JSON reply is held whole, redacted and checked before any of it is sent (#3694): one
		// that parsed before redaction and doesn't after is never passed on, it becomes the
		// could-not-be-checked answer. A JSON reply is never a live stream, so holding it costs
		// only the wait for its last byte. It fails closed (#3718, coral-hub Security): a reply
		// over jsonWholeMax is refused (never streamed unchecked), and one that can't get room in
		// the process-wide jsonBudget is answered busy, to retry.
		// The reservation is given back as soon as the bytes it covers are no longer held, before
		// the answer that ends the request: a caller that has the answer finds the budget back
		// (#4136: the deferred release ran after the hub had the reply, and a check right after it
		// saw the budget still taken), and a slow hub link doesn't keep it from other replies.
		raw, release, why := readJSONBudgeted(body, resp.ContentLength, decoded, progress)
		defer release()
		switch why {
		case jsonTooLarge:
			release()
			respondJSONTooLarge(s)
			return
		case jsonNoBudget:
			release()
			respondJSONBusy(s)
			return
		case jsonReadFailed:
			release()
			s.Reject(CodeUpstream, "local response ended early", 0)
			return
		}
		out, ok := l.redactReply(resp.Header, raw)
		if !ok || !redactedJSONOK(resp.Header, raw, out) {
			raw, out = nil, nil
			release()
			l.respondRedactFailed(s)
			return
		}
		w, err := s.Respond(ResponseHeader{Status: resp.StatusCode, Headers: hdr})
		if err != nil {
			return
		}
		for len(out) > 0 {
			n := min(len(out), maxChunk)
			if _, err := w.Write(out[:n]); err != nil {
				return
			}
			out = out[n:]
			progress()
		}
		raw = nil
		release() // every byte is written: nothing of the reply is held any more
		_ = w.Close()
		return
	}
	w, err := s.Respond(ResponseHeader{Status: resp.StatusCode, Headers: hdr})
	if err != nil {
		return
	}
	w = guardEventStream(s, resp.Header, w)
	red := l.redactor().NewStream(w)
	buf := make([]byte, maxChunk)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if !redactWrite(red, buf[:n]) {
				return // the scanner failed mid-stream: leave it unterminated, so the hub sees a truncated reply
			}
			progress()
		}
		if rerr != nil {
			if rerr == io.EOF && redactClose(red) {
				_ = w.Close()
			}
			return
		}
	}
}

// jsonWholeMax bounds one JSON reply serveRedacted holds to check it after redaction (#3718:
// 2 MiB, from 16). Real replies are far below it: the largest 100-message chat page measured on
// 277 MB, 199 MB and 172 MB transcripts was 193 KB. A bigger reply is refused, never streamed
// unchecked; the dashboard pages what could be bigger through Coral Hub (the history transcript,
// the board's export).
var jsonWholeMax = 2 << 20

// jsonBudget bounds the bytes held by every JSON reply being checked at once: each holds its
// reply and the redacted copy, counted as they are read. Worst case held: 32 MiB, whatever the
// pools' sizes (it was a reply per stream times the cap, 2 GiB). A reply that can't get room is
// answered busy, to retry (respondJSONBusy).
var jsonBudget = newByteBudget(32 << 20)

// jsonInitialHold is what a JSON reply of unknown length reserves before its first byte (twice
// this, for the reply and its copy): more than any chat page, so a typical reply takes the budget
// once and never grows; a bigger one grows as it arrives. Reserving up front keeps replies that
// each hold part of the budget from starving one another. With the defaults about 64 typical
// replies fit at once (the general pool's size).
var jsonInitialHold = 256 << 10

// byteBudget is a count of bytes in use against a limit; take never waits.
type byteBudget struct {
	mu        sync.Mutex
	used, max int64
}

func newByteBudget(max int64) *byteBudget { return &byteBudget{max: max} }

// take reserves n bytes if they fit, and reports whether they did.
func (b *byteBudget) take(n int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used+n > b.max {
		return false
	}
	b.used += n
	return true
}

func (b *byteBudget) give(n int64) {
	b.mu.Lock()
	b.used -= n
	b.mu.Unlock()
}

type jsonReadResult int

const (
	jsonReadOK jsonReadResult = iota
	jsonTooLarge
	jsonNoBudget
	jsonReadFailed
)

// readJSONBudgeted reads a JSON reply whole, with room reserved in jsonBudget for it and its
// redacted copy (twice its bytes): for a reply of known length exactly that, before its first
// byte; for one of unknown length jsonInitialHold to start with, then more as it outgrows it. A
// reply of known length over jsonWholeMax is refused before a byte is read, one that turns out
// over it while being read too. release gives back everything reserved; the caller defers it on
// every path.
func readJSONBudgeted(body io.Reader, contentLength int64, decoded bool, progress func()) (raw []byte, release func(), why jsonReadResult) {
	var held int64
	release = func() {
		if held > 0 {
			jsonBudget.give(held)
			held = 0
		}
	}
	if contentLength > int64(jsonWholeMax) && !decoded {
		return nil, release, jsonTooLarge
	}
	first := int64(min(jsonInitialHold, jsonWholeMax))
	if contentLength >= 0 && !decoded {
		first = contentLength
	}
	if !jsonBudget.take(2 * first) {
		return nil, release, jsonNoBudget
	}
	held = 2 * first
	buf := make([]byte, maxChunk)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if len(raw)+n > jsonWholeMax {
				return nil, release, jsonTooLarge
			}
			if need := 2*int64(len(raw)+n) - held; need > 0 {
				if !jsonBudget.take(need) {
					return nil, release, jsonNoBudget
				}
				held += need
			}
			raw = append(raw, buf[:n]...)
			progress()
		}
		if err == io.EOF {
			return raw, release, jsonReadOK
		}
		if err != nil {
			return nil, release, jsonReadFailed
		}
	}
}

// respondJSONTooLarge refuses a JSON reply over jsonWholeMax: never sent unchecked (#3718).
func respondJSONTooLarge(s *Stream) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	_ = s.RespondWhole(ResponseHeader{Status: http.StatusBadGateway, Headers: h},
		[]byte(`{"error":"this reply is too large to be checked for secrets through Coral Hub; open it on the computer","code":"hub_reply_too_large"}`))
}

// respondJSONBusy answers a JSON reply that can't get room in jsonBudget: busy, to retry, as a
// full pool is answered (busy), but 503 (the computer is busy, not the caller over a limit).
func respondJSONBusy(s *Stream) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Retry-After", busyRetryAfter)
	h.Set(LimitHeader, "connector")
	h.Set("Cache-Control", "no-store")
	_ = s.RespondWhole(ResponseHeader{Status: http.StatusServiceUnavailable, Headers: h},
		[]byte(`{"error":"this computer is checking too many Coral Hub replies at once; try again shortly","code":"hub_busy"}`))
}

// jsonReply reports whether a reply's Content-Type is JSON (application/json or a +json type).
// It goes by the declared type: a JSON body sent as text/plain is streamed through the
// redactor's stream unchecked (Coral's own JSON endpoints all declare application/json).
func jsonReply(hdr http.Header) bool {
	mt, _, err := mime.ParseMediaType(hdr.Get("Content-Type"))
	return err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json"))
}

// redactReply redacts a reply held whole: by its structure when hdr declares JSON. A panic is a
// failure, so a reply is never sent unchecked.
func (l *LocalHandler) redactReply(hdr http.Header, raw []byte) (out []byte, ok bool) {
	defer func() {
		if recover() != nil {
			out, ok = nil, false
		}
	}()
	out, err := l.redactor().Reply(raw, jsonReply(hdr))
	return out, err == nil
}

// redactedJSONOK is false when a JSON reply that parsed before redaction doesn't after it (#3694:
// the redactor must never be what breaks a reply). A reply that didn't parse to begin with is
// the upstream's own, and is passed redacted as it is.
func redactedJSONOK(hdr http.Header, raw, out []byte) bool {
	return !jsonReply(hdr) || !json.Valid(raw) || json.Valid(out)
}

func (l *LocalHandler) respondRedactFailed(s *Stream) {
	redactFailLog.Do(func() { slog.Warn("[tunnel] a reply could not be checked for secrets; it was not sent to Coral Hub") })
	w, err := s.Respond(ResponseHeader{Status: http.StatusBadGateway, Headers: http.Header{"Content-Type": {"application/json"}}})
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, redactFailed)
	_ = w.Close()
}

// redactWrite and redactClose turn an error or a panic in the stream into a failure, so a reply
// is never sent unscanned.
func redactWrite(r redact.Stream, b []byte) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	_, err := r.Write(b)
	return err == nil
}

func redactClose(r redact.Stream) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return r.Close() == nil
}

// bodyless reports whether a response to method with status carries no body.
func bodyless(method string, status int) bool {
	return method == http.MethodHead || status == http.StatusNoContent || status == http.StatusNotModified || (status >= 100 && status < 200)
}

// respondUnsupportedEncoding answers a local response whose Content-Encoding the
// connector will not relay. It is an answer (502 with a JSON error), not a stream
// failure, so the hub does not take the machine for unreachable.
func respondUnsupportedEncoding(s *Stream) {
	w, err := s.Respond(ResponseHeader{Status: http.StatusBadGateway, Headers: http.Header{"Content-Type": {"application/json"}}})
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, `{"error":"unsupported content encoding"}`+"\n")
	_ = w.Close()
}
