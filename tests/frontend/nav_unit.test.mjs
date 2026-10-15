// The dashboard's history owner (#3532, nav.js), against a small fake of location and history:
// the start route (old hash links, old Chats filters on "/", an invalid path), history.state set
// even on a hash load (QA's #3529 note), push vs replace, note() without a re-show, and Back.
import assert from 'node:assert/strict';

const S = '6f9a3c1e-0000-4000-8000-000000000001';
function fakeBrowser(href) {
    const u = new URL(href, 'http://coral.test');
    const entries = [{ url: u.pathname + u.search + u.hash, state: null }];
    let i = 0;
    const listeners = [];
    const loc = {};
    const sync = () => { const v = new URL(entries[i].url, 'http://coral.test'); Object.assign(loc, { pathname: v.pathname, search: v.search, hash: v.hash }); };
    sync();
    globalThis.location = loc;
    globalThis.history = {
        get state() { return entries[i].state; },
        get length() { return entries.length; },
        pushState(state, _t, url) { entries.splice(i + 1); entries.push({ url, state }); i++; sync(); },
        replaceState(state, _t, url) { entries[i] = { url, state }; sync(); },
    };
    globalThis.window = { addEventListener: (ev, fn) => { if (ev === 'popstate') listeners.push(fn); } };
    return {
        back() { i--; sync(); listeners.forEach(fn => fn({ state: entries[i].state })); },
        forward() { i++; sync(); listeners.forEach(fn => fn({ state: entries[i].state })); },
        get length() { return entries.length; },
        url: () => entries[i].url,
    };
}

// startRoute: legacy hashes, old filters on "/", invalid paths.
fakeBrowser('/');
let nav = await import('../../coral-go/pkg/frontend/static/nav.js?' + Math.random());
assert.deepEqual(nav.startRoute({ pathname: '/', search: '', hash: `#chat/${S}` }), { view: 'agents', id: S });
assert.deepEqual(nav.startRoute({ pathname: '/', search: '', hash: '#team-settings=qa' }), { view: 'team', server: null, id: 'qa', sub: 'settings' });
assert.deepEqual(nav.startRoute({ pathname: '/', search: '?q=battle&page=2&page_size=50', hash: '' }), { view: 'chats', query: 'page=2&q=battle' });
assert.deepEqual(nav.startRoute({ pathname: '/nope', search: '', hash: '' }), { view: 'agents' });
assert.deepEqual(nav.startRoute({ pathname: '/', search: '', hash: '#chat/..%2F..%2Fapi' }), { view: 'agents' }, 'a bad legacy id goes home');

// A page opened with an old hash: the URL becomes the path and history.state is the route.
let b = fakeBrowser(`/#chat/${S}`);
nav = await import('../../coral-go/pkg/frontend/static/nav.js?' + Math.random());
const shown = [];
for (const v of ['agents', 'chats', 'team', 'workflows', 'docs']) nav.onRoute(v, (r, o) => shown.push([r.view, r.id || null, !!o.fromHistory]));
nav.startNav();
assert.equal(b.url(), `/agents/${S}`, 'the legacy hash is replaced with the path');
assert.deepEqual(history.state, { coralRoute: { view: 'agents', id: S } }, 'history.state set on a hash load');
assert.equal(b.length, 1, 'replaced, not pushed');

// Tabs and objects push; a refinement replaces; note() records without showing.
await nav.go({ view: 'chats', query: '' });
await nav.go({ view: 'chats', query: 'q=x' }, { replace: true });
nav.note({ view: 'chats', id: S });
await nav.go({ view: 'workflows' });
assert.equal(b.url(), '/workflows');
assert.equal(b.length, 4, '/agents/<s>, /chats?q=x, /chats/<s>, /workflows');
assert.deepEqual(shown.map(x => x[0]), ['agents', 'chats', 'chats', 'workflows'], 'note() did not show');

// Back and Forward show the entry's route, as from history.
shown.length = 0;
b.back();
assert.equal(b.url(), `/chats/${S}`);
b.back();
assert.equal(b.url(), '/chats?q=x');
b.forward();
assert.deepEqual(shown, [['chats', S, true], ['chats', null, true], ['chats', S, true]]);
assert.deepEqual(nav.currentRoute(), { view: 'chats', id: S });

// go() with an invalid route goes home; going to the current URL replaces (no duplicate entry).
const before = b.length;
await nav.go({ view: 'agents', id: '../../api' });
assert.equal(b.url(), '/agents');
await nav.go({ view: 'agents' });
assert.equal(b.length, before - 1 + 1, 'the same URL again is not a second entry');
console.log('nav_unit: ok');
