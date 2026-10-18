package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4875 follow-up: a tag stored before its colour was checked ("red;position:fixed;inset:0" is a valid style value) is given the default colour
// when the database opens, so that no client of the API gets CSS as a colour. Only the colour value changes: every tag keeps its id and name,
// the tags on sessions and folders stay, a valid colour in any case is untouched, and a second run changes nothing.
func TestOpenResetsTagColoursThatAreNotHex(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tags.db")
	db, err := OpenWithKey(ctx, path, "")
	require.NoError(t, err)
	good := []string{"#fff", "#FFF", "#58a6ff", "#58A6FF", "#12345678", "#abcdefAB", "#0aF"}
	bad := []string{"red", "", "#ff", "#ffff", "#fffff", "#fffffff", "#fffffffff", "#ggg", "#58a6ff;position:fixed", "red;position:fixed;inset:0;z-index:99999",
		"url(http://x/y)", "#58a6ff)", " #fff", "#fff ", "#fff\n", "rgb(1,2,3)", "#12345g", "58a6ff", "##fff", "#\ufb000"}
	ids := map[string]int64{} // name -> id, colour -> was it good
	colour := map[string]string{}
	insert := func(name, c string) {
		res, err := db.ExecContext(ctx, `INSERT INTO tags (name, color) VALUES (?, ?)`, name, c) // the way an old row got there: no check
		require.NoError(t, err)
		id, _ := res.LastInsertId()
		ids[name], colour[name] = id, c
	}
	for i, c := range good {
		insert("good-"+string(rune('a'+i)), c)
	}
	for i, c := range bad {
		insert("bad-"+string(rune('a'+i)), c)
	}
	// the tags are in use: on a session and a folder
	_, err = db.ExecContext(ctx, `INSERT INTO session_tags (session_id, tag_id) VALUES ('s1', ?)`, ids["bad-a"])
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO folder_tags (folder_name, tag_id) VALUES ('f1', ?)`, ids["bad-c"])
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db, err = OpenWithKey(ctx, path, "") // the migration runs on open
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	var rows []struct {
		ID    int64  `db:"id"`
		Name  string `db:"name"`
		Color string `db:"color"`
	}
	require.NoError(t, db.SelectContext(ctx, &rows, `SELECT id, name, color FROM tags`))
	require.Len(t, rows, len(good)+len(bad), "no tag was deleted")
	for _, r := range rows {
		assert.Equal(t, ids[r.Name], r.ID, "%s keeps its id", r.Name)
		if r.Name[:4] == "good" {
			assert.Equal(t, colour[r.Name], r.Color, "%s: a hex colour is untouched, in any case", r.Name)
		} else {
			assert.Equal(t, DefaultTagColor, r.Color, "%s (%q) is the default colour now", r.Name, colour[r.Name])
		}
		assert.True(t, ValidTagColor(r.Color), "%s: %q", r.Name, r.Color)
	}
	var n int
	require.NoError(t, db.GetContext(ctx, &n, `SELECT COUNT(*) FROM session_tags WHERE tag_id = ?`, ids["bad-a"]))
	assert.Equal(t, 1, n, "the tag on a session stays")
	require.NoError(t, db.GetContext(ctx, &n, `SELECT COUNT(*) FROM folder_tags WHERE tag_id = ?`, ids["bad-c"]))
	assert.Equal(t, 1, n, "the tag on a folder stays")

	changed, err := db.ResetInvalidTagColors(ctx)
	require.NoError(t, err)
	assert.Zero(t, changed, "a second run changes nothing")
	// and the count it reports is the number it changed: put two bad colours back and run it
	_, err = db.ExecContext(ctx, `UPDATE tags SET color = 'blue;display:none' WHERE name IN ('good-a', 'good-b')`)
	require.NoError(t, err)
	changed, err = db.ResetInvalidTagColors(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 2, changed)
}
