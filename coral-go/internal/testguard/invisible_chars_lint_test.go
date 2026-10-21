package testguard

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Census (#5616): no literal invisible character in committed source. A zero-width space, a bidi control (the Trojan Source family), U+2028/U+2029 or any other format character
// is invisible to a reviewer: it hid in a regex range (#5584), in rune literals (#5572's boards.go) and in test strings, and it can make code read differently from what it does. Where one is
// needed (a sanitiser, or a test of one), write it as an escape (\u200b) so it shows; this test fails with file:line:column and the code point for any literal one, except where the
// allowlist below names the file and the character with the reason a literal is needed there.
//
// It scans every tracked text file (git ls-files; a walk of the tree when git is not there): see isTextSource. The characters: every Unicode
// format character (category Cf: zero-width, bidi marks and embeddings and isolates, word joiner, soft hyphen, the invisible operators, tag characters, BOM), the line and paragraph
// separators U+2028 and U+2029, and the combining grapheme joiner U+034F. Variation selectors are not on the list (an emoji needs them) and neither are the non-ASCII spaces.
//
// Caching note: this reads files outside this package, so go test may serve a cached pass after a file is edited without touching a .go file: run it with -count=1 to be sure.

// invisibleAllowed is "path:U+XXXX" with the reason this character may be literal in that file. Keep it short: an escape is nearly always the better way.
var invisibleAllowed = map[string]string{}

// isInvisible reports whether r is one of the characters this census refuses.
func isInvisible(r rune) bool {
	return unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' || r == '\u034f'
}

// isTextSource says whether a file is text a reviewer reads: no NUL byte in its first 8 KiB and valid UTF-8 throughout (a Latin-1 or binary file is not scanned), and at most 8 MiB. Every tracked
// file of that kind is scanned whatever its extension: an invisible character hides as well in a .yaml, a .rb cask, a .ps1, a .wxs, a golden file or a text list as in a .go.
func isTextSource(src []byte) bool {
	if len(src) > 8<<20 {
		return false
	}
	head := src
	if len(head) > 8<<10 {
		head = head[:8<<10]
	}
	return !bytes.Contains(head, []byte{0}) && utf8.Valid(src)
}

// variationSelectorFindings returns "line:col U+XXXX" for each variation selector that does not belong to an emoji: the supplementary ones (U+E0100 to U+E01EF) and U+FE00 to U+FE0D are never
// needed in source (they can carry hidden data), and U+FE0E and U+FE0F (text and emoji presentation) only follow a non-ASCII character or a keycap base (a digit, # or *).
func variationSelectorFindings(src []byte) []string {
	var out []string
	line, col := 1, 1
	var prev rune
	for i := 0; i < len(src); {
		r, n := utf8.DecodeRune(src[i:])
		if r == utf8.RuneError && n == 1 {
			i++
			col++
			continue
		}
		switch {
		case r >= 0xE0100 && r <= 0xE01EF, r >= 0xFE00 && r <= 0xFE0D:
			out = append(out, fmt.Sprintf("%d:%d U+%04X", line, col, r))
		case r == 0xFE0E || r == 0xFE0F:
			if !(prev >= 0x80 || prev == '#' || prev == '*' || (prev >= '0' && prev <= '9')) {
				out = append(out, fmt.Sprintf("%d:%d U+%04X", line, col, r))
			}
		}
		prev = r
		if r == '\n' {
			line++
			col = 1
		} else {
			col++
		}
		i += n
	}
	return out
}

// invisibleFindings returns "line:col U+XXXX" for each literal invisible character of src, in order.
func invisibleFindings(src []byte) []string {
	var out []string
	line, col := 1, 1
	for i := 0; i < len(src); {
		r, n := utf8.DecodeRune(src[i:])
		if r == utf8.RuneError && n == 1 {
			i++
			col++
			continue
		}
		if isInvisible(r) {
			out = append(out, fmt.Sprintf("%d:%d U+%04X", line, col, r))
		}
		if r == '\n' {
			line++
			col = 1
		} else {
			col++
		}
		i += n
	}
	return out
}

// trackedFiles lists the files to scan, relative to root, slash-separated.
func trackedFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	if _, err := exec.LookPath("git"); err == nil {
		cmd := exec.Command("git", "-C", root, "ls-files", "-z")
		if out, err := cmd.Output(); err == nil && len(out) > 0 {
			for _, f := range bytes.Split(out, []byte{0}) {
				if len(f) > 0 {
					files = append(files, string(f))
				}
			}
			sort.Strings(files)
			return files
		}
	}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if rel, err := filepath.Rel(root, p); err == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(files)
	return files
}

