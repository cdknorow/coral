// Where Saved for later is opened from (#4792), on its own: which scope each page has, its parameters for GET /api/saved, the heading and the empty line.
//   node saved_scope.test.js
const assert = require('node:assert/strict');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

(async () => {
    const m = await import(pathToFileURL(path.resolve(__dirname, '../../coral-go/pkg/frontend/static/saved_scope.js')).href);
    const live = { type: 'live', name: 'qa', display_name: 'QA Engineer', session_id: 'sess-1', server: 'local', board: { server: 'local', name: 'coral-go-team' }, job_title: 'QA Engineer' };
    // home, and every page that is not an agent's or a team's
    assert.equal(m.scopeOf(undefined), m.ALL);
    assert.equal(m.scopeOf({ dock: 'agent', team: null, session: null }), m.ALL, 'the Agents page with nothing selected is home');
    assert.equal(m.scopeOf({ dock: 'other', session: live, team: { server: 'local', name: 't' } }), m.ALL, 'another page keeps no agent or team: the one open before is not its scope');
    assert.equal(m.scopeOf({ dock: 'chats', session: null }), m.ALL);
    // an agent
    const a = m.scopeOf({ dock: 'agent', session: live });
    assert.deepEqual([a.kind, a.agent, a.team, a.author, a.label, a.server], ['agent', 'sess-1', 'coral-go-team', 'QA Engineer', 'QA Engineer', 'local']);
    assert.deepEqual(m.scopeParams(a), [['agent', 'sess-1'], ['team', 'coral-go-team'], ['author', 'QA Engineer']], 'its chat, and what it wrote on its board');
    assert.equal(m.scopeHeading(a), 'Saved: QA Engineer');
    const lone = m.scopeOf({ dock: 'agent', session: { ...live, board: null, job_title: '' } });
    assert.deepEqual(m.scopeParams(lone), [['agent', 'sess-1']], 'an agent on no board has its chat only');
    const nameless = m.scopeOf({ dock: 'agent', session: { ...live, job_title: '' } });
    assert.deepEqual(m.scopeParams(nameless), [['agent', 'sess-1']], 'without the name it writes under, the board is not asked for (the server needs both)');
    assert.equal(m.scopeOf({ dock: 'agent', session: { type: 'live', session_id: 'abc', server: 's1' } }).label, 'abc', 'the label falls back to the id');
    // a team: the one opened in the agent page, or the board page's
    const t = m.scopeOf({ dock: 'agent', team: { server: 'local', name: 'coral-go-team' }, session: live });
    assert.deepEqual([t.kind, t.team, t.label], ['team', 'coral-go-team', 'coral-go-team'], 'a team opened beats the agent underneath');
    assert.deepEqual(m.scopeParams(t), [['team', 'coral-go-team']]);
    assert.equal(m.scopeHeading(t), 'Saved: coral-go-team');
    const b = m.scopeOf({ dock: 'board', boardPage: { server: 's1', name: 'hub-team' }, session: live });
    assert.deepEqual([b.kind, b.server, b.team], ['team', 's1', 'hub-team'], 'the board page is its team');
    assert.equal(m.scopeOf({ dock: 'board', boardPage: null }), m.ALL);
    // an ended agent's chat in Chats
    const h = m.scopeOf({ dock: 'chats', session: { type: 'history', name: 'sess-old', display_name: 'Old QA', server: 'local' } });
    assert.deepEqual([h.kind, h.agent, h.label], ['agent', 'sess-old', 'Old QA']);
    assert.deepEqual(m.scopeParams(h), [['agent', 'sess-old']]);
    // identity: a scope has a key that differs between agents, teams and servers
    const keys = new Set([a.key, t.key, b.key, h.key, lone.key, m.ALL.key].map(String));
    assert.equal(keys.size, 5, 'the agent with a board and the one without are one agent: the same key');
    assert.notEqual(m.scopeOf({ dock: 'agent', session: { ...live, server: 's2' } }).key, a.key);
    // the query string
    assert.equal(m.withScope('', m.ALL), '');
    assert.equal(m.withScope('?q=x&kind=board', m.ALL), '?q=x&kind=board');
    assert.equal(m.withScope('?limit=0', t), '?limit=0&team=coral-go-team');
    assert.equal(m.withScope('', a), '?agent=sess-1&team=coral-go-team&author=QA+Engineer');
    assert.equal(m.withScope('?team=other&q=a', t), '?team=coral-go-team&q=a', 'the scope wins over a leftover team');
    // the empty line
    assert.equal(m.scopeEmpty(m.ALL, 'open'), 'Bookmark a message to save it here.');
    assert.equal(m.scopeEmpty(a, 'open'), 'Nothing saved from QA Engineer yet.');
    assert.equal(m.scopeEmpty(a, 'done'), 'Nothing marked done.');
    console.log('saved_scope: ok');
})().catch(e => { console.error(e); process.exit(1); });
