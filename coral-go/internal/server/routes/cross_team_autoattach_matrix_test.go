package routes

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The test matrix for #5501: a cross-team send whose TEXT names coral://artifacts/<hex> (or /api/artifacts/<hex>) links attaches those files by itself, within the limits of --attach (4 files, 8 MiB
// each, 16 MiB per send), only when the cross-team artifacts switch is on; a link over a limit, not in this store or not the sending team's keeps #5499's warning. It is two Corals end to end: the
// sender is the relay rig (relayAttachEnv: the real send route, a fake Coral Hub that records every file and message), and what the hub got is handed to a second, receiving Coral (artifactEnv: the
// real receive route) to see what it stores. Written before #5501: every case that expects an attachment is red until it lands; the rest hold now.
//
// What is asserted, per case: the files the hub received, in order and by digest; that the message went exactly once; the reply's warning fields (#5499: "warnings" and "unattached_links", canonical
// coral:// links, once each, in order); and what the receiver stored.

// autoSend sends text with no --attach at all.
func linkSend(e *relayAttachEnv, text string) (int, map[string]any) {
	e.t.Helper()
	code, out, _ := e.relayText(alphaOrchSID, "", text)
	return code, out
}

// hubDigests are the digests of the files the fake hub received, in order, without the "sha256:" prefix.
func hubDigests(e *relayAttachEnv) []string {
	_, files, _ := e.wire.snapshot()
	var ids []string
	for _, f := range files {
		ids = append(ids, strings.TrimPrefix(asString(f.Meta["digest"]), "sha256:"))
	}
	return ids
}

func messagesSent(e *relayAttachEnv) int { _, _, n := e.wire.snapshot(); return n }

// warned is the reply's list of links that went unattached, nil when there is no warning.
func warned(out map[string]any) []string {
	var links []string
	if ws, _ := out["warnings"].([]any); len(ws) > 0 {
		for _, l := range out["unattached_links"].([]any) {
			links = append(links, l.(string))
		}
	}
	return links
}

// receive hands every file the hub got to a receiving Coral that accepts them, and returns what that Coral stored (the object names; sidecars are not counted).
func receive(t *testing.T, e *relayAttachEnv) []string {
	t.Helper()
	r := newArtifactEnv(t)
	r.open()
	_, files, _ := e.wire.snapshot()
	for _, f := range files {
		status, out := r.send("auth", relaySrc, "", frame(f.Meta, f.Data))
		require.Equal(t, http.StatusOK, status, out)
	}
	r.h.WaitBackground()
	var objects []string
	for _, n := range r.stored() {
		if !strings.Contains(n, ".") {
			objects = append(objects, n)
		}
	}
	return objects
}

func TestLinkAutoAttachOneLink(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	a := e.upload(alphaOrchSID, "# the report\nnumbers\n")
	code, out := linkSend(e, "the report is "+uri(a)+", please read it")
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []string{a}, hubDigests(e), "the linked file was attached")
	assert.Equal(t, 1, messagesSent(e))
	assert.Nil(t, warned(out), "attached: nothing to warn about")
	assert.ElementsMatch(t, []string{a}, receive(t, e), "the receiver stored exactly that file")
}

func TestLinkAutoAttachFiveLinksTakesFourAndWarnsAboutTheFifth(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	var ids []string
	text := "files:"
	for i := 0; i < 5; i++ {
		id := e.upload(alphaOrchSID, strings.Repeat("f", 8)+string(rune('A'+i))+"\n")
		ids = append(ids, id)
		text += " " + uri(id)
	}
	code, out := linkSend(e, text)
	require.Equal(t, http.StatusOK, code, "a link over the limit is a warning, never a refusal of the message: %v", out)
	assert.Equal(t, ids[:4], hubDigests(e), "the first four links, in the order of the text")
	assert.Equal(t, 1, messagesSent(e))
	assert.Equal(t, []string{uri(ids[4])}, warned(out), "the fifth is named in the warning")
	assert.ElementsMatch(t, ids[:4], receive(t, e))
}

func TestLinkAutoAttachOneFileOverEightMiBIsNotAttachedAndIsWarned(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	big := e.upload(alphaOrchSID, strings.Repeat("b", 9<<20))
	small := e.upload(alphaOrchSID, "# small\n")
	code, out := linkSend(e, "big "+uri(big)+" small "+uri(small))
	require.Equal(t, http.StatusOK, code, "the message still goes: %v", out)
	assert.Equal(t, []string{small}, hubDigests(e), "only the file within 8 MiB was attached")
	assert.Equal(t, 1, messagesSent(e))
	assert.Equal(t, []string{uri(big)}, warned(out))
	assert.ElementsMatch(t, []string{small}, receive(t, e))
}

func TestLinkAutoAttachSixteenMiBPerSend(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	six := func(c string) string { return e.upload(alphaOrchSID, strings.Repeat(c, 6<<20)) }
	a, b, c := six("c"), six("d"), six("e")
	code, out := linkSend(e, uri(a)+" "+uri(b)+" "+uri(c))
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []string{a, b}, hubDigests(e), "12 MiB fit; the third would make 18 MiB")
	assert.Equal(t, []string{uri(c)}, warned(out))
	assert.Equal(t, 1, messagesSent(e))
}

