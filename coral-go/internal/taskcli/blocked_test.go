package taskcli

import (
	"bytes"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPrintBlocked(t *testing.T) {
	var out bytes.Buffer
	PrintBlocked(&out, []byte(`{"error":"no available tasks","blocked_tasks":[{"id":837,"title":"Diagnose Forge failures","reasons":["#835: missing required artifacts: candidate"]}]}`))
	require.Equal(t, "Task #837 blocked: Diagnose Forge failures\n  #835: missing required artifacts: candidate\n", out.String())
	out.Reset()
	PrintBlocked(&out, []byte(`{"error":"no available tasks"}`))
	require.Empty(t, out.String())
}
