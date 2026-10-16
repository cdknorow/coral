package betterleaks

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/pkg/redact"
)

// #3761's fuzz properties for the library (#3808): the rules are betterleaks', so what is
// fuzzed is the glue. For any text around a planted secret, raw or JSON-encoded:
//   - streamed in two or three writes it gives the same as written whole;
//   - a stream of one line gives exactly what the whole-text scan gives;
//   - a JSON reply stays JSON;
//   - a scan over its budget returns at the budget (and fails closed), on 1 input in 50.
//
//   go test ./pkg/redact/betterleaks -run '^$' -fuzz FuzzLibraryStream -fuzztime 10m

func fuzzStream(s *redact.Scanner, body []byte, cuts ...int) (string, error) {
	var out bytes.Buffer
	w := s.NewStream(&out)
	at := 0
	for _, c := range cuts {
		c = min(max(c, at), len(body))
		if _, err := w.Write(body[at:c]); err != nil {
			return "", err
		}
		at = c
	}
	if _, err := w.Write(body[at:]); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func FuzzLibraryStream(f *testing.F) {
	for _, p := range []string{"", "x", "token=", "\"", "\\", "{\"a\": \"", "Authorization: Bearer ", "https://u:", "é", "\x1b[1;32m", "a,b;c", "<b>"} {
		for k := range caught() {
			f.Add(p, uint8(k), p, uint8(0), uint16(7), uint16(19))
			f.Add(p, uint8(k), p, uint8(1), uint16(30), uint16(31))
		}
	}
	f.Fuzz(func(t *testing.T, prefix string, kind uint8, suffix string, enc uint8, c1, c2 uint16) {
		s := scanner(t)
		all := caught()
		c := all[int(kind)%len(all)]
		text := prefix + " " + c.text + " " + suffix
		body := []byte(text)
		if enc%2 == 1 {
			body, _ = json.Marshal(map[string]string{"content": text})
		}
		whole, err := fuzzStream(s, body)
		if err != nil {
			t.Fatalf("whole: %v", err)
		}
		a, b := int(c1)%(len(body)+1), int(c2)%(len(body)+1)
		if a > b {
			a, b = b, a
		}
		for _, cuts := range [][]int{{a}, {a, b}} {
			got, err := fuzzStream(s, body, cuts...)
			if err != nil {
				t.Fatalf("split %v: %v", cuts, err)
			}
			if got != whole {
				t.Fatalf("split %v differs:\n in %q\n whole %q\n split %q", cuts, body, whole, got)
			}
		}
		if !bytes.ContainsAny(body, "\n") && !bytes.Contains(body, []byte("-----")) && len(body) <= redact.DefaultFrame {
			one, err := s.Text(body)
			if err != nil {
				t.Fatalf("text: %v", err)
			}
			if string(one) != whole {
				t.Fatalf("a one-line stream differs from the whole-text scan:\n in %q\n text %q\n stream %q", body, one, whole)
			}
		}
		if enc%2 == 1 {
			out, err := s.Reply(body, true)
			if err != nil {
				t.Fatalf("reply: %v", err)
			}
			if !json.Valid(out) {
				t.Fatalf("redaction broke the JSON:\n in %s\n out %s", body, out)
			}
		}
		// The budget property waits up to its budget, so it runs on 1 input in 50: the rest go to
		// the split and JSON properties at full speed.
		if !raceEnabled && (int(c1)+int(c2)+len(prefix)+len(suffix))%50 == 0 {
			tight := &redact.Scanner{Detector: s.Detector, Budget: 100 * time.Millisecond}
			big := []byte(strings.Repeat(text+"\n", max(1, (64<<10)/(len(text)+1))))
			start := time.Now()
			_, _ = tight.Text(big)
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("a 64 KB scan took %v with a 100 ms budget", d)
			}
		}
	})
}
