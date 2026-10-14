package main

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// terminalLogDepth is how many log lines may wait for a slow terminal before
// further lines are dropped from the terminal copy (never from coral.log).
const terminalLogDepth = 4096

// logOutput is where the server's log lines go: coral.log first, then a copy
// for the terminal that never blocks the caller. Every slog and log call in the
// server writes here while holding the log package's lock, so a write that
// blocks freezes every goroutine that logs. A terminal that stops reading (Ctrl-S
// in the window, a full pty buffer, a window that is not being drained) used to
// do exactly that, with the line never reaching coral.log because the terminal
// came first (#3226).
//
// The returned terminal writer is for flushing it before the process exits.
func logOutput(file, terminal io.Writer) (io.Writer, *terminalWriter) {
	t := newTerminalWriter(terminal, terminalLogDepth)
	return &fileThenTerminal{file: file, terminal: t}, t
}

// fileThenTerminal writes each line to the file, then queues it for the
// terminal even when the file write failed (a full disk must not also hide the
// line from the terminal). It reports the file's error.
type fileThenTerminal struct {
	file     io.Writer
	terminal *terminalWriter
}

func (w *fileThenTerminal) Write(p []byte) (int, error) {
	n, err := w.file.Write(p)
	w.terminal.Write(p)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	return len(p), err
}

// terminalWriter copies log lines to a terminal from its own goroutine. Write
// queues the line and returns at once; when the queue is full (the terminal is
// not draining) the line is dropped and counted, and once the terminal reads
// again a note says how many lines it missed.
type terminalWriter struct {
	ch      chan terminalItem
	dropped atomic.Int64
}

// terminalItem is a line to write, or, with done set, a flush marker that the
// goroutine closes once every line queued before it has been written.
type terminalItem struct {
	line []byte
	done chan struct{}
}

func newTerminalWriter(w io.Writer, depth int) *terminalWriter {
	t := &terminalWriter{ch: make(chan terminalItem, depth)}
	go func() {
		for item := range t.ch {
			if n := t.dropped.Swap(0); n > 0 {
				fmt.Fprintf(w, "[log] %d line(s) were not shown here while the terminal was not reading; they are in coral.log\n", n)
			}
			if item.done != nil {
				close(item.done)
				continue
			}
			w.Write(item.line)
		}
	}()
	return t
}

func (t *terminalWriter) Write(p []byte) (int, error) {
	line := append([]byte(nil), p...) // the log package reuses its buffer
	select {
	case t.ch <- terminalItem{line: line}:
	default:
		t.dropped.Add(1)
	}
	return len(p), nil
}

// Flush waits until every line queued so far has been written to the terminal,
// or until timeout, whichever comes first. The process calls it before exiting
// so the last lines (why it stopped) reach the terminal; the timeout keeps a
// terminal that is not reading from holding up the exit.
func (t *terminalWriter) Flush(timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	done := make(chan struct{})
	select {
	case t.ch <- terminalItem{done: done}:
	case <-timer.C:
		return
	}
	select {
	case <-done:
	case <-timer.C:
	}
}
