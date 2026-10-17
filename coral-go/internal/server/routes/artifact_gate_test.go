package routes

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// genImage encodes a small test image.
func genPNG(t testing.TB) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < 8; i++ {
		img.Set(i, i, color.RGBA{255, 0, 0, 255})
	}
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, img))
	return b.Bytes()
}

func genJPEG(t testing.TB) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	var b bytes.Buffer
	require.NoError(t, jpeg.Encode(&b, img, nil))
	return b.Bytes()
}

func genGIF(t testing.TB) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 8, 8), color.Palette{color.Black, color.White})
	var b bytes.Buffer
	require.NoError(t, gif.Encode(&b, img, nil))
	return b.Bytes()
}

// pngChunk is one PNG chunk with a correct CRC.
func pngChunk(typ string, payload []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(len(payload)))
	b.WriteString(typ)
	b.Write(payload)
	crc := crc32.NewIEEE()
	crc.Write([]byte(typ))
	crc.Write(payload)
	binary.Write(&b, binary.BigEndian, crc.Sum32())
	return b.Bytes()
}

// withPNGText inserts a tEXt chunk (a name, a place) after IHDR.
func withPNGText(png []byte) []byte {
	const afterIHDR = 8 + 12 + 13
	out := append([]byte{}, png[:afterIHDR]...)
	out = append(out, pngChunk("tEXt", []byte("Author\x00Jane Doe, 12 Elm St"))...)
	return append(out, png[afterIHDR:]...)
}

// withJPEGExif inserts an APP1 Exif segment after SOI.
func withJPEGExif(j []byte) []byte {
	payload := append([]byte("Exif\x00\x00"), []byte("GPS 37.77,-122.41 and a name")...)
	seg := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	seg = append(seg, payload...)
	return append(append(append([]byte{}, j[:2]...), seg...), j[2:]...)
}

// withGIFComment inserts a comment extension before the trailer.
func withGIFComment(g []byte) []byte {
	comment := append([]byte{0x21, 0xFE, 12}, []byte("a secret one")...)
	comment = append(comment, 0)
	return append(append(append([]byte{}, g[:len(g)-1]...), comment...), 0x3B)
}

func padTo(b []byte, off int, sig string) []byte {
	out := make([]byte, max(off+len(sig), len(b)))
	copy(out, b)
	copy(out[off:], sig)
	return out
}

