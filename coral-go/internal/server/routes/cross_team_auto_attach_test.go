package routes

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/tunnel"
)

// #5501: a cross-team send whose text names coral://artifacts (or /api/artifacts) links ATTACHES those files automatically, within --attach's limits and checks, only when the
// cross-team artifacts switch is on; a link that cannot be attached keeps #5499's warning, and the send is never refused for it.

func TestRelaySendAttachesTheFilesTheTextLinks(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	a := e.upload(alphaOrchSID, "# alpha report\nnumbers\n")
	b := e.upload(alphaOrchSID, "# second file\nmore\n")
	code, out, _ := e.relayText(alphaOrchSID, "m1", "the report is "+uri(a)+" and the log /api/artifacts/"+b+"?v=2, see "+uri(a)+" again")
	require.Equal(t, http.StatusOK, code, out)
	events, files, msgs := e.wire.snapshot()
	assert.Equal(t, []string{"file:sha256:" + a, "file:sha256:" + b, "message"}, events, "each linked file once, in the order of the text, then the message")
	assert.Equal(t, 1, msgs)
	require.Len(t, files, 2)
	assert.Equal(t, []any{uri(a), uri(b)}, out["attached"])
	assert.Equal(t, []any{uri(a), uri(b)}, out["auto_attached"])
	assert.NotContains(t, out, "warnings", "everything the text names went")
	assert.Len(t, e.sharedRows(), 2, "the shared rows are written as for --attach")
}

// --attach files go first and count against the limits; a link in the text for one of them is not sent twice.
func TestRelaySendAutoAttachFollowsExplicitAttachAndDoesNotDuplicate(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	a := e.upload(alphaOrchSID, "# one\n")
	b := e.upload(alphaOrchSID, "# two\n")
	code, out, _ := e.relayText(alphaOrchSID, "m1", "files "+uri(a)+" and "+uri(b), uri(a))
	require.Equal(t, http.StatusOK, code, out)
	events, _, _ := e.wire.snapshot()
	assert.Equal(t, []string{"file:sha256:" + a, "file:sha256:" + b, "message"}, events)
	assert.Equal(t, []any{uri(a), uri(b)}, out["attached"])
	assert.Equal(t, []any{uri(b)}, out["auto_attached"], "only the one --attach did not name")
}

func TestRelaySendAutoAttachIsOffWithTheSwitchOff(t *testing.T) {
	e := newRelayAttachEnv(t, false)
	a := e.upload(alphaOrchSID, "# alpha report\n")
	code, out, _ := e.relayText(alphaOrchSID, "m1", "see "+uri(a))
	require.Equal(t, http.StatusOK, code, out)
	events, files, msgs := e.wire.snapshot()
	assert.Equal(t, []string{"message"}, events, "no file left")
	assert.Empty(t, files)
	assert.Equal(t, 1, msgs, "the message is sent")
	assert.NotContains(t, out, "attached")
	assert.Equal(t, []any{uri(a)}, out["unattached_links"], "the warning stays")
	assert.Empty(t, e.sharedRows())
}

// A link that is not an upload of this team, or is not in this store, is not attached and is named in the warning; the send goes.
func TestRelaySendAutoAttachSkipsWhatItMayNotOrCannotSend(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	mine := e.upload(alphaOrchSID, "# mine\n")
	theirs := e.upload(betaOrchSID, "# beta's upload, not alpha's\n")
	missing := strings.Repeat("e", 64)
	code, out, _ := e.relayText(alphaOrchSID, "m1", "mine "+uri(mine)+", theirs "+uri(theirs)+", missing "+uri(missing))
	require.Equal(t, http.StatusOK, code, out)
	events, _, msgs := e.wire.snapshot()
	assert.Equal(t, []string{"file:sha256:" + mine, "message"}, events)
	assert.Equal(t, 1, msgs)
	assert.Equal(t, []any{uri(mine)}, out["auto_attached"])
	assert.Equal(t, []any{uri(theirs), uri(missing)}, out["unattached_links"])
	assert.Equal(t, []any{"unattached_artifact_links"}, out["warnings"])
}

// 4 files in all: the fifth link is only warned about; --attach files count.
func TestRelaySendAutoAttachStopsAtTheFileLimit(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	var ids []string
	text := "files"
	for _, c := range []string{"one", "two", "three", "four", "five"} {
		id := e.upload(alphaOrchSID, "# "+c+" file\n")
		ids = append(ids, id)
		text += " " + uri(id)
	}
	code, out, _ := e.relayText(alphaOrchSID, "m1", text)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []any{uri(ids[0]), uri(ids[1]), uri(ids[2]), uri(ids[3])}, out["attached"])
	assert.Equal(t, []any{uri(ids[4])}, out["unattached_links"])

	// With two --attach files the text adds only two more.
	e2 := newRelayAttachEnv(t, true)
	var ex []string
	for _, c := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
		ex = append(ex, e2.upload(alphaOrchSID, "# "+c+" doc\n"))
	}
	code, out, _ = e2.relayText(alphaOrchSID, "m2", "all "+uri(ex[0])+" "+uri(ex[1])+" "+uri(ex[2])+" "+uri(ex[3])+" "+uri(ex[4]), uri(ex[4]), uri(ex[3]))
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []any{uri(ex[4]), uri(ex[3]), uri(ex[0]), uri(ex[1])}, out["attached"], "the --attach ones first, then the text's in its order, 4 in all")
	assert.Equal(t, []any{uri(ex[2])}, out["unattached_links"])
}

