package routes

import (
	"time"

	"github.com/subgentic/coral/internal/store"
)

// fileClockSlack is how far a file's time may fall short of its row's creation and still be taken as written after it: some file systems keep whole seconds only.
const fileClockSlack = 2 * time.Second

// writtenBefore reports whether a file last written at mtime is older than the row (a task, a workflow run) it would be served for. Ids that start again after a restore from an older backup or a reset with the data
// folder kept would otherwise serve an older row's file under the new id (#6948, Security's #6946): a file that cannot have been written for this row is refused. A creation time that cannot be read fails closed.
func writtenBefore(mtime time.Time, createdAt string) bool {
	for _, layout := range []string{store.ISOFormat, time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if created, err := time.Parse(layout, createdAt); err == nil {
			return mtime.Add(fileClockSlack).Before(created)
		}
	}
	return true
}
