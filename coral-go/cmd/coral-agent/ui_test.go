package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUIFileHTML(t *testing.T) {
	dir := t.TempDir()
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6FEYAAAAASUVORK5CYII=")
	require.NoError(t, err)
	imagePath := filepath.Join(dir, "picture.png")
	require.NoError(t, os.WriteFile(imagePath, png, 0600))
	embedded, err := uiFileHTML(imagePath)
	require.NoError(t, err)
	require.Contains(t, embedded, "data:image/png;base64,")
	htmlPath := filepath.Join(dir, "interactive.html")
	source := `<button onclick="coralUI.emit('choose', {option:'A'})">Choose</button>`
	require.NoError(t, os.WriteFile(htmlPath, []byte(source), 0600))
	got, err := uiFileHTML(htmlPath)
	require.NoError(t, err)
	require.Equal(t, source, got)
	require.NoError(t, os.WriteFile(imagePath, []byte("not an image"), 0600))
	_, err = uiFileHTML(imagePath)
	require.Error(t, err)
	require.NoError(t, os.WriteFile(htmlPath, []byte(strings.Repeat("x", (2<<20)+1)), 0600))
	_, err = uiFileHTML(htmlPath)
	require.Error(t, err)
	_, err = uiFileHTML("")
	require.Error(t, err)
}
