package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #6717: the extended-length prefix is written with backslashes all through, so SQLite can open a database in a long data folder.
func TestExtendedPath(t *testing.T) {
	for in, want := range map[string]string{
		"//?/D:/a/very/long/data": `\\?\D:\a\very\long\data`,
		`\\?\D:\a\very\long\data`: `\\?\D:\a\very\long\data`,
		`\\?\D:/mixed\forms/here`: `\\?\D:\mixed\forms\here`,
		"//?/d:/x":                `\\?\d:\x`,
		`D:\plain\long\path`:      `D:\plain\long\path`,
		"/home/user/.coral":       "/home/user/.coral",
		"//?/UNC/host/share/x":    "//?/UNC/host/share/x",
		`\\.\pipe\x`:              `\\.\pipe\x`,
		"":                        "",
	} {
		assert.Equal(t, want, ExtendedPath(in), in)
	}
}
