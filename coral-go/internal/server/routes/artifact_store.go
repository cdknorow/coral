package routes

// The artifact store's bookkeeping (#3553, from the #3551 audit): its real size (the setting
// artifact_store_max_bytes caps it; 0 is no cap, the default), an hourly cap per uploading
// agent, one lock per digest so a store and a delete of the same artifact never interleave, and
// image inspection by the bytes (not the declared type) before anything is stored.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	uploadWindow        = time.Hour
	uploadWindowCount   = 100       // manual uploads per agent session per hour, refused ones included
	uploadWindowBytes   = 512 << 20 // bytes read per agent session per hour, refused uploads included
	sharedUploadBucket  = "shared"  // token-less uploads (default mode) share one bucket
	staleUploadTempAge  = time.Hour
	artifactUploadTempP = ".upload-" // temp files a streamed upload writes in objects/
	artifactAtomicTempP = ".tmp-"    // writeFileAtomic's and createSidecarOnce's (#3552)
)

var objectNameRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// artifactLocks: one of 256 locks per digest (its first byte). A store (object, sidecar, upload
// record, size) and a delete of the same digest hold it, so neither sees the other half done.
var artifactLocks [256]sync.Mutex

func lockArtifact(hexID string) func() {
	var i byte
	if len(hexID) >= 2 {
		if b, err := hexByte(hexID[0], hexID[1]); err == nil {
			i = b
		}
	}
	artifactLocks[i].Lock()
	return artifactLocks[i].Unlock
}

func hexByte(a, b byte) (byte, error) {
	v := func(c byte) (byte, bool) {
		switch {
		case c >= '0' && c <= '9':
			return c - '0', true
		case c >= 'a' && c <= 'f':
			return c - 'a' + 10, true
		}
		return 0, false
	}
	x, ok1 := v(a)
	y, ok2 := v(b)
	if !ok1 || !ok2 {
		return 0, os.ErrInvalid
	}
	return x<<4 | y, nil
}

// artifactStore is the bookkeeping for one Coral data folder.
type artifactStore struct {
	mu       sync.Mutex
	dir      string // <coralDir>/artifacts/objects
	measured bool
	bytes    int64
	objects  int
	maxBytes func() int64 // the setting; 0 is no cap
	// receivedMaxBytes caps what other Corals and boards have sent (received_cap.go); nil is the default 2 GiB.
	receivedMaxBytes func() int64
	windows          map[string]*uploadUsage
	now              func() time.Time
}

type uploadUsage struct {
	start time.Time
	count int
	bytes int64
}

var (
	artifactStoresMu sync.Mutex
	artifactStores   = map[string]*artifactStore{}
)

// storeFor returns the bookkeeping for coralDir's artifact store, measuring it the first time.
func storeFor(coralDir string) *artifactStore {
	artifactStoresMu.Lock()
	defer artifactStoresMu.Unlock()
	s := artifactStores[coralDir]
	if s == nil {
		s = &artifactStore{dir: filepath.Join(coralDir, "artifacts", "objects"), windows: map[string]*uploadUsage{}, now: time.Now,
			maxBytes: func() int64 { return 0 }}
		artifactStores[coralDir] = s
	}
	s.mu.Lock()
	if !s.measured {
		s.measureLocked()
	}
	s.mu.Unlock()
	return s
}

