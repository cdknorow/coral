package routes

import (
	"regexp"
	"testing"
)

// A refusal always answers a fixed snake_case code (bad_frame, artifacts_off, digest_mismatch,
// artifact_too_large, store_full, type_refused, ...), never the file's bytes.
var fixedRefusalCode = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// FuzzReceiveExternalArtifactFrame drives the whole receive handler (the 4-byte length, the metadata
// JSON, then exactly size bytes) with arbitrary frame bytes on an authentic, switched-on board, and
// holds the invariants the receiver promises (#4389, from QA's nightly-fuzz task): the handler never
// panics; an accepted frame (2xx) actually reached the shared store (a valid frame round-trips); a
// refused frame answers a FIXED code (a snake_case token, never the file's text) and leaves nothing in
// the object store (a refused file is never stored, not even briefly). This complements
// FuzzParseReceivedMeta, which fuzzes only the metadata object; here the whole frame and the gate run.
func FuzzReceiveExternalArtifactFrame(f *testing.F) {
	good := []byte("hello, cross-team world\n")
	f.Add(frame(meta(good, "hello.txt"), good))                                     // a valid frame: must round-trip to the store
	f.Add([]byte{})                                                                 // nothing
	f.Add([]byte{0, 0, 0, 0})                                                       // zero metadata length
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})                                           // metadata length over the cap
	f.Add(rawFrame([]byte(`{}`), nil))                                              // empty metadata object
	f.Add(rawFrame([]byte(`{"size":5}`), []byte("hi")))                             // body shorter than the declared size
	f.Add(frame(meta(good, "../../etc/passwd"), good))                              // a hostile name
	f.Add(frame(meta([]byte("\x00\x01binary"), "x.txt"), []byte("\x00\x01binary"))) // non-text bytes
	bad := map[string]any{"message_id": "m1", "to_address": "srv/beta", "from_board": "alpha",
		"digest": "sha256:" + "00" + "00", "size": len(good), "name": "h.txt", "media_type": "text/plain"}
	f.Add(frame(bad, good)) // a wrong digest

	f.Fuzz(func(t *testing.T, body []byte) {
		e := newArtifactEnv(t)
		e.open()
		// No panic in the handler's frame reading, the metadata parse, the gate, the digest or the store.
		code, out := e.send("auth", relaySrc, "", body)

		if code >= 200 && code < 300 {
			// Accepted: a valid frame must have reached the shared object store (round-trip), not just
			// been parsed. An accept that stored nothing would be a silent drop.
			if len(e.stored()) == 0 {
				t.Fatalf("status %d accepted the frame but stored nothing: %v", code, out)
			}
			return
		}

		// Refused: a fixed code (the "error" field), never the file's text, and nothing in the store.
		rc, _ := out["error"].(string)
		if !fixedRefusalCode.MatchString(rc) {
			t.Fatalf("refusal (status %d) without a fixed code: error=%q out=%v", code, rc, out)
		}
		if names := e.stored(); len(names) != 0 {
			t.Fatalf("refused with %q but %d object(s) are in the store: %v", rc, len(names), names)
		}
	})
}
