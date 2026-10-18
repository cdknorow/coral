package store

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cdknorow/coral/internal/querypage"
)

// #4806: the events search's q refuses every control character, at the start, in the middle and at the end (the live and the history route share this parser).
func TestEventQueryTextRefusesEveryControlCharacter(t *testing.T) {
	for _, v := range querypage.ControlCases() {
		_, err := ParseEventQuery("a", nil, EventQueryParams{Q: []string{v}})
		assert.Error(t, err, "events q %q", v)
	}
	q, err := ParseEventQuery("a", nil, EventQueryParams{Q: []string{" a b "}})
	assert.NoError(t, err)
	assert.Equal(t, "a b", q.Text)
}