// measureLocked counts the real objects (objects/<64 hex>, not sidecars or temp files) and
// removes temp files a crash left behind (older than an hour, so a live upload's is safe).
func (s *artifactStore) measureLocked() {
	s.measured = true
	s.bytes, s.objects = 0, 0
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	now := s.now()
	for _, e := range entries {
		name := e.Name()
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		switch {
		case objectNameRe.MatchString(name):
			s.bytes += info.Size()
			s.objects++
		case (strings.HasPrefix(name, artifactUploadTempP) || strings.HasPrefix(name, artifactAtomicTempP)) && now.Sub(info.ModTime()) > staleUploadTempAge:
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	}
}

// size reports the store's size, its object count and its cap (0: none).
func (s *artifactStore) size() (int64, int, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes, s.objects, s.maxBytes()
}

// fits reports whether n more bytes fit under the cap.
func (s *artifactStore) fits(n int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	max := s.maxBytes()
	return max <= 0 || s.bytes+n <= max
}

func (s *artifactStore) added(n int64) {
	s.mu.Lock()
	s.bytes += n
	s.objects++
	s.mu.Unlock()
}

func (s *artifactStore) removed(n int64) {
	s.mu.Lock()
	s.bytes -= n
	if s.bytes < 0 {
		s.bytes = 0
	}
	if s.objects > 0 {
		s.objects--
	}
	s.mu.Unlock()
}

// admitUpload counts one upload against key's hourly window; false when the window is used up
// (then retryIn says when it resets).
func (s *artifactStore) admitUpload(key string) (ok bool, retryIn time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.usageLocked(key)
	count, bytes := windowLimits(key)
	if u.count >= count || u.bytes >= bytes {
		return false, uploadWindow - s.now().Sub(u.start)
	}
	u.count++
	return true, 0
}

// chargeUpload counts n bytes read against key's window, stored or refused (a refused upload
// can't be used to probe for free); false once the window's bytes are used up.
func (s *artifactStore) chargeUpload(key string, n int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.usageLocked(key)
	u.bytes += n
	_, bytes := windowLimits(key)
	return u.bytes <= bytes
}

// windowLimits are the uploads and bytes a window key may use in an hour: the full allowance, or half of it for an image sent through Coral Hub (#6088), so one hub account can never
// use up the allowance of this computer's own dashboard (its key is dashboardUploadBucket).
func windowLimits(key string) (count int, bytes int64) {
	if strings.HasPrefix(key, dashboardHubBucket) {
		return uploadWindowCount / 2, uploadWindowBytes / 2
	}
	return uploadWindowCount, uploadWindowBytes
}

func (s *artifactStore) usageLocked(key string) *uploadUsage {
	now := s.now()
	u := s.windows[key]
	if u == nil || now.Sub(u.start) >= uploadWindow {
		for k, w := range s.windows {
			if now.Sub(w.start) >= uploadWindow {
				delete(s.windows, k)
			}
		}
		u = &uploadUsage{start: now}
		s.windows[key] = u
	}
	return u
}

// ── Images: by the bytes, whatever the declared type ────────────────────

// sniffImageFamily names the image format of data by its magic bytes: "image/jpeg",
// "image/png", "image/gif", "image/webp", "image/heif" (HEIC, HEIF, AVIF: ISO-BMFF with an
// image brand), "image/tiff", or "" for anything else.
func sniffImageFamily(data []byte) string {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif"
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	case len(data) >= 4 && (string(data[0:4]) == "II*\x00" || string(data[0:4]) == "MM\x00*"):
		return "image/tiff"
	case len(data) >= 12 && string(data[4:8]) == "ftyp":
		switch string(data[8:12]) {
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1", "avif", "avis":
			return "image/heif"
		}
	}
	return ""
}

// errImageMetadata: an image the server can't strip carries EXIF or XMP.
type errImageMetadata string

func (e errImageMetadata) Error() string { return string(e) }

