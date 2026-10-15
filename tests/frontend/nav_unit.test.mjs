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
assert.deepEqual(nav.startRoute({ pathname: '/', search: '', hash: `#chat/${S}` }), { view: 'agents', server: null, id: S, sub: null });
assert.deepEqual(nav.startRoute({ pathname: '/', search: '', hash: '#team-settings=qa' }), { view: 'team', server: null, id: 'qa', sub: 'settings' });
assert.deepEqual(nav.startRoute({ pathname: '/', search: '?q=battle&page=2&page_size=50', hash: '' }), { view: 'chats', query: 'page=2&q=battle' });
assert.deepEqual(nav.startRoute({ pathname: '/', search: '?view=x&q=battle&type=agent&page=2&page_size=50', hash: '#history' }), { view: 'chats', query: 'page=2&type=agent&q=battle' }, 'a legacy Chats hash keeps its filters');
assert.deepEqual(nav.startRoute({ pathname: '/', search: '?q=battle', hash: '#session/abc' }), { view: 'chats', id: 'abc' }, 'a chat link has no list filters');
assert.deepEqual(nav.startRoute({ pathname: '/', search: '', hash: '#board/..' }), { view: 'agents' }, 'a dot-named board link goes home');
assert.deepEqual(nav.startRoute({ pathname: '/nope', search: '', hash: '' }), { view: 'agents' });
assert.deepEqual(nav.startRoute({ pathname: '/', search: '', hash: '#chat/..%2F..%2Fapi' }), { view: 'agents' }, 'a bad legacy id goes home');

// Before startNav() nothing is written: the pop-out (/agent/<id>) never starts it, and the views
// it shares with the dashboard (a history chat, the filters) must not rewrite its URL.
{
    const pop = fakeBrowser(`/agent/${S}`);
    const navPop = await import('../../coral-go/pkg/frontend/static/nav.js?' + Math.random());
    navPop.note({ view: 'chats', id: S });
    navPop.note({ view: 'chats', query: 'q=x' }, { replace: true });
    assert.equal(pop.url(), `/agent/${S}`, 'the pop-out URL is untouched');
    assert.equal(pop.length, 1);
    assert.equal(navPop.currentRoute(), null);
}

// A page opened with an old hash: the URL becomes the path and history.state is the route.
let b = fakeBrowser(`/#chat/${S}`);
nav = await import('../../coral-go/pkg/frontend/static/nav.js?' + Math.random());
const shown = [];
for (const v of ['agents', 'chats', 'team', 'workflows', 'docs']) nav.onRoute(v, (r, o) => shown.push([r.view, r.id || null, !!o.fromHistory]));
nav.startNav();
assert.equal(b.url(), `/agents/${S}`, 'the legacy hash is replaced with the path');
assert.deepEqual(history.state, { coralRoute: { view: 'agents', server: null, id: S, sub: null }, prev: null }, 'history.state set on a hash load');
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
// Handlers only ever see the parsed route (Security's review of 1b part 1): a stray field is
// dropped through go(), note() and startRoute(), and a tampered or old-shape history.state is
// checked like a typed URL, falling back to the location's own route.
shown.length = 0;
const got = [];
nav.onRoute('team', r => got.push(r));
await nav.go({ view: 'team', id: 'qa', sub: '<img onerror=1>', evil: 'x' });
assert.deepEqual(got.pop(), { view: 'team', server: null, id: 'qa', sub: null }, 'go(): stray fields dropped');
nav.note({ view: 'team', id: 'qa', sub: 'settings', html: '<b>' });
assert.deepEqual(nav.currentRoute(), { view: 'team', server: null, id: 'qa', sub: 'settings' }, 'note(): stray fields dropped');
assert.deepEqual(nav.startRoute({ pathname: '/team/qa', search: '', hash: '' }), { view: 'team', server: null, id: 'qa', sub: null });
// A history entry whose stored route was tampered with, or has an old shape.
// (A name like "../../api" is a legal board name, kept as one encoded segment; a bidi
// override is not, so this state is refused.)
history.replaceState({ coralRoute: { view: 'team', id: '\u202eevil', sub: '<script>' } }, '', '/chats');
await nav.go({ view: 'workflows' });
got.length = 0; shown.length = 0;
b.back();
assert.equal(got.length, 0, 'the tampered team route never reached a handler');
assert.deepEqual(nav.currentRoute(), { view: 'chats', query: '' }, "it fell back to the location's own route");
history.replaceState({ coralRoute: { view: 'agents', sessionId: S } }, '', `/agents/${S}`); // an old shape
await nav.go({ view: 'workflows' });
b.back();
assert.deepEqual(nav.currentRoute(), { view: 'agents', server: null, id: S, sub: null }, 'an old-shape state is read from the location');
// backIs: the entry Back returns to (the phone's back button goes back instead of pushing a copy).
await nav.go({ view: 'agents' });
nav.note({ view: 'agents', id: S });
assert.equal(nav.backIs({ view: 'agents' }), true, 'pushed from /agents');
nav.note({ view: 'agents', id: S, sub: 'notes' }, { replace: true });
assert.equal(nav.backIs({ view: 'agents' }), true, 'a replace keeps where the entry came from');
assert.equal(nav.backIs({ view: 'chats' }), false);
assert.equal(nav.backIs({ view: 'team', id: '..' }), false, 'an invalid route is never Back');

console.log('nav_unit: ok');
