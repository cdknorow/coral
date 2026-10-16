package background

import "sync"

// filesChanged are the functions told when the git poller sees a working directory's set of
// changed files change (a file added, removed or modified): caches of that folder's file list
// drop it (#3721, the file search's cache).
var filesChanged struct {
	sync.RWMutex
	fns []func(workdir string)
}

// OnFilesChanged registers fn to be told, with the working directory, when the git poller sees
// its changed files change. fn must be quick and must not block.
func OnFilesChanged(fn func(workdir string)) {
	filesChanged.Lock()
	filesChanged.fns = append(filesChanged.fns, fn)
	filesChanged.Unlock()
}

// NotifyFilesChanged tells the registered functions that workdir's files changed (the git
// poller calls it; another watcher may too).
func NotifyFilesChanged(workdir string) {
	filesChanged.RLock()
	fns := filesChanged.fns
	filesChanged.RUnlock()
	for _, fn := range fns {
		fn(workdir)
	}
}