func TestLinkAutoAttachSwitchOffSendsTheMessageAndWarns(t *testing.T) {
	e := newRelayAttachEnv(t, false)
	a := e.upload(alphaOrchSID, "# the report\n")
	code, out := linkSend(e, "see "+uri(a))
	require.Equal(t, http.StatusOK, code, "unlike an explicit --attach, a link in the text is not a refusal when the switch is off: %v", out)
	assert.Empty(t, hubDigests(e), "no file leaves with the switch off")
	assert.Equal(t, 1, messagesSent(e))
	assert.Equal(t, []string{uri(a)}, warned(out))
	assert.Empty(t, e.sharedRows())
}

func TestLinkAutoAttachALinkNotInTheStoreIsWarnedAndNothingElseChanges(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	ghost := strings.Repeat("9", 64)
	a := e.upload(alphaOrchSID, "# real\n")
	code, out := linkSend(e, uri(ghost)+" and "+uri(a))
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []string{a}, hubDigests(e))
	assert.Equal(t, []string{uri(ghost)}, warned(out))
	assert.Equal(t, 1, messagesSent(e))
}

func TestLinkAutoAttachTheSameLinkTwiceIsOneFile(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	a := e.upload(alphaOrchSID, "# once\n")
	code, out := linkSend(e, uri(a)+" again "+uri(a)+" and "+"/api/artifacts/"+a+"?v=2")
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []string{a}, hubDigests(e), "one file, however many times and in whatever form the text names it")
	assert.Nil(t, warned(out))
	assert.ElementsMatch(t, []string{a}, receive(t, e), "stored once")
}

func TestLinkAutoAttachTheApiArtifactsForm(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	a := e.upload(alphaOrchSID, "# by url\n")
	b := e.upload(alphaOrchSID, "# by https url\n")
	code, out := linkSend(e, "/api/artifacts/"+a+"?v=2 and https://dash.example.test/api/artifacts/"+b)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []string{a, b}, hubDigests(e), "the url forms attach as the coral:// form does")
	assert.Nil(t, warned(out))
}

func TestLinkAutoAttachNeverAttachesAnotherTeamsFile(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	mine := e.upload(alphaOrchSID, "# mine\n")
	theirs := e.upload(betaOrchSID, "# beta's own upload\n") // an upload of a session on ANOTHER board
	code, out := linkSend(e, uri(theirs)+" "+uri(mine))
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []string{mine}, hubDigests(e), "only the sending team's own upload; another team's is treated like an unknown id")
	assert.Equal(t, []string{uri(theirs)}, warned(out))
}

func TestLinkAutoAttachAndAnExplicitAttachShareOneSetAndOneLimit(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, e.upload(alphaOrchSID, strings.Repeat("g", 8)+string(rune('A'+i))+"\n"))
	}
	// four attached explicitly, the text names the first again and a fifth
	code, out, _ := e.relayText(alphaOrchSID, "", "again "+uri(ids[0])+" and "+uri(ids[4]), uri(ids[0]), uri(ids[1]), uri(ids[2]), uri(ids[3]))
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, ids[:4], hubDigests(e), "the explicit four, once each; the repeat adds nothing")
	assert.Equal(t, []string{uri(ids[4])}, warned(out), "the fifth is over the limit of 4 for the whole send")
}

// Whatever #5501 decides to answer for a text file with a secret in it, the secret never leaves: an explicit --attach refuses the send, a linked file must not get past the same scan.
func TestLinkAutoAttachNeverSendsAFileWithASecret(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	secret := e.upload(alphaOrchSID, "notes\nAWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\ntoken ghp_0123456789abcdefghijklmnopqrstuvwxyz01\n")
	code, out := linkSend(e, "my notes "+uri(secret))
	t.Logf("the send answered %d: %v", code, out)
	assert.Empty(t, hubDigests(e), "a file the scan refuses is never handed to Coral Hub")
	_, files, _ := e.wire.snapshot()
	for _, f := range files {
		assert.NotContains(t, string(f.Data), "wJalrXUtnFEMI")
	}
}

// The rig itself, green today: two files attached the explicit way reach the hub in order and the receiving Coral stores exactly them. If this fails the matrix above is not measuring #5501.
func TestLinkAutoAttachRigExplicitAttachReachesTheReceiver(t *testing.T) {
	e := newRelayAttachEnv(t, true)
	a := e.upload(alphaOrchSID, "# one\n")
	b := e.upload(alphaOrchSID, "# two\n")
	code, out, _ := e.relayText(alphaOrchSID, "", "two files", uri(a), uri(b))
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, []string{a, b}, hubDigests(e))
	assert.Equal(t, 1, messagesSent(e))
	assert.Nil(t, warned(out))
	assert.ElementsMatch(t, []string{a, b}, receive(t, e))
}
