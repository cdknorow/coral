package routes

import "sync"

// What the redactor makes of a text takes about 40 microseconds for 400 bytes, and a search through Coral Hub tests every message or task of
// a board against what a viewer sees (#4745): 20,000 messages took 0.8 s, on every keystroke of the search box, though no message had
// changed. shownTextMemoized remembers the results by the text (a board's texts do not change), bounded to 64 MiB in all (the whole memo
// is dropped when it is full) and to texts under 16 KiB; a text the redactor could not check (the empty answer shownText gives for one) is
// never remembered, so a failure is not kept after the redactor works again.

const (
	shownMemoLimit   = 64 << 20
	shownMemoMaxText = 16 << 10
)

var shownMemo = struct {
	sync.Mutex
	m     map[string]string
	bytes int
}{m: map[string]string{}}

func resetShownMemo() {
	shownMemo.Lock()
	shownMemo.m, shownMemo.bytes = map[string]string{}, 0
	shownMemo.Unlock()
}

func shownTextMemoized(s string) string {
	if s == "" || len(s) > shownMemoMaxText {
		return shownText(s)
	}
	shownMemo.Lock()
	v, ok := shownMemo.m[s]
	shownMemo.Unlock()
	if ok {
		return v
	}
	v = shownText(s)
	if v == "" { // not checkable: hidden this time, asked again next time
		return v
	}
	shownMemo.Lock()
	if shownMemo.bytes+len(s)+len(v) > shownMemoLimit {
		shownMemo.m, shownMemo.bytes = map[string]string{}, 0
	}
	shownMemo.m[s] = v
	shownMemo.bytes += len(s) + len(v)
	shownMemo.Unlock()
	return v
}
