package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #6717 item 5: the extended-length prefix is dropped, so SQLite opens a database in a long data folder (it cannot under //?/D:/ or \\?\D:\).
func TestPlainDrivePath(t *testing.T) {
	for in, want := range map[string]string{
		"//?/D:/a/very/long/data": `D:\a\very\long\data`,
		`\\?\D:\a\very\long\data`: `D:\a\very\long\data`,
		`\\?\D:/mixed\forms/here`: `D:\mixed\forms\here`,
		"//?/d:/x":                `d:\x`,
		`D:\plain\long\path`:      `D:\plain\long\path`,
		"D:/plain/forward":        "D:/plain/forward",
		"/home/user/.coral":       "/home/user/.coral",
		"//?/UNC/host/share/x":    "//?/UNC/host/share/x",
		`\\.\pipe\x`:              `\\.\pipe\x`,
		"":                        "",
	} {
		assert.Equal(t, want, PlainDrivePath(in), in)
	}
}
