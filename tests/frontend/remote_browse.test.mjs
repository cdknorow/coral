// Launch Agent > Browse on a hub-connected server (#3491): the first listing asks the
// remote for an empty path (its home), never "~", which the hub tunnel refuses
// (pkg/tunnel safeRef: TierNever, answered not_found). A local Browse is unchanged.
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
    // The machine answers an empty path with its home, as ListFilesystem does.
    const path = p === '' ? '/home/someone' : p;
    return new Response(JSON.stringify({ path, entries: ['code', 'notes'] }), { status: 200, headers: { 'content-type': 'application/json' } });
};

const m = await import('../../coral-go/pkg/frontend/static/browser.js');

// The pure rule.
assert.equal(m.browseStartPath('', true, ''), '', 'remote, nothing typed: its home (empty path)');
assert.equal(m.browseStartPath('~/proj', true, ''), '', 'remote, a "~" path typed: its home, never "~"');
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
assert.equal(els['launch-dir'].value, '/home/someone', 'the input takes the absolute home the remote reported');
assert.match(els['browser-list'].innerHTML, /code/, 'its folders are listed');

// Remote with "~/x" typed: still the home, not "~/x".
await open('m1', '~/proj');
assert.match(asked[0], /path=$/, asked[0]);

// Local: unchanged ("~" or the coral root go to the local server).
await open('local', '', '');
assert.match(asked[0], /^\/api\/filesystem\/list\?path=(~|%7E)$/, 'local Browse still starts at "~": ' + asked[0]);

console.log('remote_browse: all checks passed');
