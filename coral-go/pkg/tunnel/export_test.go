package tunnel

// Test-only hooks, kept out of the shipped binary (#3824, Go cleanup 2: audit #3780 section B).

// inUse is the bytes reserved now (tests).
func (b *byteBudget) inUse() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}
