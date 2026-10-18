// Launch Agent > Browse on a hub-connected server (#3491): the first listing asks the
// remote for an empty path (its home); it answers "~", and a click asks for "~/folder", which
// the hub tunnel takes on this route only (#4527; before, safeRef refused it as TierNever and the
// page said "Failed to load"). Any other "~" form starts at the home. A local Browse is unchanged.
import assert from 'node:assert/strict';

globalThis.location = { protocol: 'https:', host: 'hub.example', origin: 'https://hub.example' };
const els = {};
const el = (id, extra = {}) => (els[id] = Object.assign({ id, value: '', dataset: {}, style: { display: 'none' }, innerHTML: '', textContent: '', dispatchEvent() {}, querySelector: () => null }, extra));
globalThis.document = { getElementById: id => els[id] || null, addEventListener() {} };
globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
const asked = [];
globalThis.fetch = async (url) => {
    const u = new URL(String(url), 'https://hub.example');
    asked.push(u.pathname + u.search);
    const p = u.searchParams.get('path');
    // The machine answers an empty path with its home, which Coral Hub shows as "~" (hubDisplayPath), and any other path as asked.
    const path = p === '' ? '~' : p;
    return new Response(JSON.stringify({ path, entries: ['code', 'notes'] }), { status: 200, headers: { 'content-type': 'application/json' } });
};

const m = await import('../../coral-go/pkg/frontend/static/browser.js');

// The pure rule.
assert.equal(m.browseStartPath('', true, ''), '', 'remote, nothing typed: its home (empty path)');
assert.equal(m.browseStartPath('~/proj', true, ''), '~/proj', 'remote, a "~/folder" path (what a Browse put in the input): that folder (#4527)');
assert.equal(m.browseStartPath('~', true, ''), '~', 'remote, "~": the home');
assert.equal(m.browseStartPath('~user/x', true, ''), '', 'remote, a "~user" path: its home, the tunnel refuses it');
assert.equal(m.browseStartPath('~other', true, ''), '', 'remote, "~other": its home');
assert.equal(m.browseStartPath('  /srv/app ', true, ''), '/srv/app', 'remote, an absolute path typed: that path');
assert.equal(m.browseStartPath('', false, '/Users/me/coral'), '/Users/me/coral', 'local: the coral root');
assert.equal(m.browseStartPath('~/proj', false, '~'), '~/proj', 'local: "~" paths are fine on the local server');

const open = async (server, typed, coralRoot = '') => {
    for (const id of ['dir-browser', 'browser-list', 'browser-current-path']) el(id);
    el('launch-dir', { value: typed, dataset: { coralRoot } });
    m.setDirServerResolver(() => server);
    asked.length = 0;
    m.toggleBrowser('launch-dir', 'dir-browser', 'browser-list', 'browser-current-path');
    for (let i = 0; i < 20 && !els['browser-list'].innerHTML.includes('code'); i++) await new Promise(r => setTimeout(r, 5));
};

// Remote, empty input: the request goes to the remote with path= (empty), and the home is listed.
await open('m1', '');
assert.equal(asked.length, 1, JSON.stringify(asked));
assert.match(asked[0], /^\/api\/remote\/m1\/api\/filesystem\/list\?path=$/, 'an empty path through the hub: ' + asked[0]);
assert.ok(!asked[0].includes('%7E') && !asked[0].includes('~'), 'never "~" for a remote');
assert.equal(els['launch-dir'].value, '~', 'the input takes the home as the remote shows it: "~", never an absolute path');
assert.match(els['browser-list'].innerHTML, /code/, 'its folders are listed');

// Remote with "~/x" typed (what an earlier Browse left in the input): that folder, which the tunnel takes (#4527).
await open('m1', '~/proj');
assert.match(asked[0], /path=(~|%7E)(\/|%2F)proj$/, asked[0]);
// A "~user" form is not taken by the tunnel: the home instead.
await open('m1', '~root/x');
assert.match(asked[0], /path=$/, asked[0]);

// A click on a folder asks for "~/<folder>", and again deeper, and "up" asks for the parent, down to "~" (#4527).
await open('m1', '');
assert.equal(els['launch-dir'].value, '~');
m.browserNavigateTo('code');
for (let i = 0; i < 20 && els['launch-dir'].value !== '~/code'; i++) await new Promise(r => setTimeout(r, 5));
assert.equal(els['launch-dir'].value, '~/code', 'the folder click asks for ~/code and the listing is shown under it');
assert.match(asked[asked.length - 1], /path=(~|%7E)(\/|%2F)code$/, asked[asked.length - 1]);
m.browserNavigateTo('notes');
for (let i = 0; i < 20 && els['launch-dir'].value !== '~/code/notes'; i++) await new Promise(r => setTimeout(r, 5));
assert.equal(els['launch-dir'].value, '~/code/notes');
m.browserNavigateUp();
for (let i = 0; i < 20 && els['launch-dir'].value !== '~/code'; i++) await new Promise(r => setTimeout(r, 5));
assert.equal(els['launch-dir'].value, '~/code', 'up goes back one level');
m.browserNavigateUp();
for (let i = 0; i < 20 && els['launch-dir'].value !== '~'; i++) await new Promise(r => setTimeout(r, 5));
assert.equal(els['launch-dir'].value, '~', 'and up again to the home');

// Local: unchanged ("~" or the coral root go to the local server).
await open('local', '', '');
assert.match(asked[0], /^\/api\/filesystem\/list\?path=(~|%7E)$/, 'local Browse still starts at "~": ' + asked[0]);

console.log('remote_browse: all checks passed');
