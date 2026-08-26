package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestContentLengthFramingFragmented(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1}`)
	var framed bytes.Buffer
	if err := writeFrame(&framed, body); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(&oneByteReader{r: bytes.NewReader(framed.Bytes())})
	got, err := readFrame(reader)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestContentLengthHeaderLimits(t *testing.T) {
	long := "X: " + strings.Repeat("a", maxHeaderLine) + "\r\n\r\n"
	if _, err := readFrame(bufio.NewReader(strings.NewReader(long))); err == nil {
		t.Fatal("accepted oversized header")
	}
	var many strings.Builder
	for i := 0; i < maxHeaders; i++ {
		many.WriteString("X: y\r\n")
	}
	many.WriteString("\r\n")
	if _, err := readFrame(bufio.NewReader(strings.NewReader(many.String()))); err == nil {
		t.Fatal("accepted too many headers")
	}
}

type oneByteReader struct{ r *bytes.Reader }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.r.Read(p)
}

func TestMethodAllowlist(t *testing.T) {
	for _, method := range []string{"textDocument/hover", "textDocument/definition", "textDocument/references", "textDocument/didOpen"} {
		if !MethodAllowed(method) {
			t.Errorf("%s should be allowed", method)
		}
	}
	for _, method := range []string{"initialize", "workspace/executeCommand", "shutdown"} {
		if MethodAllowed(method) {
			t.Errorf("%s should be rejected", method)
		}
	}
}

func TestValidateRawID(t *testing.T) {
	id, err := normalizeID(json.Number("12"))
	if err != nil || string(id) != "12" {
		t.Fatalf("%s, %v", id, err)
	}
}
