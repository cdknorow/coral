// The dashboard's route grammar (#3532, routes.js): parse and build round-trip, the @server
// marker vs a name written %40, legacy hashes, and hostile segments (Security's section 7 and
// coral-hub Security's condition): a crafted path never becomes a route, so no /api path is
// built from it.
import assert from 'node:assert/strict';
import { parse, build, fromLegacyHash, valid, normalize, chatsQuery } from '../../coral-go/pkg/frontend/static/routes.js';

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
rt('/chats/@m2', { view: 'chats', server: 'm2', query: '' });
rt('/chats/@m2?page=2&q=battle', { view: 'chats', server: 'm2', query: 'page=2&q=battle' });
rt(`/chats/@m2/${S}`, { view: 'chats', server: 'm2', id: S });
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
rt('/docs/README', { view: 'docs', id: 'README' });
rt('/servers/m2', { view: 'servers', id: 'm2' });
assert.deepEqual(parse('/'), { view: 'agents' }, '/ is home');

// ?view=terminal on an agent route (coral-hub, #6752): exactly one view parameter with exactly the value terminal sets terminal: true; anything else is as if there were no query. Agents routes only.
const TERM = { view: 'agents', server: null, id: S, sub: null, terminal: true }, PLAIN = { view: 'agents', server: null, id: S, sub: null };
for (const [q, want, why] of [
    ['?view=terminal', TERM, 'the link'], ['?x=1&view=terminal', TERM, 'other keys are not read'], ['?view=terminal&x=1', TERM, 'other keys are not read, after'],
    ['', PLAIN, 'no query'], ['?', PLAIN, 'an empty query'], ['?view=', PLAIN, 'an empty value'], ['?view=chat', PLAIN, 'another value'], ['?view=Terminal', PLAIN, 'case matters'], ['?view=terminal%20', PLAIN, 'a trailing space'],
    ['?view=terminal&view=terminal', PLAIN, 'a second view, the same'], ['?view=terminal&view=chat', PLAIN, 'a second view, another'], ['?view=chat&view=terminal', PLAIN, 'a second view, first other'],
    ['?view[]=terminal', PLAIN, 'junk key'], ['?VIEW=terminal', PLAIN, 'the key is lower case'], ['?terminal', PLAIN, 'junk'], ['?view', PLAIN, 'no value'],
]) assert.deepEqual(parse(`/agents/${S}`, q), want, `agents ${q || '(none)'}: ${why}`);
assert.deepEqual(parse(`/agents/@m2/${S}/files`, '?view=terminal'), { view: 'agents', server: 'm2', id: S, sub: 'files', terminal: true }, 'a server and a sub tab keep their fields');
assert.deepEqual(parse('/agents', '?view=terminal'), { view: 'agents' }, 'the agents list has no terminal');
for (const path of ['/chats', `/chats/${S}`, '/jobs', '/workflows', '/docs/board', `/team/coral-go-team`, `/board/qa`]) assert.ok(!('terminal' in (parse(path, '?view=terminal') || {})), `${path}: only an agent route reads view=terminal`);
assert.equal(build(TERM), `/agents/${S}`, 'build writes the path without the query');
assert.deepEqual(normalize(TERM), PLAIN, 'a route that went through normalize has lost the flag: it is read from the URL once');

// The Chats query keeps only its own keys and drops page=1.
assert.equal(chatsQuery('?q=x&page=1&page_size=50&debug=1'), 'q=x');

// Hostile or malformed segments are never routes.
for (const bad of [
    '/agents/..%2F..%2Fapi%2Fsettings', `/agents/${S}%2F..%2Fx`, `/agents/${S}%3Fx`, '/agents/not-a-uuid', `/agents/${S}/evil`, `/agents/${S}/changes`, `/agents/${S}/team-settings`,
    `/agents/@bad%2Fslug/${S}`, `/agents/@../${S}`, `/agents/@/${S}`,
    '/chats/@../x', '/chats/@bad%2Fslug/x', '/chats/@m2/..%2Fapi', '/chats/@m2/a%3Fb', '/chats/@m2/a/b', '/chats/..%2F..%2Fapi', '/chats/a%2Fb', '/chats/a%3Fb', '/chats/%E0%A4%A', '/chats/' + 'x'.repeat(200), '/chats/a%00b', '/chats/..',
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
// An uppercase server marker reads lower-case (QA).
assert.deepEqual(parse(`/agents/@M2/${S}`), { view: 'agents', server: 'm2', id: S, sub: null });
// A board or team named "." or ".." is never a route, from a path, a legacy hash or a view's own
// key: its name would climb out of /api/board/ in boardFetch (coral-hub Security). #3534 refuses
// such names at creation.
for (const n of ['.', '..']) {
    for (const v of ['team', 'board']) {
        assert.equal(normalize({ view: v, server: null, id: n, sub: null }), null, `${v} ${n}`);
        assert.equal(normalize({ view: v, server: 'm2', id: n, sub: null }), null, `${v} @m2/${n}`);
    }
    for (const p of [`/board/${n}`, '/board/%2E%2E', '/board/%2e', `/team/@m2/${n}`, '/team/%2E%2E/settings']) assert.equal(parse(p), null, p);
}
for (const h of ['#board/..', '#board/%2E%2E', '#team-view=..', '#team-settings=.', '#team-view=%40m2%2F..']) {
    assert.equal(normalize(fromLegacyHash(h)), null, h);
}
assert.equal(normalize(fromLegacyHash('#board/...')).id, '...', 'three dots is an ordinary name');
console.log('routes_unit: ok');

// The views' own keys (#3532 part 2).
{
    const { liveRoute, keyRoute, routeKey } = await import('../../coral-go/pkg/frontend/static/routes.js');
    const u = '0b5e2f2a-1c3d-4e5f-8a9b-0c1d2e3f4a5b';
    assert.equal(build(liveRoute('local', u)), `/agents/${u}`);
    assert.equal(build(liveRoute(null, u, 'files')), `/agents/${u}/files`);
    assert.equal(build(liveRoute('m2', u)), `/agents/@m2/${u}`);
    assert.equal(build(keyRoute('team', '@m2/qa', 'settings')), '/team/@m2/qa/settings');
    assert.equal(build(keyRoute('board', '@odd')), '/board/%40odd');
    assert.equal(build(keyRoute('team', 'a/b')), '/team/a%2Fb');
    for (const k of ['qa', '@m2/qa', '@odd', 'a/b', '@m2/@x', '@x!/y']) {
        assert.equal(routeKey(normalize(keyRoute('board', k))), k, k);
        assert.equal(routeKey(normalize(keyRoute('team', k))), k, k);
    }
    assert.equal(normalize(liveRoute('local', 'not-a-uuid')), null);
}
