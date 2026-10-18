package board

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cdknorow/coral/internal/querypage"
)

// #4806: a search's text parameters refuse every control character (the C0 bytes, DEL and C1), at the start, in the middle and at the end,
// not only tab, CR and LF, and not only where the trim leaves them. The task and message searches.
func TestSearchTextRefusesEveryControlCharacter(t *testing.T) {
	cases := querypage.ControlCases()
	for _, v := range cases {
		_, err := ParseTaskQuery(TaskQueryParams{Q: []string{v}})
		assert.Error(t, err, "tasks q %q", v)
		_, err = ParseMessageQuery(MessageQueryParams{Q: []string{v}})
		assert.Error(t, err, "messages q %q", v)
		_, err = ParseMessageQuery(MessageQueryParams{From: []string{v}})
		assert.Error(t, err, "messages from %q", v)
		_, err = ParseMessageQuery(MessageQueryParams{Mentions: []string{v}})
		assert.Error(t, err, "messages mentions %q", v)
	}
	assert.Equal(t, 3*(0x20+2), len(cases))
	for _, ok := range []string{"ab", " a b ", "résumé", "100% a_b"} {
		q, err := ParseTaskQuery(TaskQueryParams{Q: []string{ok}})
		assert.NoError(t, err, ok)
		assert.NotEmpty(t, q.Text)
		_, err = ParseMessageQuery(MessageQueryParams{Q: []string{ok}, From: []string{ok}, Mentions: []string{ok}})
		assert.NoError(t, err, ok)
	}
}
