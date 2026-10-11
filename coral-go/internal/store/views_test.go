package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestViewStore_CreateAndGet(t *testing.T) {
	db := openTestDB(t)
	vs := NewViewStore(db)
	ctx := context.Background()

	view := &CustomView{
		Name:   "Token Chart",
		Prompt: "Show token usage per agent",
		HTML:   "<div>chart</div>",
		Scope:  "global",
	}
	id, err := vs.CreateView(ctx, view)
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))

	got, err := vs.GetView(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, id, got.ID)
	assert.Equal(t, "Token Chart", got.Name)
	assert.Equal(t, "Show token usage per agent", got.Prompt)
	assert.Equal(t, "<div>chart</div>", got.HTML)
	assert.Equal(t, "global", got.Scope)
	assert.NotEmpty(t, got.CreatedAt)
	assert.NotEmpty(t, got.UpdatedAt)
}

func TestViewStore_GetNotFound(t *testing.T) {
	db := openTestDB(t)
	vs := NewViewStore(db)
	ctx := context.Background()

	got, err := vs.GetView(ctx, 99999)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestViewStore_ListViews_Empty(t *testing.T) {
	db := openTestDB(t)
	vs := NewViewStore(db)
	ctx := context.Background()

	views, err := vs.ListViews(ctx)
	require.NoError(t, err)
	assert.Empty(t, views)
}

func TestViewStore_ListViews_OrderedByTabOrder(t *testing.T) {
	db := openTestDB(t)
	vs := NewViewStore(db)
	ctx := context.Background()

	// Create views in reverse tab order
	_, err := vs.CreateView(ctx, &CustomView{Name: "Third", TabOrder: 3, Scope: "global"})
	require.NoError(t, err)
	_, err = vs.CreateView(ctx, &CustomView{Name: "First", TabOrder: 1, Scope: "global"})
	require.NoError(t, err)
	_, err = vs.CreateView(ctx, &CustomView{Name: "Second", TabOrder: 2, Scope: "global"})
	require.NoError(t, err)

	views, err := vs.ListViews(ctx)
	require.NoError(t, err)
	require.Len(t, views, 3)
	assert.Equal(t, "First", views[0].Name)
	assert.Equal(t, "Second", views[1].Name)
	assert.Equal(t, "Third", views[2].Name)
}

func TestViewStore_UpdateView(t *testing.T) {
	db := openTestDB(t)
	vs := NewViewStore(db)
	ctx := context.Background()

	id, err := vs.CreateView(ctx, &CustomView{
		Name: "Original", HTML: "<div>v1</div>", Scope: "global",
	})
	require.NoError(t, err)

	err = vs.UpdateView(ctx, id, &CustomView{
		Name: "Updated", HTML: "<div>v2</div>", Prompt: "new prompt", TabOrder: 5, Scope: "session",
	})
	require.NoError(t, err)

	got, err := vs.GetView(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Updated", got.Name)
	assert.Equal(t, "<div>v2</div>", got.HTML)
	assert.Equal(t, "new prompt", got.Prompt)
	assert.Equal(t, 5, got.TabOrder)
	assert.Equal(t, "session", got.Scope)
}

func TestViewStore_DeleteView(t *testing.T) {
	db := openTestDB(t)
	vs := NewViewStore(db)
	ctx := context.Background()

	id, err := vs.CreateView(ctx, &CustomView{Name: "ToDelete", Scope: "global"})
	require.NoError(t, err)

	err = vs.DeleteView(ctx, id)
	require.NoError(t, err)

	got, err := vs.GetView(ctx, id)
	require.NoError(t, err)
	assert.Nil(t, got, "view should be deleted")
}

func TestViewStore_DeleteView_NonExistent(t *testing.T) {
	db := openTestDB(t)
	vs := NewViewStore(db)
	ctx := context.Background()

	// Deleting a non-existent view should not error
	err := vs.DeleteView(ctx, 99999)
	require.NoError(t, err)
}

func TestViewStore_CreateMultiple(t *testing.T) {
	db := openTestDB(t)
	vs := NewViewStore(db)
	ctx := context.Background()

	id1, err := vs.CreateView(ctx, &CustomView{Name: "View1", Scope: "global"})
	require.NoError(t, err)
	id2, err := vs.CreateView(ctx, &CustomView{Name: "View2", Scope: "global"})
	require.NoError(t, err)

	assert.NotEqual(t, id1, id2, "IDs should be unique")

	views, err := vs.ListViews(ctx)
	require.NoError(t, err)
	assert.Len(t, views, 2)
}

func TestViewStore_HTMLPreservesContent(t *testing.T) {
	db := openTestDB(t)
	vs := NewViewStore(db)
	ctx := context.Background()

	html := `<div id="root"><script>
		const x = '<script>alert("xss")<\/script>';
		fetch('/api/sessions/live').then(r => r.json()).then(d => {
			document.getElementById('root').innerHTML = JSON.stringify(d);
		});
	</script></div>`

	id, err := vs.CreateView(ctx, &CustomView{Name: "JSView", HTML: html, Scope: "global"})
	require.NoError(t, err)

	got, err := vs.GetView(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, html, got.HTML, "HTML should be stored verbatim")
}

func TestViewStore_DefaultScope(t *testing.T) {
	db := openTestDB(t)
	vs := NewViewStore(db)
	ctx := context.Background()

	// Scope can be empty — the handler defaults it to "global"
	id, err := vs.CreateView(ctx, &CustomView{Name: "NoScope"})
	require.NoError(t, err)

	got, err := vs.GetView(ctx, id)
	require.NoError(t, err)
	// Store saves whatever scope is passed — defaulting happens in the handler
	assert.Equal(t, "", got.Scope)
}

func TestViewStore_CreatedViaDefaultsToTheCautiousValue(t *testing.T) {
	db := openTestDB(t)
	vs := NewViewStore(db)
	ctx := context.Background()
	idA, err := vs.CreateView(ctx, &CustomView{Name: "a", Scope: "global"}) // unspecified
	require.NoError(t, err)
	idB, err := vs.CreateView(ctx, &CustomView{Name: "b", Scope: "global", CreatedVia: "ui"})
	require.NoError(t, err)
	idC, err := vs.CreateView(ctx, &CustomView{Name: "c", Scope: "global", CreatedVia: "admin"}) // not a known value
	require.NoError(t, err)
	for id, want := range map[int64]string{idA: "api", idB: "ui", idC: "api"} {
		v, err := vs.GetView(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, want, v.CreatedVia)
	}
	// an edit records the editor's provenance, both ways
	require.NoError(t, vs.UpdateView(ctx, idA, &CustomView{Name: "a", Scope: "global", CreatedVia: "ui"}))
	v, _ := vs.GetView(ctx, idA)
	assert.Equal(t, "ui", v.CreatedVia)
	require.NoError(t, vs.UpdateView(ctx, idA, &CustomView{Name: "a", Scope: "global", CreatedVia: "api"}))
	v, _ = vs.GetView(ctx, idA)
	assert.Equal(t, "api", v.CreatedVia)
	list, err := vs.ListViews(ctx)
	require.NoError(t, err)
	require.Len(t, list, 3)
}

// Views that exist before the column does have unknown provenance: they come out as "api", so the dashboard asks once.
func TestViewStore_ExistingRowsMigrateAsAPI(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `ALTER TABLE custom_views DROP COLUMN created_via`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO custom_views (name, prompt, html, tab_order, scope, created_at, updated_at) VALUES ('old', '', '<b>x</b>', 0, 'global', 't', 't')`)
	require.NoError(t, err)
	require.NoError(t, db.ensureSchema(ctx)) // what every startup runs
	vs := NewViewStore(db)
	list, err := vs.ListViews(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "api", list[0].CreatedVia)
	assert.Equal(t, "<b>x</b>", list[0].HTML)
}
