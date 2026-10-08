package ptymanager

import (
	"strings"
	"testing"
	"time"
)

func TestPTYBackendSendLineSubmitsSeparately(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()

	if err := backend.Spawn("line-test", "test", t.TempDir(), "sid-line", "cat", 80, 24); err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	ch, err := backend.Attach("line-test", "ws-line")
	if err != nil {
		t.Fatalf("Attach failed: %v", err)
	}
	if err := backend.SendLine("line-test", "submitted-line"); err != nil {
		t.Fatalf("SendLine failed: %v", err)
	}

	var output strings.Builder
	timeout := time.After(3 * time.Second)
	for !strings.Contains(output.String(), "submitted-line") {
		select {
		case data, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed, got: %q", output.String())
			}
			output.Write(data)
		case <-timeout:
			t.Fatalf("timeout waiting for output, got: %q", output.String())
		}
	}
}

func TestEnterKeyIsSingleByte(t *testing.T) {
	if len(EnterKey) != 1 {
		t.Fatalf("EnterKey = %q, want a single byte so TUIs read it as submit", EnterKey)
	}
}
