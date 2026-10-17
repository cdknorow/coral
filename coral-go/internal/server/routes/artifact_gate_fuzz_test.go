package routes

import (
	"bytes"
	"testing"
)

// Fuzz targets for the image strippers and the content gate (#4380). The checked-in corpus is the small
// seed set added here (a valid file of each type, with metadata and with a polyglot tail); a crasher
// `go test -fuzz` finds is saved under testdata/fuzz and then runs with every `go test`.
//
//	go test ./internal/server/routes -run xxx -fuzz FuzzStripJPEG -fuzztime 30s

func seedImages(f *testing.F) {
	f.Helper()
	for _, s := range [][]byte{
		genPNG(f), withPNGText(genPNG(f)), genJPEG(f), withJPEGExif(genJPEG(f)), genGIF(f), withGIFComment(genGIF(f)),
		append(genPNG(f), "PK\x03\x04 tail"...), append(genJPEG(f), "tail"...),
		[]byte("\x89PNG\r\n\x1a\n"), []byte("\xff\xd8\xff\xd9"), []byte("GIF89a"), nil,
	} {
		f.Add(s)
	}
}

// checkStrip is what every stripper must hold for any input: it does not panic, never grows a file, and on
// success its output is stable (stripped once is stripped) and still of the same family.
func checkStrip(t *testing.T, family string, in []byte, strip func([]byte) ([]byte, error)) {
	out, err := strip(in)
	if err != nil {
		return
	}
	if len(out) > len(in) {
		t.Fatalf("%s: the output (%d bytes) is larger than the input (%d)", family, len(out), len(in))
	}
	if sniffImageFamily(out) != family {
		t.Fatalf("%s: the output is no longer of that family", family)
	}
	again, err := strip(out)
	if err != nil {
		t.Fatalf("%s: stripping its own output fails: %v", family, err)
	}
	if !bytes.Equal(again, out) {
		t.Fatalf("%s: strip(strip(x)) != strip(x)", family)
	}
}

func FuzzStripJPEG(f *testing.F) {
	seedImages(f)
	f.Fuzz(func(t *testing.T, in []byte) { checkStrip(t, "image/jpeg", in, stripJPEG) })
}

func FuzzStripPNG(f *testing.F) {
	seedImages(f)
	f.Fuzz(func(t *testing.T, in []byte) { checkStrip(t, "image/png", in, stripPNGText) })
}

func FuzzStripGIF(f *testing.F) {
	seedImages(f)
	f.Fuzz(func(t *testing.T, in []byte) { checkStrip(t, "image/gif", in, stripGIF) })
}

// The gate for any input: no panic, a refusal is a gateRefusal with a fixed code, and an acceptance is
// self-consistent: the extension is the type's, an image is stripped (never larger), and a refused kind is
// never accepted as text.
func FuzzGateReceived(f *testing.F) {
	seedImages(f)
	for _, s := range []string{
		"\x7fELF", "MZ", "#!/bin/sh\n", "PK\x03\x04", "{\\rtf1", "%PDF-1.7", "<svg/>", "<html>", `{"a":1}`, "plain text\n",
		"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1", "\x1f\x8b", "L\x00\x00\x00\x01\x14\x02\x00\x00\x00\x00\x00\xc0\x00\x00\x00\x00\x00\x00F",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		res, err := gateReceived(in)
		if err != nil {
			r, ok := err.(gateRefusal)
			if !ok || (r.Code != "type_refused" && r.Code != "image_metadata") {
				t.Fatalf("a refusal that is not the gate's: %v", err)
			}
			return
		}
		if res.Ext == "" || res.MediaType == "" {
			t.Fatalf("accepted with no type or extension: %+v", res)
		}
		if res.Image && len(res.Data) > len(in) {
			t.Fatalf("an accepted image grew (%d > %d)", len(res.Data), len(in))
		}
		head := in
		if len(head) > gateHead {
			head = head[:gateHead]
		}
		tail := in
		if len(tail) > gateTail {
			tail = tail[len(tail)-gateTail:]
		}
		if refusedKind(head, tail) != "" {
			t.Fatalf("a refused kind was accepted as %s", res.MediaType)
		}
	})
}