func TestNoLiteralInvisibleCharactersInCommittedSource(t *testing.T) {
	root := repoRootForLint(t)
	used := map[string]bool{}
	var bad []string
	scanned := 0
	for _, rel := range trackedFiles(t, root) {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			continue // a tracked file that is not there (a sparse checkout): nothing to read
		}
		if !isTextSource(src) {
			continue // an image, an archive, a binary: nothing a reviewer reads
		}
		scanned++
		for _, f := range append(invisibleFindings(src), variationSelectorFindings(src)...) {
			cp := f[strings.Index(f, "U+"):]
			key := rel + ":" + cp
			if _, ok := invisibleAllowed[key]; ok {
				used[key] = true
				continue
			}
			bad = append(bad, fmt.Sprintf("%s:%s: a literal %s: write it as an escape (\\u%s)", rel, strings.SplitN(f, " ", 2)[0], cp, strings.ToLower(strings.TrimPrefix(cp, "U+"))))
		}
	}
	if scanned < 500 {
		t.Fatalf("only %d source files scanned: the file list is wrong, so this census checked nothing", scanned)
	}
	var stale []string
	for key := range invisibleAllowed {
		if !used[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(bad) > 0 {
		t.Errorf("literal invisible characters in committed source (a reviewer cannot see them; use an escape, or add the file and character to invisibleAllowed with the reason):\n%s", strings.Join(bad, "\n"))
	}
	if len(stale) > 0 {
		t.Errorf("invisibleAllowed lists characters that are no longer there (remove them):\n%s", strings.Join(stale, "\n"))
	}
}

// The census must see each kind it is for, and must not see what it is not for.
func TestInvisibleCharacterCensusKnowsWhatItIsFor(t *testing.T) {
	for name, r := range map[string]rune{
		"zero-width space": '\u200b', "zero-width non-joiner": '\u200c', "zero-width joiner": '\u200d', "left-to-right mark": '\u200e', "right-to-left mark": '\u200f',
		"left-to-right embedding": '\u202a', "right-to-left override": '\u202e', "left-to-right isolate": '\u2066', "pop directional isolate": '\u2069',
		"line separator": '\u2028', "paragraph separator": '\u2029', "word joiner": '\u2060', "soft hyphen": '\u00ad', "BOM": '\ufeff', "arabic letter mark": '\u061c',
		"combining grapheme joiner": '\u034f', "tag letter": '\U000e0041', "invisible separator": '\u2063',
	} {
		got := invisibleFindings([]byte("a" + string(r) + "b"))
		if len(got) != 1 {
			t.Errorf("%s (U+%04X): not found: %v", name, r, got)
		}
	}
	for name, s := range map[string]string{
		"plain text": "hello, world\n", "an accented letter": "café", "an emoji with its variation selector": "⚠\ufe0f", "a non-breaking space": "a b",
		"CJK": "日本", "the escape written out": `"\u200b"`, "a tab and CRLF": "a\tb\r\n",
	} {
		if got := invisibleFindings([]byte(s)); len(got) != 0 {
			t.Errorf("%s: wrongly found %v", name, got)
		}
	}
	got := invisibleFindings([]byte("one\ntwo \u200b x\nthree\u202e"))
	want := []string{"2:5 U+200B", "3:6 U+202E"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("positions: got %v, want %v", got, want)
	}
}

// Variation selectors can carry hidden data when they are not part of an emoji: the census refuses the ones an emoji never needs and the others unless they follow what they belong to.
func TestVariationSelectorsThatAreNotEmojiAreFound(t *testing.T) {
	for name, s := range map[string]string{
		"a supplementary selector": "a\U000e0100b", "a selector VS1": "a\ufe00", "an emoji selector after ASCII text": "ab\ufe0f", "a text selector after a letter": "x\ufe0e",
		"a run of hidden ones": "⚠\U000e0101\U000e0102",
	} {
		if got := variationSelectorFindings([]byte(s)); len(got) == 0 {
			t.Errorf("%s: not found", name)
		}
	}
	for name, s := range map[string]string{
		"an emoji with its selector": "⚠\ufe0f", "a keycap": "1\ufe0f⃣", "a text-presentation arrow": "↔\ufe0e", "plain text": "hello",
	} {
		if got := variationSelectorFindings([]byte(s)); len(got) != 0 {
			t.Errorf("%s: wrongly found %v", name, got)
		}
	}
	if got := variationSelectorFindings([]byte("one\ntwo ⚠\nx\U000e0100")); strings.Join(got, "|") != "3:2 U+E0100" {
		t.Errorf("position: %v", got)
	}
}

// Every tracked text file is read, whatever its extension; a binary or a non-UTF-8 file is not.
func TestIsTextSourceReadsTextOfAnyExtensionAndNotBinaries(t *testing.T) {
	if !isTextSource([]byte("name: x\non: push\n")) || !isTextSource([]byte("cask \"coral\" do\nend\n")) || !isTextSource(nil) {
		t.Error("text is not text")
	}
	if isTextSource([]byte("PK\x03\x04\x00\x00")) || isTextSource([]byte{0x89, 'P', 'N', 'G', 0x00}) || isTextSource([]byte("caf\xe9 latin-1")) {
		t.Error("a binary or a non-UTF-8 file is text")
	}
	if isTextSource(make([]byte, 9<<20)) {
		t.Error("a file over 8 MiB is scanned")
	}
}
