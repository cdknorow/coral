package ptymanager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Screen corpus (#3484, for #3475): real tmux capture-pane -p -e screens of each CLI
// and version under testdata/screens/<cli>/<version>/, one file per state, labelled in
// that directory's expect.json. Every detector the gate uses runs over every capture,
// so a new CLI version is a new directory, and a detector change shows each screen it
// reads differently. CORAL_SCREEN_CORPUS_DUMP=1 prints the labels the detectors give
// now, to start a new directory's expect.json (check every value by eye first).

// screenLabel is one capture's expected reading.
type screenLabel struct {
	State          string `json:"state"`           // idle, draft, suggestion, permission_menu, ...
	Prompt         bool   `json:"prompt"`          // PromptOnScreen: something holds sends
	WhyPrefix      string `json:"why_prefix"`      // the start of PromptOnScreen's reason ("" when no prompt)
	PromptHold     bool   `json:"prompt_hold"`     // isPromptHold: a real prompt, not a footer or busy screen
	InputBox       bool   `json:"input_box"`       // InputBoxOnScreen
	Box            string `json:"box"`             // inputBoxState for a one-line nudge: unknown, empty, ours, other
	SuggestionOnly bool   `json:"suggestion_only"` // suggestionOnly on the styled screen
	Note           string `json:"note,omitempty"`
	// KnownGap: what the detectors get wrong on this screen today. The labels are the
	// right reading; the test logs the difference instead of failing, and fails once
	// the detectors read it right, so the gap is removed from expect.json then.
	KnownGap string `json:"known_gap,omitempty"`
}

// corpusNudge is the text the box is read against: never in any capture, so a box
// with text is "other", never "ours".
const corpusNudge = "You have 1 unread message on the message board. Run 'coral-board read' to see them."

var boxStateNames = map[boxState]string{boxUnknown: "unknown", boxEmpty: "empty", boxOurs: "ours", boxOther: "other"}

// whyPrefixes are the reasons PromptOnScreen gives, by their fixed start.
var whyPrefixes = []string{"selection menu", "prompt question", footerWhy, busyWhy}

func whyPrefix(why string) string {
	for _, p := range whyPrefixes {
		if strings.HasPrefix(why, p) {
			return p
		}
	}
	return why
}

// sgrRe matches an SGR sequence (colour, faint, ...), the only escapes capture-pane -e
// writes.
var sgrRe = regexp.MustCompile("\x1b\\[[0-9;:]*m")

// readScreen labels a capture with what the detectors say now. The gate reads the
// plain screen (capture-pane -p, no styles) for the prompt and the box, and the styled
// one (capture-pane -p -e) only for a faint suggestion, so the capture is read both ways.
func readScreen(styled string) screenLabel {
	screen := sgrRe.ReplaceAllString(styled, "")
	open, why := PromptOnScreen(screen)
	l := screenLabel{
		Prompt:         open,
		InputBox:       InputBoxOnScreen(screen),
		Box:            boxStateNames[inputBoxState(screen, corpusNudge, false)],
		SuggestionOnly: suggestionOnly(styled),
	}
	if open {
		l.WhyPrefix = whyPrefix(why)
		l.PromptHold = isPromptHold(why)
	}
	return l
}

func TestScreenCorpus(t *testing.T) {
	root := filepath.Join("testdata", "screens")
	dirs, err := filepath.Glob(filepath.Join(root, "*", "*"))
	require.NoError(t, err)
	require.NotEmpty(t, dirs, "the corpus is there")
	dump := os.Getenv("CORAL_SCREEN_CORPUS_DUMP") == "1"
	checked, gaps := 0, 0
	for _, dir := range dirs {
		b, err := os.ReadFile(filepath.Join(dir, "expect.json"))
		require.NoError(t, err, "%s has an expect.json", dir)
		var want map[string]screenLabel
		require.NoError(t, json.Unmarshal(b, &want), dir)
		files, err := os.ReadDir(dir)
		require.NoError(t, err)
		names := map[string]bool{}
		for _, f := range files {
			if f.Name() != "expect.json" {
				names[f.Name()] = true
			}
		}
		for name := range want {
			if !names[name] {
				t.Errorf("%s: expect.json labels %s, which is not in the directory", dir, name)
			}
		}
		sorted := make([]string, 0, len(names))
		for n := range names {
			sorted = append(sorted, n)
		}
		sort.Strings(sorted)
		for _, name := range sorted {
			path := filepath.Join(dir, name)
			screen, err := os.ReadFile(path)
			require.NoError(t, err)
			got := readScreen(string(screen))
			w, ok := want[name]
			if dump {
				got.State, got.Note, got.KnownGap = w.State, w.Note, w.KnownGap
				j, _ := json.Marshal(got)
				fmt.Printf("%q: %s,\n", path, j)
			}
			if !ok {
				t.Errorf("%s: no label in expect.json", path)
				continue
			}
			got.State, got.Note, got.KnownGap = w.State, w.Note, w.KnownGap
			switch {
			case w.KnownGap != "" && got == w:
				t.Errorf("%s (%s): the known gap is fixed (%s): remove known_gap from expect.json", path, w.State, w.KnownGap)
			case w.KnownGap != "":
				t.Logf("KNOWN GAP %s (%s): %s: %s", path, w.State, w.KnownGap, labelDiff(w, got))
				gaps++
			case got != w:
				t.Errorf("%s (%s): %s", path, w.State, labelDiff(w, got))
			}
			checked++
		}
	}
	t.Logf("%d captures checked in %d CLI/version directories, %d known gaps", checked, len(dirs), gaps)
}

// labelDiff names each field that differs, as field want/got.
func labelDiff(want, got screenLabel) string {
	var d []string
	add := func(field string, w, g any) {
		if w != g {
			d = append(d, fmt.Sprintf("%s want %v got %v", field, w, g))
		}
	}
	add("prompt", want.Prompt, got.Prompt)
	add("why_prefix", want.WhyPrefix, got.WhyPrefix)
	add("prompt_hold", want.PromptHold, got.PromptHold)
	add("input_box", want.InputBox, got.InputBox)
	add("box", want.Box, got.Box)
	add("suggestion_only", want.SuggestionOnly, got.SuggestionOnly)
	return strings.Join(d, "; ")
}
