package jsonl

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
)

func writeLines(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	require.NoError(t, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return p
}

func claudeLine(t *testing.T, data string) string {
	b, err := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": data}}}}})
	require.NoError(t, err)
	return string(b)
}

func TestFindImageFindsByContentAndRefusesTheRest(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nbody-one")
	other := []byte("\x89PNG\r\n\x1a\nbody-two")
	p := writeLines(t, `{"type":"user","message":{"content":"no image on this line"}}`, claudeLine(t, base64.StdEncoding.EncodeToString(other)), claudeLine(t, base64.StdEncoding.EncodeToString(png)))
	got, err := FindImage(context.Background(), p, "claude", agent.ImageRef(base64.StdEncoding.EncodeToString(png)))
	require.NoError(t, err)
	assert.Equal(t, png, got)
	_, err = FindImage(context.Background(), p, "claude", agent.ImageRef("not in the file"))
	assert.ErrorIs(t, err, ErrImageNotFound)
	for _, bad := range []string{"", "x", "../../etc/passwd", strings.Repeat("Z", 32)} {
		_, err = FindImage(context.Background(), p, "claude", bad)
		assert.ErrorIs(t, err, ErrImageNotFound, bad)
	}
	_, err = FindImage(context.Background(), filepath.Join(t.TempDir(), "none.jsonl"), "claude", agent.ImageRef("x"))
	assert.Error(t, err)
}

func TestFindImageRefusesAnOversizedImageAndSkipsAnOverlongLine(t *testing.T) {
	big := base64.StdEncoding.EncodeToString(make([]byte, 6000))
	p := writeLines(t, claudeLine(t, big))
	restoreImg := maxImageBytes
	maxImageBytes = 5000
	t.Cleanup(func() { maxImageBytes = restoreImg })
	_, err := FindImage(context.Background(), p, "claude", agent.ImageRef(big))
	assert.ErrorIs(t, err, ErrImageTooLarge, "decoded size over the cap")

	maxImageBytes = restoreImg
	restoreLine := maxImageLine
	maxImageLine = 4000
	t.Cleanup(func() { maxImageLine = restoreLine })
	_, err = FindImage(context.Background(), p, "claude", agent.ImageRef(big))
	assert.ErrorIs(t, err, ErrImageNotFound, "a line over the cap is not read")
}

func TestFindImageFindsACodexDataURLAndTheEventsImageList(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\ncodex")
	enc := base64.StdEncoding.EncodeToString(png)
	resp, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{
		map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + enc}}}})
	ev, _ := json.Marshal(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "m", "images": []any{"data:image/png;base64," + enc}}})
	for _, line := range []string{string(resp), string(ev)} {
		got, err := FindImage(context.Background(), writeLines(t, line), "claude", agent.ImageRef(enc)) // the walk is the same for any agent type; the Codex rollout open is covered by the route test
		require.NoError(t, err)
		assert.Equal(t, png, got)
	}
}

// The reader's messages carry the reference, not the bytes; a message that is only images reads as its labels.
func TestClaudeImageBlocksBecomeChipsWithReferences(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nx"))
	p := writeLines(t, claudeLine(t, enc))
	msgs, _, err := ReadFromContext(context.Background(), p, "claude", 0)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "[Image #1]", msgs[0]["content"])
	atts, _ := msgs[0]["attachments"].([]map[string]any)
	require.Len(t, atts, 1)
	assert.Equal(t, agent.ImageRef(enc), atts[0]["ref"])
	b, _ := json.Marshal(msgs[0])
	assert.NotContains(t, string(b), enc)
}

// An image-bearing transcript of n lines (and filler lines between), and the references of its images.
func imageTranscript(t *testing.T, n int, imgBytes int) (path string, refs []string, b64s []string) {
	t.Helper()
	var lines []string
	for i := 0; i < n; i++ {
		raw := make([]byte, imgBytes)
		copy(raw, "\x89PNG\r\n\x1a\n")
		raw[len(raw)-1], raw[len(raw)-2] = byte(i), byte(i>>8)
		enc := base64.StdEncoding.EncodeToString(raw)
		b64s, refs = append(b64s, enc), append(refs, agent.ImageRef(enc))
		lines = append(lines, `{"type":"user","message":{"content":"text before"}}`, claudeLine(t, enc))
	}
	return writeLines(t, lines...), refs, b64s
}

// #5055: each image's line is remembered as the transcript is scanned, so the next reference is one line read, not the transcript again: N images cost the
// transcript once, not N times.
func TestFindImageRemembersWhereEachImageIs(t *testing.T) {
	path, refs, b64s := imageTranscript(t, 20, 20000)
	ctx := context.Background()
	imageScanBytes.Store(0)
	for i := len(refs) - 1; i >= 0; i-- { // the worst order: the last image first, then every earlier one
		got, err := FindImage(ctx, path, "claude", refs[i])
		require.NoError(t, err)
		assert.Equal(t, agent.ImageRef(base64.StdEncoding.EncodeToString(got)), refs[i])
	}
	fileSize := int64(len(b64s[0])) * 20
	used := imageScanBytes.Load()
	assert.Less(t, used, fileSize*2, "20 images cost about one pass over the transcript and one line each, not 20 passes: %d bytes for a %d-byte transcript", used, fileSize)
	// and a second round of asks reads one line each
	imageScanBytes.Store(0)
	for _, ref := range refs {
		_, err := FindImage(ctx, path, "claude", ref)
		require.NoError(t, err)
	}
	assert.LessOrEqual(t, imageScanBytes.Load(), int64(len(b64s[0])+200)*20, "known images are read by offset")
}

