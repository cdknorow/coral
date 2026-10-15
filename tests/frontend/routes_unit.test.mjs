// The dashboard's route grammar (#3532, routes.js): parse and build round-trip, the @server
// marker vs a name written %40, legacy hashes, and hostile segments (Security's section 7 and
// coral-hub Security's condition): a crafted path never becomes a route, so no /api path is
// built from it.
import assert from 'node:assert/strict';
import { parse, build, fromLegacyHash, valid, chatsQuery } from '../../coral-go/pkg/frontend/static/routes.js';

const S = '6f9a3c1e-0000-4000-8000-000000000001';
const rt = (path, route) => {
    const u = new URL(path, 'http://x');
    assert.deepEqual(parse(u.pathname, u.search), route, `parse ${path}`);
    assert.equal(build(route), path, `build ${JSON.stringify(route)}`);
};

// Round trips, one per family.
rt('/agents', { view: 'agents' });
rt(`/agents/${S}`, { view: 'agents', server: null, id: S, sub: null });
rt(`/agents/@m2/${S}/files`, { view: 'agents', server: 'm2', id: S, sub: 'files' });
rt('/chats', { view: 'chats', query: '' });
rt('/chats?page=2&q=battle', { view: 'chats', query: 'page=2&q=battle' }); // the query in the Chats tab's canonical key order
rt(`/chats/${S}`, { view: 'chats', id: S });
rt('/team/coral-go-team', { view: 'team', server: null, id: 'coral-go-team', sub: null });
rt('/team/@m2/settings', { view: 'team', server: 'm2', id: 'settings', sub: null });
rt('/team/m2/settings', { view: 'team', server: null, id: 'm2', sub: 'settings' });
rt('/team/%40m2/settings', { view: 'team', server: null, id: '@m2', sub: 'settings' });
rt('/team/a%2Fb', { view: 'team', server: null, id: 'a/b', sub: null });
rt('/board/@m2/qa', { view: 'board', server: 'm2', id: 'qa' });
rt('/workflows', { view: 'workflows' });
rt('/workflows/7', { view: 'workflows', id: 7 });
rt('/workflows/7/runs/3', { view: 'workflows', id: 7, run: 3 });
rt('/jobs', { view: 'jobs' });
rt('/analytics', { view: 'analytics' });
rt('/docs/board', { view: 'docs', id: 'board' });
rt('/servers/m2', { view: 'servers', id: 'm2' });
assert.deepEqual(parse('/'), { view: 'agents' }, '/ is home');

// The Chats query keeps only its own keys and drops page=1.
assert.equal(chatsQuery('?q=x&page=1&page_size=50&debug=1'), 'q=x');

// Hostile or malformed segments are never routes.
for (const bad of [
    '/agents/..%2F..%2Fapi%2Fsettings', `/agents/${S}%2F..%2Fx`, `/agents/${S}%3Fx`, '/agents/not-a-uuid', `/agents/${S}/evil`,
    `/agents/@bad%2Fslug/${S}`, `/agents/@../${S}`, `/agents/@/${S}`,
    '/chats/..%2F..%2Fapi', '/chats/a%2Fb', '/chats/a%3Fb', '/chats/%E0%A4%A', '/chats/' + 'x'.repeat(200), '/chats/a%00b', '/chats/..',
    '/team/%3Cscript%3E%0d%0a', '/team/' + encodeURIComponent('x'.repeat(201)), '/team/a%00b', '/team/%E0%A4%A', '/team/a/b/c', '/team/x/admin',
    '/workflows/1%2F..', '/workflows/0', '/workflows/-1', '/workflows/7/runs/x', '/workflows/abc',
    '/docs/..%2Fsecrets', '/docs/%3Cscript%3E', '/docs/a/b',
    '/servers/..%2F', '/servers/Ab', '/servers/a%2Fb',
    '/jobs/1', '/analytics/x', '/history', '/chat/x', '/random',
]) {
    // Raw, without URL normalization (a browser resolves "/chats/.." to "/" itself; the parser
    // must refuse it anyway).
    const [path, query = ''] = bad.split('?');
    assert.equal(parse(path, query), null, `not a route: ${bad}`);
}
// A team name with markup is a route, but only as text: the router passes it to textContent and
// encodeURIComponent, never innerHTML (checked in the browser suite).
assert.deepEqual(parse('/team/%3Cb%3Ex%3C%2Fb%3E'), { view: 'team', server: null, id: '<b>x</b>', sub: null });

// Legacy hashes map to routes; bad ids in them are dropped by valid().
assert.deepEqual(fromLegacyHash(`#chat/${S}`), { view: 'agents', id: S });
assert.deepEqual(fromLegacyHash(`#chat/m2/${S}`), { view: 'agents', server: 'm2', id: S });
assert.deepEqual(fromLegacyHash(`#session/${S}`), { view: 'chats', id: S });
assert.deepEqual(fromLegacyHash(`#history/${S}`), { view: 'chats', id: S });
assert.deepEqual(fromLegacyHash('#team-view=%40m2%2Fcoral-go-team'), { view: 'team', server: 'm2', id: 'coral-go-team', sub: null });
assert.deepEqual(fromLegacyHash('#team-settings=qa'), { view: 'team', server: null, id: 'qa', sub: 'settings' });
assert.deepEqual(fromLegacyHash('#servers/m2'), { view: 'servers', id: 'm2' });
assert.deepEqual(fromLegacyHash('#tokens'), { view: 'analytics' });
assert.deepEqual(fromLegacyHash('#agents'), { view: 'agents' });
assert.deepEqual(fromLegacyHash('#workflows'), { view: 'workflows' }, "the audit: /#workflows opens Workflows");
assert.deepEqual(fromLegacyHash('#docs'), { view: 'docs' });
assert.deepEqual(fromLegacyHash('#history'), { view: 'chats', query: '' });
assert.equal(fromLegacyHash('#nonsense'), null);
assert.equal(valid(fromLegacyHash('#chat/..%2F..%2Fapi')), false, 'a bad legacy id is dropped');
assert.equal(valid(fromLegacyHash(`#chat/${S}`)), true);
console.log('routes_unit: ok');
