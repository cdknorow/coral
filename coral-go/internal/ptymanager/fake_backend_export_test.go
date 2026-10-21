package ptymanager

import "testing"

// NewFakeBackendForTest is a PTYBackend with one session that never starts a program and whose replay is ring,
// for tests in other packages that read a PTY screen. Writes to it are recorded (Written).
func NewFakeBackendForTest(t *testing.T, name, ring string, cols, rows int) (b *PTYBackend, written func() [][]byte) {
	t.Helper()
	b, s, proc := newFakeBackend(t, name)
	s.ringMax = 1 << 16
	s.ring = []byte(ring)
	s.cols, s.rows = uint16(cols), uint16(rows)
	return b, proc.written
}
