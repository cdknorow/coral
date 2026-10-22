package appmarker

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMarkerKeepsOnlyVersionCharacters(t *testing.T) {
	assert.Equal(t, "CoralApp/1.4.157", Marker("1.4.157"))
	assert.Equal(t, "CoralApp/1.4.157-dev+abc", Marker("1.4.157-dev+abc"))
	assert.Equal(t, "CoralApp/1.4.157--x", Marker("1.4.157 \"--x\"\n"), "no space, quote or newline reaches a command line or header; what is left is one token")
	assert.Equal(t, "CoralApp/0", Marker(""))
	assert.Equal(t, "CoralApp/0", Marker(" \"\n"))
}

func TestInFindsTheMarkerInAUserAgent(t *testing.T) {
	assert.True(t, In("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.3537.71 "+Marker("1.4.157")))
	assert.False(t, In("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.3537.71"))
	assert.False(t, In("CoralAppX"), "a product token has a slash")
}
