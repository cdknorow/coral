package routes

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// The content gate for a file that arrives from another computer (cross-team artifacts, coral-hub's
// design section 10 C4, #4380): what the file IS is decided from a bounded number of its own bytes,
// never from its name or its declared type. Executables, archives and containers, OLE documents, RTF
// and Windows shortcuts are refused; images are accepted after their metadata is stripped (and their
// size checked), as are text, JSON, PDF, HTML and SVG, which stay inert when served (they download or
// open only sandboxed: setUntrustedContentHeaders). The stored and downloaded extension comes from
// the sniffed type, so a renamed ELF is refused and a text file named x.png is stored as x.txt.

const (
	// gateHead is how many leading bytes are looked at: enough for every signature below (an ISO's
	// "CD001" sits at 32769) and no more, whatever the size of the file.
	gateHead = 33 << 10
	// gateTail is how many trailing bytes are looked at (an Apple disk image ends with "koly").
	gateTail = 512
	// gateTextProbe is how much is checked to call a file text.
	gateTextProbe = 8 << 10
)

// gateResult is what an accepted file is.
type gateResult struct {
	MediaType string // the sniffed media type (what it is served as)
	Ext       string // its extension with the dot, from the type
	Image     bool   // an image: Data is the stripped bytes
	Data      []byte // the bytes to store (stripped for an image, the input otherwise)
}

// refusal is why a file was refused, in Coral's words.
type gateRefusal struct {
	Code   string // "type_refused" or "image_metadata"
	Reason string
}

func (r gateRefusal) Error() string { return r.Code + ": " + r.Reason }

func refuse(reason string) error { return gateRefusal{Code: "type_refused", Reason: reason} }

// gateReceived decides what data is and returns what to store, or a gateRefusal.
func gateReceived(data []byte) (gateResult, error) {
	if len(data) == 0 {
		return gateResult{}, refuse("the file is empty")
	}
	head := data
	if len(head) > gateHead {
		head = head[:gateHead]
	}
	tail := data
	if len(tail) > gateTail {
		tail = tail[len(tail)-gateTail:]
	}
	if what := refusedKind(head, tail); what != "" {
		return gateResult{}, refuse(what + " files are not accepted")
	}

	// Images: by the bytes, stripped, and not larger than an image viewer should be asked to draw.
	if family := sniffImageFamily(head); family != "" {
		if err := imageSizeOK(data, family); err != nil {
			return gateResult{}, refuse("the image is too large")
		}
		clean, _, err := cleanUploadedImage(data)
		if err != nil {
			return gateResult{}, gateRefusal{Code: "image_metadata", Reason: err.Error()}
		}
		return gateResult{MediaType: family, Ext: imageExt(family), Image: true, Data: clean}, nil
	}
	switch {
	case bytes.HasPrefix(head, []byte("%PDF-")):
		return gateResult{MediaType: "application/pdf", Ext: ".pdf", Data: data}, nil
	}
	if !looksLikeText(head) || !textThroughout(data) {
		return gateResult{}, refuse("this type of file is not accepted")
	}
	switch {
	case isSVG(head):
		return gateResult{MediaType: "image/svg+xml", Ext: ".svg", Data: data}, nil
	case isHTML(head):
		return gateResult{MediaType: "text/html", Ext: ".html", Data: data}, nil
	case len(data) <= gateTextJSONMax && json.Valid(data):
		return gateResult{MediaType: "application/json", Ext: ".json", Data: data}, nil
	}
	return gateResult{MediaType: "text/plain", Ext: ".txt", Data: data}, nil
}

// gateTextJSONMax is the largest file looked at as JSON (parsing is linear, and larger text is plain text).
const gateTextJSONMax = 8 << 20

// refusedKind names the kind of file head and tail are, among those the gate refuses, or "".
func refusedKind(head, tail []byte) string {
	at := func(off int, sig string) bool {
		return len(head) >= off+len(sig) && string(head[off:off+len(sig)]) == sig
	}
	switch {
	case at(0, "\x7fELF"):
		return "executable (ELF)"
	case at(0, "MZ"):
		return "executable (Windows)"
	case at(0, "#!"):
		return "script"
	case at(0, "\xfe\xed\xfa\xce"), at(0, "\xfe\xed\xfa\xcf"), at(0, "\xce\xfa\xed\xfe"), at(0, "\xcf\xfa\xed\xfe"), at(0, "\xca\xfe\xba\xbe"), at(0, "\xca\xfe\xd0\x0d"):
		return "executable (Mach-O or Java)" // also a universal binary, and a Java class (0xCAFEBABE)
	case at(0, "PK\x03\x04"), at(0, "PK\x05\x06"), at(0, "PK\x07\x08"):
		return "archive (zip)"
	case at(0, "\x1f\x8b"):
		return "archive (gzip)"
	case at(0, "7z\xbc\xaf\x27\x1c"):
		return "archive (7z)"
	case at(0, "Rar!\x1a\x07"):
		return "archive (rar)"
	case at(0, "BZh"):
		return "archive (bzip2)"
	case at(0, "\xfd7zXZ\x00"):
		return "archive (xz)"
	case at(0, "\x28\xb5\x2f\xfd"):
		return "archive (zstd)"
	case at(0, "\x04\x22\x4d\x18"):
		return "archive (lz4)"
	case at(257, "ustar"):
		return "archive (tar)"
	case at(0, "!<arch>\n"):
		return "archive (ar)"
	case at(0, "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"):
		return "OLE document"
	case at(0, "{\\rtf"):
		return "RTF document"
	case at(0, "L\x00\x00\x00\x01\x14\x02\x00\x00\x00\x00\x00\xc0\x00\x00\x00\x00\x00\x00F"):
		return "Windows shortcut"
	case at(0, "conectix"), at(0, "vhdxfile"), at(0, "KDMV"), at(0, "QFI\xfb"), at(0, "<<< Oracle VM"), at(32769, "CD001"), at(32769, "BEA01"):
		return "disk image"
	case len(tail) == gateTail && string(tail[:4]) == "koly": // an Apple disk image ends with its 512-byte "koly" block
		return "disk image"
	}
	return ""
}

