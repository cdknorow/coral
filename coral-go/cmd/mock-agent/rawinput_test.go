package main

import (
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/vtscreen"
)

func TestInputBoxFeed(t *testing.T) {
	var got []string
	b := &inputBox{}
	submit := func(s string) { got = append(got, s) }

	b.feed([]byte("hello\r"), submit)
	// A paste's CR is text; the CR after it submits. Its start marker is split across two reads.
	b.feed([]byte("\x1b[20"), submit)
	b.feed([]byte("0~one\rtwo\x1b[201~"), submit)
	if d := b.display(); d != "[Pasted text #1 +2 lines]" {
		t.Errorf("display of a multi-line paste = %q", d)
	}
	b.feed([]byte("\r"), submit)
	b.feed([]byte("typed"), submit)
	if d := b.display(); d != "typed" {
		t.Errorf("display of typed text = %q", d)
	}
	if want := []string{"hello", "one\ntwo"}; !reflect.DeepEqual(got, want) {
		t.Errorf("submissions = %q, want %q", got, want)
	}
}

func TestSplitWords(t *testing.T) {
	got := splitWords(`"C:/Program Files (x86)/Coral/coral-hook-agentic-state.exe" --flag x`)
	want := []string{"C:/Program Files (x86)/Coral/coral-hook-agentic-state.exe", "--flag", "x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitWords = %q, want %q", got, want)
	}
}

func TestInputBoxEditingKeys(t *testing.T) {
	b := &inputBox{}
	b.feed([]byte("xyz\x7f"), func(string) {})
	if d := b.display(); d != "xy" {
		t.Errorf("after Backspace = %q", d)
	}
	b.feed([]byte("\x15"), func(string) {})
	if d := b.display(); d != "" {
		t.Errorf("after Ctrl-U = %q", d)
	}
}

// The trigger opens a menu Coral's own prompt reader recognises, so a send that would press Enter
// on it can be caught (audit F4); an Enter while it is open answers it, never submits the text.
func TestPromptTriggerOpensAMenuCoralRecognises(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	var got []string
	b := &inputBox{trigger: "OPEN_PROMPT_NOW"}
	b.feed([]byte("please OPEN_PROMPT_NOW then"), func(s string) { got = append(got, s) })
	open := b.promptOpen
	b.feed([]byte("\r"), func(s string) { got = append(got, s) })
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)

	if !open {
		t.Fatal("the trigger did not open the prompt")
	}
	if p, _ := ptymanager.PromptOnScreen(vtscreen.Render(out, 80, 24)); !p {
		t.Errorf("Coral does not see the mock's prompt on its screen:\n%s", vtscreen.Render(out, 80, 24))
	}
	if len(got) != 0 || !strings.Contains(string(out), "PROMPT ANSWERED") {
		t.Errorf("an Enter on the open prompt must answer it, not submit the text (submitted %q)", got)
	}
}

// A key's escape sequence is a key, not text: an arrow pressed in the box leaves the box as it was,
// so a control line submitted after it is still exactly that line.
func TestInputBoxSkipsKeySequences(t *testing.T) {
	var got []string
	b := &inputBox{}
	b.feed([]byte("\x1bOA\x1b[A\x1b[Z\x1b[15~MOCK_DECCKM_OFF\r"), func(s string) { got = append(got, s) })
	if !reflect.DeepEqual(got, []string{"MOCK_DECCKM_OFF"}) {
		t.Errorf("submissions = %q", got)
	}
}