// The index is of one transcript: a second transcript with the same images has its own, and an unknown reference is still not found.
func TestFindImageIndexIsPerTranscript(t *testing.T) {
	a, refsA, _ := imageTranscript(t, 3, 3000)
	b, _, _ := imageTranscript(t, 2, 4000) // different bytes
	ctx := context.Background()
	_, err := FindImage(ctx, a, "claude", refsA[2])
	require.NoError(t, err)
	_, err = FindImage(ctx, b, "claude", refsA[2])
	assert.ErrorIs(t, err, ErrImageNotFound, "another transcript's index does not answer for this one")
}

// A transcript that grew is scanned from where the last scan stopped; one that was replaced or cut short is scanned afresh.
func TestFindImageIndexFollowsTheFile(t *testing.T) {
	path, refs, _ := imageTranscript(t, 3, 3000)
	ctx := context.Background()
	_, err := FindImage(ctx, path, "claude", refs[0])
	require.NoError(t, err)
	// it grows: a new image at the end
	extra := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nappended"))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(claudeLine(t, extra) + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	got, err := FindImage(ctx, path, "claude", agent.ImageRef(extra))
	require.NoError(t, err)
	assert.Equal(t, []byte("\x89PNG\r\n\x1a\nappended"), got)
	// a line still being written (no newline yet) is not taken, and is found once it is complete
	partial := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\npartial"))
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(claudeLine(t, partial))
	_ = f.Close()
	_, err = FindImage(ctx, path, "claude", agent.ImageRef(partial))
	assert.ErrorIs(t, err, ErrImageNotFound)
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("\n")
	_ = f.Close()
	_, err = FindImage(ctx, path, "claude", agent.ImageRef(partial))
	assert.NoError(t, err, "the finished line is found")
	// it is truncated and rewritten with other images: the old offsets are not trusted
	other := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nrewritten-content"))
	require.NoError(t, os.WriteFile(path, []byte(claudeLine(t, other)+"\n"), 0o600))
	_, err = FindImage(ctx, path, "claude", refs[0])
	assert.ErrorIs(t, err, ErrImageNotFound, "an image of the old file is gone with it")
	got, err = FindImage(ctx, path, "claude", agent.ImageRef(other))
	require.NoError(t, err)
	assert.Equal(t, []byte("\x89PNG\r\n\x1a\nrewritten-content"), got)
	// replaced by another file (a rotation): a new identity
	rotated := path + ".new"
	require.NoError(t, os.WriteFile(rotated, []byte(claudeLine(t, extra)+"\n"+claudeLine(t, other)+"\n"), 0o600))
	require.NoError(t, os.Rename(rotated, path))
	_, err = FindImage(ctx, path, "claude", agent.ImageRef(extra))
	assert.NoError(t, err, "the rotated file's images are found at their new offsets")
}

// The old offset of a line that is no longer what it was is checked by hashing, and a mismatch scans again.
func TestFindImageStaleOffsetRescans(t *testing.T) {
	path, refs, _ := imageTranscript(t, 2, 2000)
	ctx := context.Background()
	_, err := FindImage(ctx, path, "claude", refs[1])
	require.NoError(t, err)
	// the same size, the same inode, other content at the remembered place (and the file looks no older)
	st, _ := os.Stat(path)
	data, _ := os.ReadFile(path)
	swapped := bytes.Replace(data, []byte(`"text before"`), []byte(`"text beford"`), -1)
	require.NoError(t, os.WriteFile(path, swapped, 0o600))
	require.NoError(t, os.Chtimes(path, st.ModTime(), st.ModTime().Add(2*time.Second)))
	_, err = FindImage(ctx, path, "claude", refs[1])
	assert.NoError(t, err, "still found: the offsets held (the images did not move)")
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", 100)+"\n"+string(swapped)), 0o600)) // everything moved down
	_, err = FindImage(ctx, path, "claude", refs[1])
	assert.NoError(t, err, "the image moved: its remembered offset no longer holds it, so the transcript is scanned again")
}

// 20 pasted images of 1 MB: asking for each in turn (the chat's chips), against the transcript read from the top each time.
func BenchmarkFindImage20Images(b *testing.B) {
	var lines []string
	var refs []string
	for i := 0; i < 20; i++ {
		raw := make([]byte, 1<<20)
		copy(raw, "\x89PNG\r\n\x1a\n")
		raw[len(raw)-1] = byte(i)
		enc := base64.StdEncoding.EncodeToString(raw)
		refs = append(refs, agent.ImageRef(enc))
		entry, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": enc}}}}})
		lines = append(lines, string(entry))
	}
	path := filepath.Join(b.TempDir(), "bench.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	for _, forgetEach := range []bool{false, true} { // true is the scan from the top for every reference: what FindImage did before the index
		name := "indexed"
		if forgetEach {
			name = "rescan-every-time"
		}
		b.Run(name, func(b *testing.B) {
			for n := 0; n < b.N; n++ {
				ForgetImageIndex(path, "claude")
				for _, ref := range refs {
					if forgetEach {
						ForgetImageIndex(path, "claude")
					}
					if _, err := FindImage(context.Background(), path, "claude", ref); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
