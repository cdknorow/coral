package jsonl

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readers builds the readers nextChunk meets over the same bytes: a file, a part of a file under a
// LimitedReader, an in-memory reader and one that returns a byte a read.
func readers(t *testing.T, data []byte, limit int64) map[string]io.Reader {
	t.Helper()
	open := func() *os.File {
		path := filepath.Join(t.TempDir(), "f.jsonl")
		require.NoError(t, os.WriteFile(path, data, 0o600))
		f, err := os.Open(path)
		require.NoError(t, err)
		t.Cleanup(func() { f.Close() })
		return f
	}
	return map[string]io.Reader{
		"file":         open(),
		"limit-file":   io.LimitReader(open(), limit),
		"limit-mem":    io.LimitReader(bytes.NewReader(data), limit),
		"mem":          bytes.NewReader(data),
		"one-byte":     iotest.OneByteReader(bytes.NewReader(data)),
		"data-and-eof": iotest.DataErrReader(bytes.NewReader(data)),
	}
}

// TestReadUpToIsReadAllOfALimitedReader is the oracle: for every size around the limit and around the
// size a hint would give, readUpTo returns the bytes io.ReadAll(io.LimitReader(r, limit)) does.
func TestReadUpToIsReadAllOfALimitedReader(t *testing.T) {
	const limit = 100
	for _, size := range []int{0, 1, 99, 100, 101, 511, 512, 513, 1000} {
		data := bytes.Repeat([]byte("abcdefghij"), size/10+1)[:size]
		for name, r := range readers(t, data, 40) {
			want, wantErr := io.ReadAll(io.LimitReader(bytes.NewReader(data), limit))
			if name == "limit-file" || name == "limit-mem" {
				want, wantErr = io.ReadAll(io.LimitReader(bytes.NewReader(data), min(40, limit)))
			}
			got, err := readUpTo(r, limit)
			assert.Equal(t, wantErr, err, "%s size %d", name, size)
			assert.Equal(t, string(want), string(got), "%s size %d", name, size)
		}
	}
}

// TestReadUpToKeepsAReadErrorAndWhatCameBefore: ReadAll gives the bytes read and the error.
func TestReadUpToKeepsAReadErrorAndWhatCameBefore(t *testing.T) {
	boom := errors.New("boom")
	r := io.MultiReader(bytes.NewReader([]byte("abc")), iotest.ErrReader(boom))
	got, err := readUpTo(r, 100)
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, "abc", string(got))
}

// TestReadUpToReadsAFileThatGrewPastItsHint: the size hint is the file's at the start; bytes written
// after it are still read, up to the limit.
func TestReadUpToReadsAFileThatGrewPastItsHint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grow.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("0123456789"), 0o600))
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	r := &growOnFirstRead{File: f, path: path}
	got, err := readUpTo(r, 1000)
	require.NoError(t, err)
	assert.Equal(t, "0123456789"+"abcdefghijklmnopqrstuvwxyz", string(got))
}

type growOnFirstRead struct {
	*os.File
	path string
	done bool
}

func (g *growOnFirstRead) Read(p []byte) (int, error) {
	if !g.done {
		g.done = true
		a, err := os.OpenFile(g.path, os.O_APPEND|os.O_WRONLY, 0)
		if err == nil {
			a.WriteString("abcdefghijklmnopqrstuvwxyz")
			a.Close()
		}
	}
	return g.File.Read(p)
}

// TestNextChunkChunksAreTheSameAsBefore: a file longer than a chunk and a line longer than a chunk.
func TestNextChunkChunksAreTheSameAsBefore(t *testing.T) {
	defer SetTranscriptBoundsForTest(40, 1<<30, 64, 8)()
	long := append(bytes.Repeat([]byte("x"), 200), '\n')
	data := append(append([]byte("a\nb\n"), long...), []byte("c\n")...)
	for name, r := range readers(t, data, int64(len(data))) {
		if name == "one-byte" || name == "data-and-eof" {
			continue
		}
		var got []byte
		for {
			chunk, eof, err := nextChunk(r)
			require.NoError(t, err, name)
			got = append(got, chunk...)
			if eof {
				break
			}
		}
		assert.Equal(t, string(data), string(got), name)
	}
}

// TestReadUpToAllocatesTheChunkOnce is the point of readUpTo: a file's chunk is one buffer, not the
// chain of growing buffers io.ReadAll leaves behind (about 2.1 times the chunk).
func TestReadUpToAllocatesTheChunkOnce(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 1<<16) // 1 MiB
	path := filepath.Join(t.TempDir(), "f.jsonl")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	for name, read := range map[string]func(*os.File) io.Reader{
		"file":       func(f *os.File) io.Reader { return f },
		"limit-file": func(f *os.File) io.Reader { return io.LimitReader(f, int64(len(data))) },
	} {
		f, err := os.Open(path)
		require.NoError(t, err)
		r := read(f)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got, err := readUpTo(r, 8<<20)
		runtime.ReadMemStats(&after)
		f.Close()
		require.NoError(t, err)
		require.Equal(t, len(data), len(got))
		assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(len(data))*12/10, name)
	}
}
