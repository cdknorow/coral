package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// ImageRef names a pasted image by its own bytes: the first 16 bytes of the SHA-256 of its base64 text, in hex (32 characters). The transcript
// reader puts it on a chip (attachments[].ref) in place of the image, and GET /api/sessions/live/{name}/attachment/{ref} finds the image in the
// transcript again by hashing its image blocks, so the reference holds no path and no offset, and a name that matches nothing in the transcript
// is not found (#5032).
func ImageRef(base64Text string) string {
	sum := sha256.Sum256([]byte(base64Text))
	return hex.EncodeToString(sum[:16])
}

// ValidImageRef reports whether s has the shape ImageRef gives.
func ValidImageRef(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// DataURLBase64 is the base64 text of an image data URL ("data:image/png;base64,..."), and false for anything else (a file path, a link, a data URL
// that is not base64).
func DataURLBase64(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if len(ref) < 12 || !strings.EqualFold(ref[:11], "data:image/") {
		return "", false
	}
	head, payload, ok := strings.Cut(ref, ",")
	if !ok || !strings.HasSuffix(strings.ToLower(head), ";base64") {
		return "", false
	}
	return payload, payload != ""
}