// cleanUploadedImage strips or refuses an image's metadata (finding 5): JPEG, PNG and GIF are
// stripped without re-encoding (#3515's strippers); WebP and HEIF/AVIF that carry EXIF or XMP,
// and TIFF with any tag beyond the pixel data, are refused (they can't be rewritten safely here). It returns the bytes to store and
// whether they changed. Anything that isn't an image is returned as is.
func cleanUploadedImage(data []byte) ([]byte, bool, error) {
	switch family := sniffImageFamily(data); family {
	case "image/jpeg", "image/png", "image/gif":
		out, err := stripImageMetadata(data, family)
		if err != nil {
			return nil, false, errImageMetadata("this image could not be read to remove its metadata")
		}
		return out, !bytes.Equal(out, data), nil
	case "image/webp":
		if _, err := stripImageMetadata(data, family); err != nil {
			return nil, false, errImageMetadata("this WebP image carries EXIF or XMP metadata (camera, location); save it without metadata and upload it again")
		}
	case "image/heif":
		if bytes.Contains(data, []byte("Exif")) || bytes.Contains(data, []byte("application/rdf+xml")) || bytes.Contains(data, []byte("x:xmpmeta")) {
			return nil, false, errImageMetadata("this HEIC/AVIF image carries EXIF or XMP metadata (camera, location); export it as PNG or JPEG, or without metadata, and upload it again")
		}
	case "image/tiff":
		if tiffHasMetadata(data) {
			return nil, false, errImageMetadata("this TIFF image carries metadata (EXIF, GPS, XMP, or tags such as Artist, Make, Model, Software or a description); export it as PNG or without metadata, and upload it again")
		}
	}
	return data, false, nil
}

// tiffPixelTags are the TIFF tags that only describe the pixels (their layout, encoding,
// colour and resolution). Any other tag (EXIF, GPS, XMP, IPTC, Photoshop, Artist, Copyright,
// ImageDescription, Make, Model, Software, DateTime, HostComputer, ...) can hold names,
// places or machine names, so a TIFF carrying one is refused (Security's #3553 review).
var tiffPixelTags = map[uint16]bool{
	0x00FE: true, 0x00FF: true, // NewSubfileType, SubfileType
	0x0100: true, 0x0101: true, 0x0102: true, 0x0103: true, // width, height, bits per sample, compression
	0x0106: true, 0x0107: true, 0x0108: true, 0x0109: true, 0x010A: true, // photometric, thresholding, cell size, fill order
	0x0111: true, 0x0112: true, 0x0115: true, 0x0116: true, 0x0117: true, // strip offsets, orientation, samples per pixel, rows per strip, strip byte counts
	0x0118: true, 0x0119: true, 0x011A: true, 0x011B: true, 0x011C: true, // min/max sample value, x/y resolution, planar configuration
	0x0128: true, 0x013D: true, 0x013E: true, 0x013F: true, 0x0140: true, // resolution unit, predictor, white point, primaries, colour map
	0x0142: true, 0x0143: true, 0x0144: true, 0x0145: true, // tile width, length, offsets, byte counts
	0x0152: true, 0x0153: true, 0x0154: true, 0x0155: true, // extra samples, sample format, s min/max sample value
	0x0156: true, 0x015B: true, // transfer range, JPEG tables
	0x0211: true, 0x0212: true, 0x0213: true, 0x0214: true, // YCbCr coefficients, subsampling, positioning, reference black/white
}

// tiffHasMetadata reports whether any IFD of a TIFF (the chain from the header, bounded) has a
// tag outside tiffPixelTags. A structure it can't read counts as carrying metadata.
func tiffHasMetadata(data []byte) bool {
	if len(data) < 8 {
		return true
	}
	var bo binary.ByteOrder = binary.LittleEndian
	if data[0] == 'M' {
		bo = binary.BigEndian
	}
	off := int(bo.Uint32(data[4:8]))
	for ifds := 0; off != 0; ifds++ {
		if ifds >= 64 || off < 8 || off+2 > len(data) {
			return true
		}
		n := int(bo.Uint16(data[off : off+2]))
		end := off + 2 + n*12
		if end+4 > len(data) {
			return true
		}
		for i := 0; i < n; i++ {
			if !tiffPixelTags[bo.Uint16(data[off+2+i*12:])] {
				return true
			}
		}
		off = int(bo.Uint32(data[end : end+4]))
	}
	return false
}
