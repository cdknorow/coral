package transcriptlink

import (
	"strconv"
	"unicode/utf16"
)

// maxProjectDirLen is how long the Claude CLI lets a project folder's name be before it shortens it (its MAX_SANITIZED_LENGTH).
const maxProjectDirLen = 200

// ClaudeProjectDirName is the name of the folder under ~/.claude/projects that the Claude CLI keeps a working directory's transcripts in, derived the way the CLI derives it
// (read from its own code, #5308): every UTF-16 code unit of the path that is not a letter or a digit becomes "-" (a dot, an underscore, a space, a colon, a backslash, a
// letter outside ASCII, each half of an emoji), and a name longer than 200 characters is its first 200, a "-", and the base-36 form of abs(h), where h is the 32-bit
// string hash (h = h*31 + unit, wrapping) of the ORIGINAL path's UTF-16 code units. Going by "/" alone misses the folder of any path with a dot, an underscore or a space.
func ClaudeProjectDirName(path string) string {
	units := utf16.Encode([]rune(path))
	name := make([]byte, len(units))
	for i, u := range units {
		if u < 0x80 && (u >= 'a' && u <= 'z' || u >= 'A' && u <= 'Z' || u >= '0' && u <= '9') {
			name[i] = byte(u)
		} else {
			name[i] = '-'
		}
	}
	if len(name) <= maxProjectDirLen {
		return string(name)
	}
	var h int32
	for _, u := range units {
		h = h<<5 - h + int32(u)
	}
	n := int64(h)
	if n < 0 {
		n = -n // abs of the 32-bit value, which for the one value -2^31 is 2^31
	}
	return string(name[:maxProjectDirLen]) + "-" + strconv.FormatInt(n, 36)
}
