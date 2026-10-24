package board

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// #3404: a Coral Hub post records the validated Hub user; every read path lists it.
func TestPostHubMessage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.Subscribe(ctx, "p", "Dev", "Backend Dev", "s", nil, "")
	require.NoError(t, err)
	m, err := s.PostHubMessage(ctx, "p", "hub-user-1234abcd", "[Operator via Coral Hub, hub-user-1234abcd] @Dev hi", nil, true)
	require.NoError(t, err)
	require.NotNil(t, m.HubUser)
	assert.Equal(t, "hub-user-1234abcd", *m.HubUser)
	assert.Equal(t, OriginHubOperator, *m.Origin)
	assert.Equal(t, "Operator", m.SubscriberID)
	u, err := s.PostHubMessage(ctx, "p", "hub-user-1234abcd", "hi", nil, false)
	require.NoError(t, err)
	assert.Equal(t, OriginHubUser, *u.Origin)
	assert.Equal(t, "hub-user-1234abcd", u.SubscriberID)
	_, err = s.DeleteMessage(ctx, "p", u.ID)
	require.NoError(t, err)
	local, err := s.PostMessage(ctx, "p", "Operator", "@Dev local", nil)
	require.NoError(t, err)
	assert.Nil(t, local.HubUser)

	list, err := s.ListMessages(ctx, "p", 10, 0, 0)
	require.NoError(t, err)
	require.Len(t, list, 2)
	read, err := s.ReadMessages(ctx, "p", "Dev", 10)
	require.NoError(t, err)
	require.Len(t, read, 2)
	for _, msgs := range [][]Message{list, read} {
		hub := map[int64]string{}
		for _, x := range msgs {
			if x.HubUser != nil {
				hub[x.ID] = *x.HubUser
			}
		}
		assert.Equal(t, map[int64]string{m.ID: "hub-user-1234abcd"}, hub)
	}
}

