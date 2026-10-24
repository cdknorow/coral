package stage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The release runbook states how many junk releases hide a promotion (Security's #337): the numbers are this package's, so a change here changes the runbook too.
func TestTheRunbookStatesThePassBounds(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "tools", "devops", "README.md"))
	require.NoError(t, err)
	doc := string(b)
	i := strings.Index(doc, "### Repository settings that protect promotions")
	require.GreaterOrEqual(t, i, 0, "the runbook's repository settings section")
	section := doc[i:]
	require.True(t, strings.HasSuffix(ReleasesList, "?per_page=30"), "the runbook says 30: change it with the page size")
	assert.Contains(t, section, "lists the newest 30 releases")
	assert.Contains(t, section, fmt.Sprintf("looks at no more than %d `promote-*` candidates", maxPromoteCandidates))
	assert.Contains(t, section, fmt.Sprintf("So %d junk `promote-vX.Y.Z-N` releases", maxPromoteCandidates))
	assert.Contains(t, section, "or 30 releases of any name")
}
