package jsonl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixed fixtures and serial runs keep the benchmark workload small. This file
// also runs against the pre-change implementation for like-for-like results.
func BenchmarkRefreshLifecycle(b *testing.B) {
	for _, provider := range []string{"codex", "agy"} {
		b.Run(provider, func(b *testing.B) {
			line := `{"timestamp":"2026-10-02T08:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n"
			if provider == "agy" {
				line = `{"created_at":"2026-10-02T08:00:00Z","type":"USER_INPUT","content":"hello"}` + "\n"
			}
			path := filepath.Join(b.TempDir(), "transcript.jsonl")
			if err := os.WriteFile(path, []byte(strings.Repeat(line, 4096)), 0600); err != nil {
				b.Fatal(err)
			}
			read := func(reader *SessionReader) {
				if provider == "codex" {
					reader.ReadCodexTurnEvent("session", "")
				} else {
					reader.ReadAgyTurnEvent("session", "")
				}
			}
			for _, mode := range []string{"cold", "unchanged", "append"} {
				b.Run(mode, func(b *testing.B) {
					reader := NewSessionReader()
					reader.getOrCreateSessionCache("session").path = path
					read(reader)
					f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
					if err != nil {
						b.Fatal(err)
					}
					defer f.Close()
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if mode == "cold" {
							reader = NewSessionReader()
							reader.getOrCreateSessionCache("session").path = path
						}
						if mode == "append" {
							if _, err := f.WriteString(line); err != nil {
								b.Fatal(err)
							}
						}
						read(reader)
					}
				})
			}
		})
	}
}

func BenchmarkRefreshColdLarge(b *testing.B) {
	path := filepath.Join(b.TempDir(), "transcript.jsonl")
	f, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	line := `{"timestamp":"2026-10-02T08:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n"
	// About 8 MiB, written incrementally so fixture creation does not hide
	// the difference in peak memory between whole-file and streaming reads.
	for i := 0; i < 100000; i++ {
		if _, err := f.WriteString(line); err != nil {
			b.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reader := NewSessionReader()
		reader.getOrCreateSessionCache("session").path = path
		if event, _ := reader.ReadCodexTurnEvent("session", ""); event != "prompt_submit" {
			b.Fatal(event)
		}
	}
}
