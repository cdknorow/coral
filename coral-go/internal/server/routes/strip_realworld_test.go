package routes

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5393: the strippers against samples built the way real cameras and tools write them (a phone JPEG with a full EXIF tree, GPS and an embedded
// thumbnail, XMP, IPTC and a multi-picture block; a PNG with every kind of text chunk; a GIF with comments and application blocks), not one planted word.
// Every sample carries canary strings; none may survive, in the stripper's output or in either copy the upload route makes (the agent's file and the artifact).

// ── builders ─────────────────────────────────────────────────────────

const (
	canaryMake     = "CANARY-MAKE-Apple"
	canaryModel    = "CANARY-MODEL-iPhone-15-Pro"
	canarySoftware = "CANARY-SOFTWARE-17.4"
	canaryDate     = "CANARY-DATE-2026:10:19"
	canaryGPSArea  = "CANARY-GPS-AREA-Home"
	canaryLens     = "CANARY-LENS-Main-Camera"
	canarySerial   = "CANARY-SERIAL-F2LXYZ"
	canaryThumb    = "CANARY-THUMBNAIL-OF-THE-UNCROPPED-ORIGINAL"
	canaryXMP      = "CANARY-XMP-creator-Jane-Doe"
	canaryIPTC     = "CANARY-IPTC-byline-caption-Jane"
	canaryCOM      = "CANARY-COM-comment-/home/jane"
	canaryMPF      = "CANARY-MPF-second-image"
	canaryTrailer  = "CANARY-TRAILER-appended-after-EOI"
)

var allCanaries = []string{canaryMake, canaryModel, canarySoftware, canaryDate, canaryGPSArea, canaryLens, canarySerial, canaryThumb, canaryXMP, canaryIPTC, canaryCOM, canaryMPF, canaryTrailer}

// tiffBuilder lays out a little-endian TIFF: IFDs first, then the values that do not fit in four bytes.
type tiffEntry struct {
	tag   uint16
	typ   uint16 // 1 byte, 2 ascii, 3 short, 4 long, 5 rational, 7 undefined
	count uint32
	value []byte // the value bytes (inline when len <= 4)
	// ptr is set for an entry whose value is the offset of another IFD: it names that IFD's index in the list.
	ptrTo int
}

func buildTIFF(ifds [][]tiffEntry) []byte {
	le := binary.LittleEndian
	// Sizes: header 8, then each IFD (2 + 12n + 4), then the out-of-line values.
	offs := make([]int, len(ifds))
	pos := 8
	for i, ifd := range ifds {
		offs[i] = pos
		pos += 2 + 12*len(ifd) + 4
	}
	var data []byte
	dataPos := pos
	var out bytes.Buffer
	out.WriteString("II")
	_ = binary.Write(&out, le, uint16(42))
	_ = binary.Write(&out, le, uint32(8))
	for i, ifd := range ifds {
		_ = binary.Write(&out, le, uint16(len(ifd)))
		for _, e := range ifd {
			_ = binary.Write(&out, le, e.tag)
			_ = binary.Write(&out, le, e.typ)
			_ = binary.Write(&out, le, e.count)
			v := e.value
			if e.ptrTo > 0 {
				v = le.AppendUint32(nil, uint32(offs[e.ptrTo]))
			}
			if len(v) <= 4 {
				out.Write(v)
				out.Write(make([]byte, 4-len(v)))
			} else {
				_ = binary.Write(&out, le, uint32(dataPos+len(data)))
				data = append(data, v...)
				if len(v)%2 == 1 {
					data = append(data, 0)
				}
			}
		}
		next := uint32(0)
		if i == 0 && len(ifds) > 3 { // IFD0 -> IFD1 (the thumbnail's), the last one in the list
			next = uint32(offs[len(ifds)-1])
		}
		_ = binary.Write(&out, le, next)
	}
	out.Write(data)
	return out.Bytes()
}

func ascii(s string) tiffEntry {
	return tiffEntry{typ: 2, count: uint32(len(s) + 1), value: append([]byte(s), 0)}
}

func with(tag uint16, e tiffEntry) tiffEntry { e.tag = tag; return e }

func rationals(n ...uint32) []byte {
	var b []byte
	for _, v := range n {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	return b
}

// phoneEXIF is the EXIF of a phone photo: camera, software, time, lens, serial, a full GPS block and a thumbnail in IFD1; Orientation as given.
func phoneEXIF(orientation uint16) []byte {
	thumb := []byte{0xFF, 0xD8, 0xFF, 0xFE, 0x00, byte(len(canaryThumb) + 2)}
	thumb = append(thumb, canaryThumb...)
	thumb = append(thumb, 0xFF, 0xD9)
	tiff := buildTIFF([][]tiffEntry{
		{ // 0: IFD0
			with(0x010F, ascii(canaryMake)), with(0x0110, ascii(canaryModel)),
			{tag: 0x0112, typ: 3, count: 1, value: binary.LittleEndian.AppendUint16(nil, orientation)},
			with(0x0131, ascii(canarySoftware)), with(0x0132, ascii(canaryDate)),
			{tag: 0x8769, typ: 4, count: 1, ptrTo: 1}, {tag: 0x8825, typ: 4, count: 1, ptrTo: 2},
		},
		{ // 1: Exif IFD
			with(0x9003, ascii(canaryDate)), with(0xA434, ascii(canaryLens)), with(0xA431, ascii(canarySerial)),
		},
		{ // 2: GPS IFD: latitude, longitude, altitude, area name
			with(0x0001, ascii("N")), {tag: 0x0002, typ: 5, count: 3, value: rationals(37, 1, 46, 1, 2925, 100)},
			with(0x0003, ascii("W")), {tag: 0x0004, typ: 5, count: 3, value: rationals(122, 1, 25, 1, 959, 100)},
			{tag: 0x0006, typ: 5, count: 1, value: rationals(1230, 100)}, with(0x001C, ascii(canaryGPSArea)),
		},
		{ // 3: a spare IFD so the thumbnail IFD is the last of four
			with(0x0100, tiffEntry{typ: 3, count: 1, value: []byte{1, 0}}),
		},
		{ // 4: IFD1: the thumbnail
			{tag: 0x0103, typ: 3, count: 1, value: []byte{6, 0}},
			{tag: 0x0201, typ: 4, count: 1, value: make([]byte, 4)}, // JPEGInterchangeFormat: patched below
			{tag: 0x0202, typ: 4, count: 1, value: binary.LittleEndian.AppendUint32(nil, uint32(len(thumb)))},
		},
	})
	tiff = append(tiff, thumb...)
	return append([]byte("Exif\x00\x00"), tiff...)
}

type jpegSample struct {
	segs    [][]byte // each a complete segment: FF marker, length, payload
	scan    []byte   // entropy-coded data after SOS
	trailer []byte
}

func seg(marker byte, payload []byte) []byte {
	return append([]byte{0xFF, marker, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}, payload...)
}

var (
	testDQT   = seg(0xDB, append([]byte{0}, bytes.Repeat([]byte{8}, 64)...))
	testSOF   = seg(0xC0, []byte{8, 0, 16, 0, 16, 1, 1, 0x11, 0})
	testDHT   = seg(0xC4, append([]byte{0x00, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, 0x00))
	testSOS   = seg(0xDA, []byte{1, 1, 0, 0, 0x3F, 0})
	testScan  = []byte{0x12, 0x34, 0xFF, 0x00, 0x56, 0xFF, 0xD0, 0x78, 0x9A}
	jfifPlain = seg(0xE0, []byte("JFIF\x00\x01\x01\x01\x00\x48\x00\x48\x00\x00"))
)

func (s jpegSample) bytes() []byte {
	out := []byte{0xFF, 0xD8}
	for _, g := range s.segs {
		out = append(out, g...)
	}
	out = append(out, s.scan...)
	out = append(out, 0xFF, 0xD9)
	return append(out, s.trailer...)
}

// phoneJPEG: what a phone writes: JFIF, EXIF (camera, GPS, thumbnail), XMP, a multi-picture block, an ICC profile, IPTC, a comment, then the image, and a
// second picture appended after the end of the first.
func phoneJPEG(orientation uint16) jpegSample {
	xmp := append([]byte("http://ns.adobe.com/xap/1.0/\x00"), []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF><rdf:Description dc:creator="`+canaryXMP+`" exif:GPSLatitude="37,46.4875N"/></rdf:RDF></x:xmpmeta>`)...)
	icc := append([]byte("ICC_PROFILE\x00\x01\x01"), bytes.Repeat([]byte{0x41}, 200)...)
	mpf := append([]byte("MPF\x00"), []byte(canaryMPF)...)
	iptc := append([]byte("Photoshop 3.0\x008BIM\x04\x04"), []byte(canaryIPTC)...)
	return jpegSample{
		segs: [][]byte{jfifPlain, seg(0xE1, phoneEXIF(orientation)), seg(0xE1, xmp), seg(0xE2, mpf), seg(0xE2, icc), seg(0xED, iptc),
			seg(0xFE, []byte(canaryCOM)), testDQT, testSOF, testDHT, testSOS},
		scan:    testScan,
		trailer: append([]byte{0xFF, 0xD8, 0xFF, 0xFE, 0x00, byte(len(canaryTrailer) + 2)}, append([]byte(canaryTrailer), 0xFF, 0xD9)...),
	}
}

// jpegMarkers lists the markers of a JPEG's segments up to its scan, with their payloads.
func jpegMarkers(t *testing.T, d []byte) (markers []byte, payloads [][]byte) {
	t.Helper()
	require.GreaterOrEqual(t, len(d), 4)
	require.Equal(t, []byte{0xFF, 0xD8}, d[:2])
	for i := 2; i+4 <= len(d); {
		require.Equal(t, byte(0xFF), d[i], "a marker at %d", i)
		m := d[i+1]
		if m == 0xD9 {
			break
		}
		n := int(binary.BigEndian.Uint16(d[i+2 : i+4]))
		markers = append(markers, m)
		payloads = append(payloads, d[i+4:i+2+n])
		i += 2 + n
		if m == 0xDA {
			break
		}
	}
	return markers, payloads
}

func assertNoCanary(t *testing.T, what string, d []byte) {
	t.Helper()
	for _, c := range allCanaries {
		assert.NotContains(t, string(d), c, "%s still holds %s", what, c)
	}
}

// ── JPEG ─────────────────────────────────────────────────────────────

func TestPhoneJPEGKeepsOnlyThePixelsTheColourProfileAndTheOrientation(t *testing.T) {
	s := phoneJPEG(6)
	orig := s.bytes()
	require.NotNil(t, orig)
	for _, c := range allCanaries {
		require.Contains(t, string(orig), c, "the sample really carries %s", c)
	}
	out, err := stripJPEG(orig)
	require.NoError(t, err)
	assertNoCanary(t, "the stripped photo", out)

	markers, payloads := jpegMarkers(t, out)
	// APP1 (the orientation only), APP0 (JFIF), the ICC profile (APP2), then the tables, frame and scan, in the file's own order.
	assert.Equal(t, []byte{0xE1, 0xE0, 0xE2, 0xDB, 0xC0, 0xC4, 0xDA}, markers)
	assert.Equal(t, jpegOrientationSegment(6)[4:], payloads[0], "the one EXIF segment holds the Orientation and nothing else")
	assert.Equal(t, 6, jpegExifOrientation(payloads[0]))
	assert.True(t, bytes.HasPrefix(payloads[2], []byte("ICC_PROFILE\x00")))

	// Never re-encoded: the tables, the frame, the scan header and the entropy-coded data are the original's bytes.
	for _, kept := range [][]byte{testDQT, testSOF, testDHT, testSOS, testScan} {
		assert.True(t, bytes.Contains(out, kept), "kept as written: %x", kept[:min(len(kept), 6)])
	}
	assert.True(t, bytes.HasSuffix(out, append(append([]byte(nil), testScan...), 0xFF, 0xD9)), "ends at the EOI of the first picture: the appended second one is gone")
	assert.Less(t, len(out), len(orig))
}

// An Android-style file: XMP and Extended XMP, an Ducky APP12, IPTC, a comment and a Samsung-style trailer, no EXIF orientation at all.
func TestAndroidStyleJPEGWithExtendedXMPLosesAllOfIt(t *testing.T) {
	xmp := append([]byte("http://ns.adobe.com/xap/1.0/\x00"), []byte("<x:xmpmeta>"+canaryXMP+"</x:xmpmeta>")...)
	ext := append([]byte("http://ns.adobe.com/xmp/extension/\x00"), bytes.Repeat([]byte(canaryXMP), 20)...)
	ducky := append([]byte("Ducky"), []byte(canaryIPTC)...)
	s := jpegSample{
		segs:    [][]byte{jfifPlain, seg(0xE1, xmp), seg(0xE1, ext), seg(0xEC, ducky), seg(0xFE, []byte(canaryCOM)), seg(0xEB, []byte("C2PA"+canaryMPF)), testDQT, testSOF, testDHT, testSOS},
		scan:    testScan,
		trailer: []byte("SEFHxxxx" + canaryTrailer),
	}
	out, err := stripJPEG(s.bytes())
	require.NoError(t, err)
	assertNoCanary(t, "the stripped file", out)
	markers, _ := jpegMarkers(t, out)
	assert.Equal(t, []byte{0xE0, 0xDB, 0xC0, 0xC4, 0xDA}, markers, "no APP1 at all: an upright file keeps no EXIF")
	assert.NotContains(t, string(out), "Exif")
}

// JFIF can carry a thumbnail of its own (its header names a width and a height and holds the RGB), and a JFXX extension can hold a whole JPEG one: the
// thumbnail of the uncropped original is exactly what a cropped photo must not keep.
func TestJFIFAndJFXXThumbnailsAreNotKept(t *testing.T) {
	thumbRGB := bytes.Repeat([]byte(canaryThumb[:8]), 24) // 8 x 8 pixels of RGB: 192 bytes
	jfifWithThumb := seg(0xE0, append([]byte("JFIF\x00\x01\x02\x00\x00\x48\x00\x48\x08\x08"), thumbRGB...))
	jfxx := seg(0xE0, append([]byte("JFXX\x00\x10"), []byte("\xFF\xD8\xFF\xFE\x00\x30"+canaryThumb+"\xFF\xD9")...))
	other := seg(0xE0, []byte("MyApp\x00"+canaryCOM))
	adobeJunk := seg(0xEE, append([]byte("Adobe\x00\x64\x00\x00\x00\x00\x01"), []byte(canaryIPTC)...))
	s := jpegSample{segs: [][]byte{jfifWithThumb, jfxx, other, adobeJunk, testDQT, testSOF, testDHT, testSOS}, scan: testScan}
	out, err := stripJPEG(s.bytes())
	require.NoError(t, err)
	assert.NotContains(t, string(out), canaryThumb[:8], "the JFIF thumbnail's pixels")
	assertNoCanary(t, "the stripped file", out)
	markers, payloads := jpegMarkers(t, out)
	require.Equal(t, byte(0xE0), markers[0], "the JFIF header stays, so viewers still know the file")
	assert.Equal(t, []byte("JFIF\x00"), payloads[0][:5])
	assert.Len(t, payloads[0], 14, "without its thumbnail: the header only")
	assert.Equal(t, []byte{0, 0}, payloads[0][12:14], "a thumbnail of 0 x 0")
	for i, m := range markers {
		if m == 0xE0 || m == 0xEE {
			assert.True(t, i == 0 || m == 0xEE, "no other APP0")
		}
	}
	// The Adobe colour-transform segment, when kept, is its own 12 bytes and nothing more.
	for i, m := range markers {
		if m == 0xEE {
			assert.Len(t, payloads[i], 12)
		}
	}
	again, err := stripJPEG(out)
	require.NoError(t, err)
	assert.Equal(t, out, again, "stripping is idempotent")
}

// ── PNG ──────────────────────────────────────────────────────────────

func zlibBytes(s string) []byte {
	var b bytes.Buffer
	zw := zlib.NewWriter(&b)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	return b.Bytes()
}

func TestPNGWithEveryKindOfTextChunkLosesThemAndKeepsThePixels(t *testing.T) {
	ihdr := pngChunk("IHDR", []byte{0, 0, 0, 2, 0, 0, 0, 2, 8, 2, 0, 0, 0})
	idat := pngChunk("IDAT", zlibBytes("\x00\x01\x02\x03\x04\x05\x06\x00\x01\x02\x03\x04\x05\x06"))
	phys := pngChunk("pHYs", []byte{0, 0, 0x0B, 0x13, 0, 0, 0x0B, 0x13, 1})
	iccp := pngChunk("iCCP", append([]byte("sRGB\x00\x00"), zlibBytes("a colour profile")...))
	var in bytes.Buffer
	in.WriteString("\x89PNG\r\n\x1a\n")
	for _, c := range [][]byte{
		ihdr, pngChunk("gAMA", []byte{0, 1, 0x86, 0xA0}), iccp, phys,
		pngChunk("tEXt", []byte("Comment\x00"+canaryCOM)), pngChunk("tEXt", []byte("Author\x00"+canaryXMP)),
		pngChunk("zTXt", append([]byte("Description\x00\x00"), zlibBytes(canaryIPTC)...)),
		pngChunk("iTXt", append([]byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00"), []byte("<x:xmpmeta>"+canaryXMP+" "+canaryGPSArea+"</x:xmpmeta>")...)),
		pngChunk("eXIf", phoneEXIF(1)), pngChunk("tIME", []byte{0x07, 0xEA, 10, 19, 12, 0, 0}),
		pngChunk("prVt", []byte(canaryModel)), pngChunk("sPLT", []byte("CANARY-SERIAL-F2LXYZ\x00\x08")),
		idat, pngChunk("IEND", nil),
	} {
		in.Write(c)
	}
	in.Write(pngChunk("tEXt", []byte("Comment\x00"+canaryTrailer))) // after IEND
	orig := in.Bytes()
	for _, c := range []string{canaryCOM, canaryXMP, canaryIPTC, canaryModel, canarySerial, canaryTrailer} {
		if c == canaryIPTC { // inside a compressed chunk: not visible as text
			continue
		}
		require.Contains(t, string(orig), c)
	}
	out, err := stripPNGText(orig)
	require.NoError(t, err)
	assertNoCanary(t, "the stripped PNG", out)
	for _, typ := range []string{"tEXt", "zTXt", "iTXt", "eXIf", "tIME", "prVt", "sPLT"} {
		assert.NotContains(t, string(out), typ)
	}
	assert.Equal(t, append(append([]byte("\x89PNG\r\n\x1a\n"), ihdr...), append(pngChunk("gAMA", []byte{0, 1, 0x86, 0xA0}), append(iccp, append(phys, append(idat, pngChunk("IEND", nil)...)...)...)...)...), out,
		"exactly the chunks that draw the image, byte for byte: the pixels (IDAT) are not re-encoded, and the colour profile (iCCP) stays")
}

// ── GIF ──────────────────────────────────────────────────────────────

func gifExt(label byte, blocks ...string) []byte {
	out := []byte{0x21, label}
	for _, b := range blocks {
		out = append(out, byte(len(b)))
		out = append(out, b...)
	}
	return append(out, 0)
}

func TestGIFWithCommentsAndApplicationBlocksKeepsOnlyTimingAndFrames(t *testing.T) {
	header := []byte("GIF89a\x02\x00\x02\x00\x80\x00\x00")
	palette := []byte{0, 0, 0, 255, 255, 255}
	gce := gifExt(0xF9, "\x04\x00\x0A\x00\x00")
	frame := func(n byte) []byte {
		return []byte{0x2C, 0, 0, 0, 0, 2, 0, 2, 0, 0, 2, 2, 0x44, n, 0}
	}
	var in bytes.Buffer
	in.Write(header)
	in.Write(palette)
	in.Write(gifExt(0xFF, "NETSCAPE2.0", "\x01\x00\x00")) // loop forever
	in.Write(gifExt(0xFE, canaryCOM))                     // comment
	in.Write(gifExt(0xFF, "XMP DataXMP", "<x:xmpmeta>"+canaryXMP+"</x:xmpmeta>"))
	in.Write(gifExt(0x01, "\x0C\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00", canaryIPTC)) // plain text
	in.Write(gce)
	in.Write(frame(1))
	in.Write(gifExt(0xFE, canaryMake))
	in.Write(gce)
	in.Write(frame(2))
	in.WriteByte(0x3B)
	in.WriteString(canaryTrailer)
	orig := in.Bytes()
	for _, c := range []string{canaryCOM, canaryXMP, canaryIPTC, canaryMake, canaryTrailer} {
		require.Contains(t, string(orig), c)
	}
	out, err := stripGIF(orig)
	require.NoError(t, err)
	assertNoCanary(t, "the stripped GIF", out)
	want := append(append(append([]byte(nil), header...), palette...), gce...)
	want = append(want, frame(1)...)
	want = append(want, gce...)
	want = append(want, frame(2)...)
	want = append(want, 0x3B)
	assert.Equal(t, want, out, "the header, the palette, the timing blocks and the frames as written, then the trailer: nothing else")
}

// ── the upload route: the agent's file and the artifact ──────────────

func TestUploadedRealWorldSamplesAreCleanInBothTheAgentsFileAndTheArtifact(t *testing.T) {
	rig := uploadRig(t)
	samples := map[string][]byte{"IMG_0042.jpg": phoneJPEG(6).bytes(), "photo.jpeg": phoneJPEG(1).bytes()}
	var pngIn bytes.Buffer
	pngIn.WriteString("\x89PNG\r\n\x1a\n")
	for _, c := range [][]byte{pngChunk("IHDR", []byte{0, 0, 0, 2, 0, 0, 0, 2, 8, 2, 0, 0, 0}), pngChunk("tEXt", []byte("Comment\x00"+canaryCOM)),
		pngChunk("eXIf", phoneEXIF(3)), pngChunk("IDAT", zlibBytes("\x00\x01\x02\x03\x04\x05\x06\x00\x01\x02\x03\x04\x05\x06")), pngChunk("IEND", nil)} {
		pngIn.Write(c)
	}
	samples["shot.png"] = pngIn.Bytes()
	for name, orig := range samples {
		code, out := postImage(t, name, orig, "s1")
		require.Equal(t, http.StatusOK, code, "%s: %v", name, out)
		art := artifactOf(out)
		require.NotNil(t, art, "%s: an artifact: %v", name, out)
		file, err := os.ReadFile(out["path"].(string))
		require.NoError(t, err)
		stored, err := os.ReadFile(filepath.Join(rig.coralDir, "artifacts", "objects", art["id"].(string)))
		require.NoError(t, err)
		assert.Equal(t, file, stored, "%s: the agent's file and the artifact are the same bytes", name)
		assertNoCanary(t, name+" (the agent's file)", file)
		assertNoCanary(t, name+" (the artifact)", stored)
		assert.NotContains(t, string(file), "Exif\x00\x00II", "%s: no EXIF tree", name)
	}
}

// ── HEIC ─────────────────────────────────────────────────────────────

func heicWithEXIF() []byte {
	var b bytes.Buffer
	b.Write([]byte{0, 0, 0, 0x18})
	b.WriteString("ftypheic")
	b.Write([]byte{0, 0, 0, 0})
	b.WriteString("mif1heic")
	b.Write([]byte{0, 0, 0, 0x30})
	b.WriteString("meta")
	b.Write([]byte{0, 0, 0, 0})
	b.WriteString("Exif" + canaryMake + canaryGPSArea)
	return b.Bytes()
}

// What a HEIC does today, pinned so the docs can say it: a file named .heic is refused by its extension; one that arrives under an allowed name (a
// paste that carries HEIC bytes as image.png, say) is recognised by its bytes as HEIF, gets no artifact, and the agent's copy is the original bytes,
// metadata included. Coral does not convert or strip HEIC.
func TestHEICIsRefusedByNameAndOtherwiseOnlyKeptForTheAgentUnstripped(t *testing.T) {
	rig := uploadRig(t)
	code, out := postImage(t, "IMG_0042.heic", heicWithEXIF(), "s1")
	assert.Equal(t, http.StatusBadRequest, code, "a .heic name is not an allowed type")
	assert.Contains(t, out["error"], "HEIC", "the refusal says what to do (#6088)")
	assert.Contains(t, out["error"], "JPEG")

	code, out = postImage(t, "photo.png", heicWithEXIF(), "s1")
	require.Equal(t, http.StatusOK, code, out)
	assert.Nil(t, artifactOf(out), "no artifact for HEIC bytes")
	assert.Contains(t, out["artifact_error"], "not a picture Coral recognises")
	file, err := os.ReadFile(out["path"].(string))
	require.NoError(t, err)
	assert.Equal(t, heicWithEXIF(), file, "the agent's copy is the original: Coral does not strip or convert HEIC")
	assert.NoDirExists(t, filepath.Join(rig.coralDir, "artifacts", "uploads", "s1"), "no record")

	// The cross-team and attach paths do look inside a HEIC, and refuse one that carries EXIF or XMP.
	_, _, err = cleanUploadedImage(heicWithEXIF())
	var meta errImageMetadata
	assert.ErrorAs(t, err, &meta)
}