// looksLikeText reports whether the probe is text: no NUL, valid UTF-8 (a cut multi-byte character at
// the end allowed), and few control characters.
func looksLikeText(head []byte) bool {
	p := head
	if len(p) > gateTextProbe {
		p = p[:gateTextProbe]
	}
	if bytes.IndexByte(p, 0) >= 0 {
		return false
	}
	// A UTF-16 file is full of NULs, caught above; binary has bytes that are not UTF-8. A character cut by
	// the probe's own end (not the file's) is not an error: up to three trailing bytes are given back.
	if len(head) > gateTextProbe {
		for i := 0; i < 3 && !utf8.Valid(p); i++ {
			p = p[:len(p)-1]
		}
	}
	if !utf8.Valid(p) {
		return false
	}
	ctrl := 0
	for _, b := range p {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' && b != '\f' && b != 0x1b {
			ctrl++
		}
	}
	return ctrl*50 < max(len(p), 1) // under 2 percent
}

// textThroughout reports whether the whole file is text: no NUL and valid UTF-8 from the first byte to
// the last (linear), so 9 KiB of text followed by a zip is not "text". The probe above stays as the
// cheap first look.
func textThroughout(data []byte) bool {
	return bytes.IndexByte(data, 0) < 0 && utf8.Valid(data)
}

func firstNonSpace(head []byte) []byte {
	h := bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))
	return bytes.TrimLeft(h, " \t\r\n\f")
}

// isSVG: text whose first element is <svg (after an XML declaration, comments or a doctype).
func isSVG(head []byte) bool {
	h := bytes.ToLower(firstNonSpace(head))
	for {
		switch {
		case bytes.HasPrefix(h, []byte("<?")):
			i := bytes.Index(h, []byte("?>"))
			if i < 0 {
				return false
			}
			h = bytes.TrimLeft(h[i+2:], " \t\r\n")
		case bytes.HasPrefix(h, []byte("<!--")):
			i := bytes.Index(h, []byte("-->"))
			if i < 0 {
				return false
			}
			h = bytes.TrimLeft(h[i+3:], " \t\r\n")
		case bytes.HasPrefix(h, []byte("<!doctype")):
			i := bytes.IndexByte(h, '>')
			if i < 0 {
				return false
			}
			h = bytes.TrimLeft(h[i+1:], " \t\r\n")
		default:
			return bytes.HasPrefix(h, []byte("<svg"))
		}
	}
}

// isHTML: text that opens like an HTML document (a doctype, or one of the tags a page starts with).
func isHTML(head []byte) bool {
	h := bytes.ToLower(firstNonSpace(head))
	for _, p := range []string{"<!doctype html", "<html", "<head", "<body", "<script", "<iframe", "<meta", "<title", "<a ", "<div", "<p>", "<p ", "<h1", "<table", "<style", "<form", "<img", "<link", "<!--"} {
		if bytes.HasPrefix(h, []byte(p)) {
			return true
		}
	}
	return false
}

func imageExt(family string) string {
	switch family {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/tiff":
		return ".tif"
	case "image/heif":
		return ".heic"
	}
	return ".bin"
}

// imageSizeOK refuses an image drawn larger than attachMaxSide on a side or attachMaxPixels in all,
// by its header only (nothing is decoded). A format whose size this does not read (TIFF, HEIF) is not
// measured here: it is refused when it carries metadata, and a viewer's own limits apply.
func imageSizeOK(data []byte, family string) error {
	var w, h int
	switch family {
	case "image/webp":
		var ok bool
		if w, h, ok = webpSize(data); !ok {
			return errors.New("bad image")
		}
	case "image/jpeg", "image/png", "image/gif":
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return errors.New("bad image")
		}
		w, h = cfg.Width, cfg.Height
	default:
		return nil
	}
	if w <= 0 || h <= 0 || w > attachMaxSide || h > attachMaxSide || int64(w)*int64(h) > attachMaxPixels {
		return errors.New("too many pixels")
	}
	return nil
}

// receivedFileName is the name a received file is stored and downloaded under: the sender's name
// reduced to the characters attachName allows, with its extension REPLACED by the one the sniffed
// type gives (never the sender's), so the name never says what the file is not.
func receivedFileName(sent string, res gateResult) string {
	base := filepath.Base(strings.ReplaceAll(sent, "\\", "/"))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = attachName(base)
	base = strings.Trim(base, ". ")
	if base == "" {
		base = "attachment"
	}
	if windowsDeviceName(base) {
		base = "_" + base
	}
	return base + res.Ext
}

// windowsDeviceName reports whether name, once a Windows file system looks at it, is a device: the
// part before the first dot (trailing spaces ignored, any case) is CON, PRN, AUX, NUL, COM0-9,
// LPT0-9, CONIN or CONOUT, so "CON.txt" and "nul.tar.txt" both open the device, not a file.
func windowsDeviceName(name string) bool {
	stem, _, _ := strings.Cut(name, ".")
	switch s := strings.ToUpper(strings.TrimRight(stem, " ")); s {
	case "CON", "PRN", "AUX", "NUL", "CONIN", "CONOUT":
		return true
	default:
		return len(s) == 4 && (s[:3] == "COM" || s[:3] == "LPT") && s[3] >= '0' && s[3] <= '9'
	}
}
