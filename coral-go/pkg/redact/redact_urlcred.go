package redact

import (
	"strings"
)

// maxURLUser bounds a URL's user name, and maxURLRun its password and host together: past them
// the text is not taken for a URL's credentials.
const (
	maxURLUser = 256
	maxURLRun  = 1024
)

// urlUserByte: a character of a URL's user name (not ':', which ends it).
func urlUserByte(c byte) bool {
	return c > ' ' && c < 0x7f && strings.IndexByte("@/?#\\\"'<>[]{}|`:", c) < 0
}

// urlRunByte: a character of what follows the user name's ':' up to the next space or quote: the
// password ('@', ':' and even a raw '/', '?' or '#' included, Security on #3756) and the host.
// Printable ASCII only: a URL percent-encodes anything else, and taking any other byte would make
// a run of non-ASCII prose one token.
func urlRunByte(c byte) bool {
	return c > ' ' && c < 0x7f && strings.IndexByte("\"'<>`", c) < 0
}

// urlUserGlue measures, after "://", the user name and the ':' after it (redis://:pw@host has
// none). -1: not a password here (an '@', a '/', a space or a quote comes first); -2: b ends first.
func urlUserGlue(b []byte) (int, bool) {
	for i := 0; i < len(b) && i <= maxURLUser; i++ {
		switch c := b[i]; {
		case c == ':':
			countURLScan(i + 1)
			return i + 1, false
		case !urlUserByte(c):
			countURLScan(i + 1)
			return -1, false
		}
	}
	countURLScan(min(len(b), maxURLUser+1))
	if len(b) <= maxURLUser {
		return -2, false
	}
	return -1, false
}

// urlScanned, when a test sets it, counts the bytes the URL finder examines (the "://" search, the user, the password's run and its
// port check): the work of URLPasswordSpans as a number, so that its linear time is tested without a clock (#4908: a timing ratio
// failed under a load of 32 to 54). Nil in production: each helper adds once per call, never per byte.
var urlScanned *int

func countURLScan(n int) {
	if urlScanned != nil {
		*urlScanned += n
	}
}

// urlPassword measures a URL's password: up to the last '@' before the next space, quote or
// "://" (so "p@ss@host" takes "p@ss", and "ab/cd@host" takes "ab/cd"); none without an '@'. The
// run ends at the next "://", where another URL starts: each URL's scan then covers only the text
// up to the next, so a line of them costs linear time (Security on #3756 v2: "://" repeated over
// 4 MiB took 4 s). Digits that end at a '/', '?', '#' or the run's end are a port
// (https://host:8080/path, https://h:80/x/@user).
func urlPassword(b []byte, final bool) (n int, need bool) {
	last := -1
	i := 0
	for ; i < len(b) && i < maxURLRun && urlRunByte(b[i]); i++ {
		if b[i] == ':' && i+2 < len(b) && b[i+1] == '/' && b[i+2] == '/' {
			break
		}
		if b[i] == '@' {
			last = i
		}
	}
	if i == len(b) && i < maxURLRun && !final {
		return 0, true // the run may go on in the next chunk
	}
	d := 0
	for d < i && b[d] >= '0' && b[d] <= '9' {
		d++
	}
	countURLScan(i + d)
	if d > 0 && (d == i || b[d] == '/' || b[d] == '?' || b[d] == '#') {
		return 0, false // a port
	}
	return max(last, 0), false
}
