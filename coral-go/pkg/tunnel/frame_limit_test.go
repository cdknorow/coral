package tunnel

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readFrameGrowing is the inbound body cap: a declared length over the cap is refused before any byte of it is read,
// a frame at the cap is read whole, and a stream that ends early is an error, not a short body.
func TestReadFrameGrowingEnforcesTheCapBeforeReadingTheBody(t *testing.T) {
	frame := func(declared int, body []byte) *bytes.Reader {
		return bytes.NewReader(append([]byte{byte(declared >> 24), byte(declared >> 16), byte(declared >> 8), byte(declared)}, body...))
	}

	over := frame(11, bytes.Repeat([]byte("x"), 11))
	_, err := readFrameGrowing(over, 10)
	assert.ErrorIs(t, err, ErrHeaderTooLarge)
	assert.Equal(t, 11, over.Len(), "an over-cap frame's body is not read")

	// A declared length of 4 GiB - 1 is refused without allocating it.
	_, err = readFrameGrowing(frame(-1, nil), 1<<20)
	assert.ErrorIs(t, err, ErrHeaderTooLarge)

	got, err := readFrameGrowing(frame(10, bytes.Repeat([]byte("y"), 10)), 10)
	require.NoError(t, err)
	assert.Equal(t, bytes.Repeat([]byte("y"), 10), got, "a frame exactly at the cap is read whole")

	_, err = readFrameGrowing(frame(10, []byte("short")), 10)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "a body shorter than declared is an error")
}