// 8 MiB per file and 16 MiB per send: a file over the first, or one that would pass the second, is only warned about.
func TestRelaySendAutoAttachKeepsTheSizeLimits(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	big := e.upload(alphaOrchSID, "# big\n"+strings.Repeat("x", 9<<20))
	s1 := e.upload(alphaOrchSID, "# six one\n"+strings.Repeat("a", 6<<20))
	s2 := e.upload(alphaOrchSID, "# six two\n"+strings.Repeat("b", 6<<20))
	s3 := e.upload(alphaOrchSID, "# six three\n"+strings.Repeat("c", 6<<20))
	code, out, _ := e.relayText(alphaOrchSID, "m1", "big "+uri(big)+" then "+uri(s1)+" "+uri(s2)+" "+uri(s3))
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []any{uri(s1), uri(s2)}, out["attached"], "12 MiB: the third 6 MiB would pass the 16 MiB total")
	assert.Equal(t, []any{uri(big), uri(s3)}, out["unattached_links"])
}

// A file the hub does not take is dropped, not a reason to lose the message; a --attach file the hub refuses still ends the send as before.
func TestRelaySendAutoAttachedFileTheHubRefusesDoesNotStopTheMessage(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	a := e.upload(alphaOrchSID, "# alpha one\n")
	b := e.upload(alphaOrchSID, "# alpha two\n")
	c := e.upload(alphaOrchSID, "# alpha three\n")
	e.wire.fileAnswer = func(i int, f sentFile) (tunnel.RelayResponse, error) {
		if asString(f.Meta["digest"]) == "sha256:"+b {
			return tunnel.RelayResponse{Status: 403, Body: []byte(`{"ok":false,"error":"artifacts_off"}`)}, nil
		}
		return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":true}`)}, nil
	}
	code, out, _ := e.relayText(alphaOrchSID, "m1", "see "+uri(a)+" "+uri(b)+" "+uri(c))
	require.Equal(t, http.StatusOK, code, out)
	_, _, msgs := e.wire.snapshot()
	assert.Equal(t, 1, msgs, "the message went")
	assert.Equal(t, []any{uri(a), uri(c)}, out["attached"])
	assert.Equal(t, []any{uri(b)}, out["unattached_links"])
	assert.Len(t, e.sharedRows(), 2, "only what the hub took is recorded as shared")

	// The same refusal of a file asked for with --attach is the send's refusal, as before.
	e2 := newRelayAttachEnv(t, true)
	d := e2.upload(alphaOrchSID, "# alpha four\n")
	e2.wire.fileAnswer = func(int, sentFile) (tunnel.RelayResponse, error) {
		return tunnel.RelayResponse{Status: 403, Body: []byte(`{"ok":false,"error":"artifacts_off"}`)}, nil
	}
	code, out, _ = e2.relayText(alphaOrchSID, "m2", "see "+uri(d), uri(d))
	assert.NotEqual(t, http.StatusOK, code, out)
	_, _, msgs = e2.wire.snapshot()
	assert.Zero(t, msgs, "no message when an explicit file is refused")
}

// All auto files refused: the message still goes, with no attached list.
func TestRelaySendWithOnlyRefusedAutoFilesIsAPlainMessage(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	a := e.upload(alphaOrchSID, "# alpha solo\n")
	e.wire.fileAnswer = func(int, sentFile) (tunnel.RelayResponse, error) {
		return tunnel.RelayResponse{Status: 403, Body: []byte(`{"ok":false,"error":"artifacts_off"}`)}, nil
	}
	code, out, _ := e.relayText(alphaOrchSID, "m1", "see "+uri(a))
	require.Equal(t, http.StatusOK, code, out)
	assert.NotContains(t, out, "attached")
	assert.NotContains(t, out, "auto_attached")
	assert.Equal(t, []any{uri(a)}, out["unattached_links"])
	_, _, msgs := e.wire.snapshot()
	assert.Equal(t, 1, msgs)
	assert.Empty(t, e.sharedRows())
}

// #5504 (Security): a file with a secret in it is never sent because the text links it (the scan --attach does applies), and a text of very many links is looked at only so far.
func TestRelaySendAutoAttachSkipsASecretFileAndBoundsTheLinksConsidered(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	token := "ghp_" + "1a2B3c4D5e6F7g8H9i0J1k2L3m4N5o6P7q8R"
	dirty := e.upload(alphaOrchSID, "# notes\ndeploy with token="+token+"\n")
	clean := e.upload(alphaOrchSID, "# clean notes\n")
	code, out, _ := e.relayText(alphaOrchSID, "m1", "dirty "+uri(dirty)+" clean "+uri(clean))
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []any{uri(clean)}, out["auto_attached"], "only the file that passes the scan goes")
	assert.Equal(t, []any{uri(dirty)}, out["unattached_links"])
	events, _, _ := e.wire.snapshot()
	assert.Equal(t, []string{"file:sha256:" + clean, "message"}, events)

	// 60 links to nothing, then a real one: only the first maxLinkedConsidered distinct links are looked at, so the real one at the end is only warned about.
	e2 := newRelayAttachEnv(t, true)
	real := e2.upload(alphaOrchSID, "# real\n")
	text := ""
	for i := 0; i < 60; i++ {
		text += " coral://artifacts/" + fmt.Sprintf("%064x", i+1)
	}
	text += " " + uri(real)
	code, out, _ = e2.relayText(alphaOrchSID, "m2", text)
	require.Equal(t, http.StatusOK, code, out)
	assert.NotContains(t, out, "auto_attached")
	events2, _, _ := e2.wire.snapshot()
	assert.Equal(t, []string{"message"}, events2, "the real file at the end was not sent: it is past the links looked at")
}
