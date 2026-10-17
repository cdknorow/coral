package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The goal generator's one-shot call gets a claude-style JSON answer naming the request (#4204).
func TestPrintOneShotAnswersLikeTheClaudeCLI(t *testing.T) {
	var out bytes.Buffer
	printOneShot(strings.NewReader("CURRENT GOAL: (none)\n\nLATEST USER REQUEST:\nFix the sidebar\n\nAGENT'S LATEST REPLY:\nOn it."), &out)
	var r struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.IsError || r.Result != "Fix the sidebar" {
		t.Fatalf("answer %q: %+v %v", out.String(), r, err)
	}
}