// An older database gets the column once; a Coral Hub user's earlier posts (sent as
// that user, #3377) get it from their sender.
func TestHubUserBackfillOnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sqlx.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE TABLE board_messages (id INTEGER PRIMARY KEY AUTOINCREMENT, project TEXT NOT NULL, session_id TEXT NOT NULL, subscriber_id TEXT, content TEXT NOT NULL, created_at TEXT NOT NULL, origin TEXT);
		INSERT INTO board_messages (project, session_id, subscriber_id, content, created_at, origin) VALUES
		 ('p','a','hub-user-1234abcd','a','2026-01-01T00:00:00Z','hub_user'),
		 ('p','b','Coral Hub','b','2026-01-01T00:00:01Z','hub_user_unknown'),
		 ('p','c','Dev','c','2026-01-01T00:00:02Z',NULL)`)
	require.NoError(t, err)
	raw.Close()
	s, err := NewStoreWithKey(path, "")
	require.NoError(t, err)
	defer s.Close()
	var got []string
	require.NoError(t, s.db.Select(&got, `SELECT COALESCE(hub_user, '-') FROM board_messages ORDER BY id`))
	assert.Equal(t, []string{"hub-user-1234abcd", "-", "-"}, got)
}

// The record of Operator actions through Coral Hub keeps the newest ones only.
func TestHubOperatorActionsAreBounded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := 0; i < hubOperatorActionsKept+5; i++ {
		require.NoError(t, s.RecordHubOperatorAction(ctx, HubOperatorAction{HubUser: "hub-user-1234abcd", Method: "POST", Path: "/api/board/p/tasks", Project: "p", Status: 200}))
	}
	var n int
	require.NoError(t, s.db.Get(&n, `SELECT COUNT(*) FROM hub_operator_actions`))
	assert.Equal(t, hubOperatorActionsKept, n)
	recent, err := s.RecentHubOperatorActions(ctx, 3)
	require.NoError(t, err)
	require.Len(t, recent, 3)
	assert.Greater(t, recent[0].ID, recent[1].ID, "newest first")
	assert.NotEmpty(t, recent[0].At)
}

// #7061: an action records the person's stable id next to the label when the machine is a team's; an older table gets the column once, and an old row reads with none.
func TestHubOperatorActionsKeepTheStableUserID(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const uid = "hub-uid-0123456789abcdef0123456789abcdef"
	require.NoError(t, s.RecordHubOperatorAction(ctx, HubOperatorAction{HubUser: "hub-user-1234abcd", HubUserID: uid, Method: "DELETE", Path: "/api/artifacts/x", Status: 200}))
	require.NoError(t, s.RecordHubOperatorAction(ctx, HubOperatorAction{HubUser: "hub-user-1234abcd", Method: "DELETE", Path: "/api/artifacts/y", Status: 200}))
	recent, err := s.RecentHubOperatorActions(ctx, 2)
	require.NoError(t, err)
	require.Len(t, recent, 2)
	assert.Empty(t, recent[0].HubUserID, "a personal machine's action has none")
	assert.Equal(t, uid, recent[1].HubUserID)
	assert.Equal(t, "hub-user-1234abcd", recent[1].HubUser, "and the label is kept beside it")
}

func TestHubOperatorActionsTableGainsTheColumnOnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sqlx.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE TABLE hub_operator_actions (id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, hub_user TEXT NOT NULL, method TEXT NOT NULL, path TEXT NOT NULL, project TEXT NOT NULL, status INTEGER NOT NULL);
		INSERT INTO hub_operator_actions (at, hub_user, method, path, project, status) VALUES ('2026-01-01T00:00:00Z', 'hub-user-1234abcd', 'POST', '/x', 'p', 200)`)
	require.NoError(t, err)
	raw.Close()
	for i := 0; i < 2; i++ { // a second open is the "duplicate column" case
		s, err := NewStoreWithKey(path, "")
		require.NoError(t, err)
		recent, err := s.RecentHubOperatorActions(context.Background(), 5)
		require.NoError(t, err)
		require.Len(t, recent, 1)
		assert.Empty(t, recent[0].HubUserID)
		require.NoError(t, s.RecordHubOperatorAction(context.Background(), HubOperatorAction{HubUser: "hub-user-1234abcd", HubUserID: "hub-uid-0123456789abcdef0123456789abcdef", Method: "POST", Path: "/y", Project: "p", Status: 200}))
		s.Close()
		raw2 := sqlx.MustOpen("sqlite", path)
		_, err = raw2.Exec(`DELETE FROM hub_operator_actions WHERE id > 1`)
		require.NoError(t, err)
		raw2.Close()
	}
}

// #7061: the cross-team switch log keeps the person's stable id beside the label; a change made here, and one made before the column existed, have none; the id is never in what a reader gets.
func TestSwitchLogKeepsTheStableUserID(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const uid = "hub-uid-0123456789abcdef0123456789abcdef"
	c, _, err := s.SetSwitchByHub(ctx, "p", SwitchMessaging, true, "hub-user-1234abcd", uid, "POST", "/api/board/p/cross-team/enable")
	require.NoError(t, err)
	assert.Equal(t, uid, c.WhoID)
	got, err := s.LastSwitchChanges(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, "hub-user-1234abcd", got[SwitchMessaging].Who)
	assert.Equal(t, uid, got[SwitchMessaging].WhoID, "kept in the record")
	raw, err := json.Marshal(got[SwitchMessaging])
	require.NoError(t, err)
	assert.NotContains(t, string(raw), uid, "never in an answer")
	actions, err := s.RecentHubOperatorActions(ctx, 5)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, uid, actions[0].HubUserID, "and in the Operator action, in the same transaction")

	require.NoError(t, s.NoteSwitchChangeLocal(ctx, "p", SwitchFiles, false, true))
	got, _ = s.LastSwitchChanges(ctx, "p")
	assert.Empty(t, got[SwitchFiles].WhoID, "a change made here has none")
}
