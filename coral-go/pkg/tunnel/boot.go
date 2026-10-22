package tunnel

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// This process's boot identity, in every Hello (#5855): a random id made once, and the time the process started.
var thisBoot = struct {
	id string
	at time.Time
}{id: newBootID(), at: time.Now().UTC()}

func newBootID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "" // no id rather than a guessable one: a Hello without it is as before
	}
	return hex.EncodeToString(b[:])
}

// BootID is this process's boot id: 16 hex digits, the same for the life of the process.
func BootID() string { return thisBoot.id }

// validBootID is the form a Hello's boot id must have to be kept: 1 to 32 lowercase hex digits.
func validBootID(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// parsePeerBoot reads a Hello's boot identity: the id when it has the right form, the time when it parses, each "" or zero otherwise (never an error: it is informational).
func parsePeerBoot(id, startedAt string) (string, time.Time) {
	if !validBootID(id) {
		id = ""
	}
	t, err := time.Parse(time.RFC3339, startedAt)
	if err != nil || len(startedAt) > 40 {
		t = time.Time{}
	}
	return id, t.UTC()
}
