//go:build linux

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// tracingTransport says on stderr what each download of the rehearsal did: the request, the status, how long, and how a body ended (bytes read, and the error if it did not end cleanly).
// The check's own answer for a download that failed is only the word "download" (the updater keeps no error text for it); a flake in the rehearsal (#6297: the tampered item once gave
// a download failure and then not) needs the reason to be found, and this is where it is seen.
type tracingTransport struct{ base http.RoundTripper }

func (t tracingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch %s %s: error after %s: %v\n", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond), err)
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "fetch %s %s: %s, %d bytes announced, headers after %s\n", r.Method, r.URL.Path, resp.Status, resp.ContentLength, time.Since(start).Round(time.Millisecond))
	resp.Body = &tracedBody{ReadCloser: resp.Body, path: r.URL.Path, start: start}
	return resp, nil
}

type tracedBody struct {
	io.ReadCloser
	path  string
	start time.Time
	n     int64
	done  bool
}

func (b *tracedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n += int64(n)
	if err != nil && !b.done {
		b.done = true
		if err == io.EOF {
			fmt.Fprintf(os.Stderr, "fetch %s: body ended after %d bytes, %s\n", b.path, b.n, time.Since(b.start).Round(time.Millisecond))
		} else {
			fmt.Fprintf(os.Stderr, "fetch %s: body read failed after %d bytes, %s: %v\n", b.path, b.n, time.Since(b.start).Round(time.Millisecond), err)
		}
	}
	return n, err
}
