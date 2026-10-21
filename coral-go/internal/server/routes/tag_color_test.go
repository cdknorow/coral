package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #4875: a tag's colour goes into a style attribute of the dashboard, so the server keeps only #rgb, #rrggbb and #rrggbbaa: anything else
// ("red;position:fixed;inset:0", a url(), a name) is a 400 naming the rule, and nothing is stored.
func TestCreateTagRefusesAColourThatIsNotAHexColour(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "tags.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{LogDir: t.TempDir()}
	cfg.UseCoralDir(t.TempDir())
	h := NewSystemHandler(db, cfg)
	r := chi.NewRouter()
	r.Post("/api/tags", h.CreateTag)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	post := func(name, color string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/tags", strings.NewReader(`{"name":"`+name+`","color":"`+color+`"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	for i, ok := range []string{"#fff", "#FFF", "#58a6ff", "#58A6FF", "#58a6ff80", "#abcdefAB", ""} { // "" is the default colour
		assert.Equal(t, http.StatusOK, post("good"+string(rune('a'+i)), ok), "colour %q", ok)
	}
	for i, bad := range []string{"red", "#ff", "#ffff", "#fffff", "#fffffff", "#ggg", "#58a6ff;position:fixed", "red;position:fixed;inset:0;z-index:99999", "url(http://x/y)", "#58a6ff )", "rgb(1,2,3)", " #fff", "#fff ", "#fff\\n"} {
		assert.Equal(t, http.StatusBadRequest, post("bad"+string(rune('a'+i)), bad), "colour %q", bad)
	}
	tags, err := store.NewSessionStore(db).ListTags(context.Background())
	require.NoError(t, err)
	for _, tag := range tags {
		assert.True(t, store.ValidTagColor(tag.Color), "stored %q", tag.Color)
		assert.True(t, strings.HasPrefix(tag.Name, "good"), "no bad tag was stored: %s", tag.Name)
	}
	assert.Len(t, tags, 7)

	_, err = store.NewSessionStore(db).CreateTag(context.Background(), "direct", "red;position:fixed")
	assert.ErrorIs(t, err, store.ErrTagColor, "the store refuses it too (the scheduler creates tags there)")
}
