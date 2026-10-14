package naming

import (
	"os"
	"testing"
)

func TestDefaultLogDir(t *testing.T) {
	defer SetDefaultLogDir("")
	SetDefaultLogDir("")
	if got := DefaultLogDir(); got != os.TempDir() {
		t.Fatalf("unset default = %q, want the temp dir", got)
	}
	SetDefaultLogDir("/x/logs")
	if got := DefaultLogDir(); got != "/x/logs" {
		t.Fatalf("set default = %q", got)
	}
}
