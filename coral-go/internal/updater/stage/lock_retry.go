package stage

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"runtime"
	"time"
)

// On Windows a file another check has open (reading a lock to judge it) cannot be removed while it is open: Go opens files without FILE_SHARE_DELETE, so the removal
// fails with a sharing violation. The reader holds it for microseconds, so a lock this check made is removed with a short retry there; a takeover lock that stayed behind made
// every later check back off for takeoverStale (#5403). A retry is only ever of the removal of a file that still holds exactly what this check wrote (below): never a rename,
// and never a lock another check made meanwhile (the first version retried every removal, and two checks held the lock together on the 132 gate).
const (
	lockFileRetries = 50
	lockFileRetryIn = 10 * time.Millisecond // 0.5 s at most
)

func newNonce() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// removeOwn removes path when it still holds exactly body, the bytes this check wrote when it made the lock. Before each attempt the file is read and compared: a file with other
// content is another check's lock and is left alone, a file that is gone is done, a file that cannot be read is left (it is retried only on Windows, where a reader or a mover
// holds it for a moment). On Windows a refusal to remove that is not "not there" is retried for up to 0.5 s; elsewhere there is one attempt.
func removeOwn(path string, body []byte) {
	for i := 0; ; i++ {
		got, err := os.ReadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return
		case err == nil && !bytes.Equal(got, body):
			return // not the lock this check made
		case err == nil:
			rerr := os.Remove(path)
			if rerr == nil || errors.Is(rerr, fs.ErrNotExist) || !retryable(rerr) {
				return
			}
		case !retryable(err):
			return
		}
		if i >= lockFileRetries {
			return
		}
		time.Sleep(lockFileRetryIn)
	}
}

// retryable: a failure on Windows that is not "it is not there", which is a sharing violation or an access denial from an open handle; never off Windows.
func retryable(err error) bool {
	return err != nil && runtime.GOOS == "windows" && !errors.Is(err, fs.ErrNotExist)
}
