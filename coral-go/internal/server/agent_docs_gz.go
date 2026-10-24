package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// The agent docs are embedded gzipped (static/agent_docs/<name>.md.gz, written by tools/sync-agent-docs.sh): about 700 KB of Markdown ship as about 260 KB (#6814). gzDocsFS shows them as the plain
// <name>.md files everything else reads (the list, one doc, the digest and the startup line), so none of that code changes and the digest is the one of the plain text. A copy synced before this change, with
// plain .md files, is read as it is.
type gzDocsFS struct{ inner fs.FS }

func newGzDocsFS(inner fs.FS) *gzDocsFS { return &gzDocsFS{inner: inner} }

const docsSeparator = "\n\n---\n\n" // between docs in /api/agent-docs/all

func (g *gzDocsFS) Open(name string) (fs.File, error) {
	if name == "." {
		return g.inner.Open(".")
	}
	data, err := g.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return &memFile{name: name, r: bytes.NewReader(data), size: int64(len(data))}, nil
}

func (g *gzDocsFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(g.inner, name)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]fs.DirEntry, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".md.gz") {
			n = strings.TrimSuffix(n, ".gz")
			e = renamedEntry{DirEntry: e, name: n}
		}
		if seen[n] {
			continue // a plain copy and a gzipped one of the same doc: one
		}
		seen[n] = true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// ReadFile is the doc's plain text: the stored gzip unzipped, or a plain copy as it is.
func (g *gzDocsFS) ReadFile(name string) ([]byte, error) {
	if strings.HasSuffix(name, ".md") {
		if raw, err := fs.ReadFile(g.inner, name+".gz"); err == nil {
			zr, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				return nil, err
			}
			defer zr.Close()
			return io.ReadAll(zr)
		}
	}
	return fs.ReadFile(g.inner, name)
}

// GzipAll is every doc as one gzip stream (README first, then the others alphabetically, a separator between): the stored members joined as they are, which is a valid gzip stream whose text is exactly what
// /api/agent-docs/all serves plain. ok is false when any doc is not stored gzipped (a plain copy): the caller serves the plain text.
func (g *gzDocsFS) GzipAll() ([]byte, bool) {
	entries, err := fs.ReadDir(g.inner, ".")
	if err != nil {
		return nil, false
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".md.gz") {
			if strings.HasSuffix(e.Name(), ".md") {
				return nil, false
			}
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		return nil, false
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := names[i] == "README.md.gz", names[j] == "README.md.gz"
		if a != b {
			return a
		}
		return names[i] < names[j]
	})
	var sep bytes.Buffer
	zw := gzip.NewWriter(&sep)
	zw.Write([]byte(docsSeparator))
	zw.Close()
	var out bytes.Buffer
	for i, n := range names {
		raw, err := fs.ReadFile(g.inner, n)
		if err != nil {
			return nil, false
		}
		if i > 0 {
			out.Write(sep.Bytes())
		}
		out.Write(raw)
	}
	return out.Bytes(), true
}

type renamedEntry struct {
	fs.DirEntry
	name string
}

func (r renamedEntry) Name() string { return r.name }
func (r renamedEntry) Info() (fs.FileInfo, error) {
	info, err := r.DirEntry.Info()
	if err != nil {
		return nil, err
	}
	return renamedInfo{FileInfo: info, name: r.name}, nil
}

type renamedInfo struct {
	fs.FileInfo
	name string
}

func (r renamedInfo) Name() string { return r.name }

// memFile is an opened doc: its plain bytes.
type memFile struct {
	name string
	r    *bytes.Reader
	size int64
}

func (m *memFile) Read(p []byte) (int, error) { return m.r.Read(p) }
func (m *memFile) Close() error               { return nil }
func (m *memFile) Stat() (fs.FileInfo, error) { return memInfo{name: m.name, size: m.size}, nil }

type memInfo struct {
	name string
	size int64
}

func (m memInfo) Name() string       { return m.name }
func (m memInfo) Size() int64        { return m.size }
func (m memInfo) Mode() fs.FileMode  { return 0o444 }
func (m memInfo) ModTime() time.Time { return time.Time{} }
func (m memInfo) IsDir() bool        { return false }
func (m memInfo) Sys() any           { return nil }
