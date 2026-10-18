package board

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The fields a caller may name are exactly the documented list: nothing is reachable by adding a column to the map alone.
func TestTaskQueryFieldAllowlist(t *testing.T) {
	var got []string
	for k := range taskQueryColumns {
		got = append(got, k)
	}
	got = append(got, "board_label")
	sort.Strings(got)
	assert.Equal(t, TaskQueryFieldNames(), got)
	for _, f := range TaskQuerySummaryFields {
		assert.NotContains(t, TaskQueryLargeFields, f, "the default set has no long text")
	}
}
