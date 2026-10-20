package routes

import (
	"bytes"
	"testing"
)

// #5284 (Security's review): the Orientation rewrite parses EXIF from untrusted images, the one new parser. Whatever an APP1 segment holds, the stripped JPEG carries at most one APP1 segment,
// and it is exactly the canonical Orientation-only segment (jpegOrientationSegment): nothing of the original EXIF (GPS, camera, thumbnail, XMP) survives, and a parse failure writes none.
//
//	go test ./internal/server/routes -run xxx -fuzz FuzzJPEGKeepsOnlyTheCanonicalOrientation -fuzztime 30s
func app1Segments(jpeg []byte) [][]byte {
	var out [][]byte
	for i := 2; i+4 <= len(jpeg); {
		if jpeg[i] != 0xFF {
			break
		}
		m := jpeg[i+1]
		if m == 0xD8 || (m >= 0xD0 && m <= 0xD7) || m == 0x01 || m == 0xFF {
			i += 2
			continue
		}
		n := int(jpeg[i+2])<<8 | int(jpeg[i+3])
		if n < 2 || i+2+n > len(jpeg) {
			break
		}
		if m == 0xE1 {
			out = append(out, jpeg[i:i+2+n])
		}
		if m == 0xDA { // the scan: the rest is image data
			break
		}
		i += 2 + n
	}
	return out
}

func FuzzJPEGKeepsOnlyTheCanonicalOrientation(f *testing.F) {
	base := genJPEG(f)
	for _, payload := range [][]byte{
		nil,
		[]byte("Exif\x00\x00MM\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00\x06\x00\x00\x00\x00\x00\x00"),
		[]byte("Exif\x00\x00II\x2a\x00\x08\x00\x00\x00\x02\x00\x12\x01\x03\x00\x01\x00\x00\x00\x08\x00\x00\x00GPS-SECRET-LAT-LONG"),
		[]byte("Exif\x00\x00MM\x00\x2a\xff\xff\xff\xff"),
		[]byte("Exif\x00\x00MM\x00\x2a\x00\x00\x00\x08\xff\xff"),
		[]byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta>secret</x:xmpmeta>"),
	} {
		f.Add(payload)
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > 60000 {
			return
		}
		seg := append([]byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}, payload...)
		in := append(append(append([]byte{}, base[:2]...), seg...), base[2:]...)
		out, err := stripJPEG(in)
		if err != nil {
			return
		}
		segs := app1Segments(out)
		if len(segs) > 1 {
			t.Fatalf("%d APP1 segments survive", len(segs))
		}
		if len(segs) == 1 {
			want := jpegOrientationSegment(jpegExifOrientation(payload))
			if want == nil || !bytes.Equal(segs[0], want) {
				t.Fatalf("an APP1 survives that is not the canonical orientation segment: % x", segs[0])
			}
		}
		if bytes.Contains(out, []byte("GPS-SECRET")) || bytes.Contains(out, []byte("xmpmeta")) {
			t.Fatal("metadata text survived")
		}
	})
}