func TestGateRefusesWhatItShould(t *testing.T) {
	lnk := append([]byte("L\x00\x00\x00\x01\x14\x02\x00\x00\x00\x00\x00\xc0\x00\x00\x00\x00\x00\x00F"), make([]byte, 80)...)
	dmg := append(bytes.Repeat([]byte("x"), 2000), append([]byte("koly"), make([]byte, 508)...)...)
	for name, data := range map[string][]byte{
		"ELF":              append([]byte("\x7fELF\x02\x01\x01"), make([]byte, 100)...),
		"PE":               append([]byte("MZ\x90\x00"), make([]byte, 100)...),
		"shell script":     []byte("#!/bin/sh\nrm -rf ~\n"),
		"Mach-O 64":        append([]byte("\xcf\xfa\xed\xfe"), make([]byte, 100)...),
		"universal binary": append([]byte("\xca\xfe\xba\xbe\x00\x00\x00\x02"), make([]byte, 100)...),
		"zip":              append([]byte("PK\x03\x04"), make([]byte, 100)...),
		"empty zip":        append([]byte("PK\x05\x06"), make([]byte, 100)...),
		"gzip":             append([]byte("\x1f\x8b\x08\x00"), make([]byte, 100)...),
		"7z":               append([]byte("7z\xbc\xaf\x27\x1c\x00\x04"), make([]byte, 100)...),
		"rar":              append([]byte("Rar!\x1a\x07\x00"), make([]byte, 100)...),
		"bzip2":            append([]byte("BZh91AY&SY"), make([]byte, 100)...),
		"xz":               append([]byte("\xfd7zXZ\x00\x00"), make([]byte, 100)...),
		"zstd":             append([]byte("\x28\xb5\x2f\xfd"), make([]byte, 100)...),
		"tar":              padTo(append([]byte("file.txt"), make([]byte, 600)...), 257, "ustar\x0000"),
		"OLE":              append([]byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"), make([]byte, 100)...),
		"RTF":              []byte("{\\rtf1\\ansi hello}"),
		"lnk":              lnk,
		"iso":              padTo(make([]byte, 40000), 32769, "CD001"),
		"qcow2":            append([]byte("QFI\xfb"), make([]byte, 100)...),
		"vhd":              append([]byte("conectix"), make([]byte, 100)...),
		"dmg":              dmg,
		"empty":            nil,
		"binary blob":      bytes.Repeat([]byte{0x01, 0x02, 0xff, 0xfe}, 100),
		"UTF-16 text":      []byte("\xff\xfeh\x00i\x00"),
	} {
		_, err := gateReceived(data)
		require.Error(t, err, name)
		var r gateRefusal
		require.ErrorAs(t, err, &r, name)
		assert.Equal(t, "type_refused", r.Code, name)
		assert.NotContains(t, r.Reason, string(data[:min(len(data), 8)])+"x", name+": the reason is Coral's words")
		// A named kind is refused BY NAME, not only because its bytes are not text: this fails if its case is
		// dropped from the gate.
		if want, named := map[string]string{
			"ELF": "ELF", "PE": "Windows", "shell script": "script", "Mach-O 64": "Mach-O", "universal binary": "Mach-O",
			"zip": "zip", "empty zip": "zip", "gzip": "gzip", "7z": "7z", "rar": "rar", "bzip2": "bzip2", "xz": "xz", "zstd": "zstd",
			"tar": "tar", "OLE": "OLE", "RTF": "RTF", "lnk": "shortcut", "iso": "disk image", "qcow2": "disk image", "vhd": "disk image", "dmg": "disk image",
		}[name]; named {
			assert.Contains(t, r.Reason, want, name)
		}
	}
}

func TestGateAcceptsAndNamesByTheSniffedType(t *testing.T) {
	jpgExif, pngText, gifComment := withJPEGExif(genJPEG(t)), withPNGText(genPNG(t)), withGIFComment(genGIF(t))
	for name, c := range map[string]struct {
		data      []byte
		mediaType string
		ext       string
		image     bool
	}{
		"png":      {genPNG(t), "image/png", ".png", true},
		"jpeg":     {genJPEG(t), "image/jpeg", ".jpg", true},
		"gif":      {genGIF(t), "image/gif", ".gif", true},
		"png+text": {pngText, "image/png", ".png", true},
		"jpg+exif": {jpgExif, "image/jpeg", ".jpg", true},
		"gif+note": {gifComment, "image/gif", ".gif", true},
		"pdf":      {[]byte("%PDF-1.7\n1 0 obj\n"), "application/pdf", ".pdf", false},
		"svg":      {[]byte(`<?xml version="1.0"?><!-- c --><svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), "image/svg+xml", ".svg", false},
		"html":     {[]byte("<!DOCTYPE html><html><body><script>alert(1)</script></body></html>"), "text/html", ".html", false},
		"html tag": {[]byte("  <script>alert(1)</script>"), "text/html", ".html", false},
		"json":     {[]byte(`{"a":[1,2,3],"b":"é"}`), "application/json", ".json", false},
		"markdown": {[]byte("# Title\n\n- one\n- two\n"), "text/plain", ".txt", false},
		"csv":      {[]byte("a,b,c\n1,2,3\n"), "text/plain", ".txt", false},
		"log":      {[]byte("2026-10-17 10:00:00 INFO started\n2026-10-17 10:00:01 WARN slow\n"), "text/plain", ".txt", false},
	} {
		res, err := gateReceived(c.data)
		require.NoError(t, err, name)
		assert.Equal(t, c.mediaType, res.MediaType, name)
		assert.Equal(t, c.ext, res.Ext, name)
		assert.Equal(t, c.image, res.Image, name)
	}

	// An image comes out stripped: no tEXt, no Exif, no comment.
	res, _ := gateReceived(pngText)
	assert.NotContains(t, string(res.Data), "Jane Doe")
	res, _ = gateReceived(jpgExif)
	assert.NotContains(t, string(res.Data), "GPS 37.77")
	res, _ = gateReceived(gifComment)
	assert.NotContains(t, string(res.Data), "a secret one")
}

// The name's extension is the sniffed type's, never the sender's.
func TestReceivedFileNameFollowsTheSniffedType(t *testing.T) {
	// Text named .png is text.
	res, err := gateReceived([]byte("just some notes\n"))
	require.NoError(t, err)
	assert.Equal(t, "evil.txt", receivedFileName("evil.png", res))
	// A renamed ELF never gets a name: it is refused.
	_, err = gateReceived(append([]byte("\x7fELF"), make([]byte, 64)...))
	assert.Error(t, err, "a renamed ELF with a .png name is refused")
	// Hostile names: traversal, direction overrides, device names, control characters, no base.
	png := gateResult{Ext: ".png"}
	for sent, want := range map[string]string{
		"../../etc/passwd":             "passwd.png",
		`..\..\windows\system32\x.exe`: "x.png",
		"report‮gpj.exe":               "report_gpj.png",
		"CON":                          "_CON.png",
		"CON.txt":                      "_CON.png",
		"nul.tar.gz":                   "_nul.tar.png",
		"Com1":                         "_Com1.png",
		"LPT9.log":                     "_LPT9.png",
		"prn":                          "_prn.png",
		"AUX ":                         "_AUX.png",
		"CONIN":                        "_CONIN.png",
		"CONOUT.x":                     "_CONOUT.png",
		"COM10":                        "COM10.png",
		"console":                      "console.png",
		"a\x00b\x07c.txt":              "a_b_c.png",
		"":                             "attachment.png",
		"...":                          "attachment.png",
		strings.Repeat("a", 300):       strings.Repeat("a", 64) + ".png",
	} {
		assert.Equal(t, want, receivedFileName(sent, png), "%q", sent)
	}
}

// An image that would be drawn larger than a viewer should be asked to, by its header alone, is refused.
func TestGateRefusesAPixelBomb(t *testing.T) {
	var ihdr bytes.Buffer
	binary.Write(&ihdr, binary.BigEndian, uint32(60000))
	binary.Write(&ihdr, binary.BigEndian, uint32(60000))
	ihdr.Write([]byte{8, 6, 0, 0, 0})
	bomb := append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", ihdr.Bytes())...)
	bomb = append(bomb, pngChunk("IDAT", []byte{0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01})...)
	bomb = append(bomb, pngChunk("IEND", nil)...)
	_, err := gateReceived(bomb)
	var r gateRefusal
	require.ErrorAs(t, err, &r)
	assert.Equal(t, "type_refused", r.Code)
	assert.Contains(t, r.Reason, "too large")
}

// A WebP with EXIF cannot be rewritten: the gate answers image_metadata, as the upload route does.
func TestGateRefusesAWebPWithMetadata(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(4+8+10+8+4))
	b.WriteString("WEBP")
	b.WriteString("VP8L")
	binary.Write(&b, binary.LittleEndian, uint32(10))
	b.Write([]byte{0x2f, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	b.WriteString("EXIF")
	binary.Write(&b, binary.LittleEndian, uint32(4))
	b.WriteString("Exif")
	_, err := gateReceived(b.Bytes())
	var r gateRefusal
	require.ErrorAs(t, err, &r)
	assert.Equal(t, "image_metadata", r.Code)
}

// Active types stay inert when served under the sniffed type: HTML, SVG and PDF download (attachment),
// sandboxed, never sniffed, with the extension of what they are.
func TestActiveTypesStayInertWhenServed(t *testing.T) {
	for name, data := range map[string][]byte{
		"html": []byte("<html><script>fetch('/api/system/api-key')</script></html>"),
		"svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`),
		"pdf":  []byte("%PDF-1.7 /JavaScript"),
	} {
		res, err := gateReceived(data)
		require.NoError(t, err, name)
		h := http.Header{}
		file := receivedFileName("anything.png", res) // the sender's name says png; the type does not
		setUntrustedContentHeaders(h, res.MediaType, file)
		assert.True(t, strings.HasPrefix(h.Get("Content-Disposition"), "attachment"), "%s: %q", name, h.Get("Content-Disposition"))
		assert.Contains(t, h.Get("Content-Disposition"), res.Ext, name)
		assert.NotContains(t, h.Get("Content-Disposition"), ".png", name)
		assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"), name)
		assert.Contains(t, h.Get("Content-Security-Policy"), "sandbox", name)
	}
}

// Strip is idempotent for every handled type: strip(strip(x)) == strip(x), and a file that carries no metadata is unchanged.
func TestStripIsIdempotent(t *testing.T) {
	for name, c := range map[string]struct {
		data   []byte
		family string
	}{
		"png":      {genPNG(t), "image/png"},
		"png+text": {withPNGText(genPNG(t)), "image/png"},
		"jpg":      {genJPEG(t), "image/jpeg"},
		"jpg+exif": {withJPEGExif(genJPEG(t)), "image/jpeg"},
		"gif":      {genGIF(t), "image/gif"},
		"gif+note": {withGIFComment(genGIF(t)), "image/gif"},
		"png+tail": {append(genPNG(t), []byte("trailing polyglot data")...), "image/png"},
		"jpg+tail": {append(genJPEG(t), []byte("PK\x03\x04 hidden zip")...), "image/jpeg"},
	} {
		once, err := stripImageMetadata(c.data, c.family)
		require.NoError(t, err, name)
		twice, err := stripImageMetadata(once, c.family)
		require.NoError(t, err, name)
		assert.Equal(t, once, twice, name)
		assert.LessOrEqual(t, len(once), len(c.data), name+": a strip never grows a file")
	}
	// Nothing to strip: unchanged.
	for _, d := range [][]byte{genPNG(t), genJPEG(t), genGIF(t)} {
		out, err := stripImageMetadata(d, sniffImageFamily(d))
		require.NoError(t, err)
		assert.Equal(t, d, out)
	}
}

// The strippers' limits: too large an input, and too many structural elements, are refused, not worked through.
func TestStripLimits(t *testing.T) {
	_, err := stripImageMetadata(make([]byte, stripMaxInput+1), "image/png")
	assert.Error(t, err, "an input over the store's maximum")

	// Too many PNG chunks.
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	b.Write(pngChunk("IHDR", make([]byte, 13)))
	for i := 0; i < stripMaxElements+5; i++ {
		b.Write(pngChunk("tEXt", nil))
	}
	b.Write(pngChunk("IEND", nil))
	_, err = stripImageMetadata(b.Bytes(), "image/png")
	assert.Error(t, err, "more PNG chunks than the limit")

	// A chunk length near 4 GiB does not wrap.
	huge := append([]byte("\x89PNG\r\n\x1a\n"), 0xFF, 0xFF, 0xFF, 0xF0, 't', 'E', 'X', 't')
	huge = append(huge, make([]byte, 16)...)
	_, err = stripImageMetadata(huge, "image/png")
	assert.Error(t, err)
}

// Text means text all the way through, not only in the first probe: 9 KiB of text then a zip is refused.
func TestGateTextIsTextThroughout(t *testing.T) {
	text := bytes.Repeat([]byte("plain words\n"), 1000) // over the 8 KiB probe
	_, err := gateReceived(text)
	require.NoError(t, err)
	for name, tail := range map[string][]byte{
		"zip":      append([]byte("PK\x03\x04"), make([]byte, 40)...),
		"NUL":      {0},
		"bad utf8": {0xff, 0xfe, 0xfd},
	} {
		_, err := gateReceived(append(append([]byte{}, text...), tail...))
		var r gateRefusal
		require.ErrorAs(t, err, &r, name)
		assert.Equal(t, "type_refused", r.Code, name)
	}
}
