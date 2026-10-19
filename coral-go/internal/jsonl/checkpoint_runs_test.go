package jsonl

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// With the read chunk large, one chunk holds many checkpoints (a run of about checkpointSpacing bytes
// between them): every old page is the same messages as the whole transcript parsed at once, the
// messages a caller collects while reading are the same, and a Codex duplicate merged at a run's edge
// numbers as it did at a chunk's edge.
func TestCheckpointsInsideOneChunkKeepEveryPageTheSame(t *testing.T) {
	for _, agentType := range []string{"claude", "codex"} {
		for _, pageChunk := range []int64{300, 700, 2000} {
			t.Run(fmt.Sprintf("%s/%d", agentType, pageChunk), func(t *testing.T) {
				defer SetTranscriptBoundsForTest(40, 1<<30, 1<<20, 8)() // the whole file is one read chunk
				defer func(old int64) { pageChunkCap = old }(pageChunkCap)
				pageChunkCap = pageChunk
				r, sid, path, want := checkpointRig(t, agentType, 300)
				ctx := context.Background()
				tv := r.ReadTranscriptContext(ctx, sid, "", agentType)
				require.Equal(t, len(want), tv.Total)
				require.Greater(t, tv.Base, 600)
				ck := r.cache[sid].ckpts
				require.Greater(t, len(ck), 10, "checkpoints inside the one chunk")
				info, err := os.Stat(path)
				require.NoError(t, err)
				for i, c := range ck {
					require.Less(t, c.offset, info.Size())
					if i > 0 {
						require.Greater(t, c.msgs, ck[i-1].msgs)
						require.GreaterOrEqual(t, c.offset-ck[i-1].offset, checkpointSpacing())
					}
				}
				for from := 0; from < tv.Base; from += 11 {
					to := min(from+9, tv.Total)
					assertSameMessages(t, want[from:to], tv.Slice(ctx, from, to), fmt.Sprintf("from %d", from))
				}
				assertSameMessages(t, want, tv.All(ctx), "everything")
				oldReadBytes.Store(0)
				tv.Slice(ctx, tv.Base/2, tv.Base/2+9)
				assert.Less(t, oldReadBytes.Load(), info.Size()/4, "a page in the middle reads a small part of the file, not the one chunk it was first read in")

				collected, total := NewSessionReader().ReadNewMessages(sid, "", agentType)
				assert.Equal(t, len(want), total)
				assertSameMessages(t, want, collected, "collected while reading")
			})
		}
	}
}
