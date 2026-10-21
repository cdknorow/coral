package main

import (
	"reflect"
	"testing"
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
