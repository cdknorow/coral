// saved_link.js (#4596): the URL of one saved message. Valid links parse to {kind, ref, key, server}; every malformed part makes it no link at all, and a server counts only on the hub.
import assert from 'node:assert/strict';
import { parseSavedLink, savedLinkUrl } from '../../coral-go/pkg/frontend/static/saved_link.js';

const hex = '0123456789abcdef0123';
const q = o => '?' + new URLSearchParams(o).toString();

assert.deepEqual(parseSavedLink(q({ saved: hex, kind: 'agent', ref: 'sess-1' })), { kind: 'agent', ref: 'sess-1', key: hex, server: 'local' });
assert.deepEqual(parseSavedLink(q({ saved: '4821', kind: 'board', ref: 'my board' })), { kind: 'board', ref: 'my board', key: '4821', server: 'local' });
assert.deepEqual(parseSavedLink(q({ saved: hex, kind: 'agent', ref: 's', server: 'Box-B' }), true).server, 'box-b', 'a server id, lower case, on the hub');
assert.equal(parseSavedLink(q({ saved: hex, kind: 'agent', ref: 's', server: 'box-b' }), false).server, 'local', 'a local Coral has no other machine: the server is ignored');
assert.equal(parseSavedLink(q({ saved: hex, kind: 'agent', ref: 's', server: 'local' }), true).server, 'local');

// Not links: no saved key, an unknown kind, a key of the other kind's shape, a bad key, a bad ref, a bad server.
const bad = [
    '', '?x=1', q({ kind: 'agent', ref: 's' }),
    q({ saved: hex, ref: 's' }), q({ saved: hex, kind: 'file', ref: 's' }), q({ saved: hex, kind: '__proto__', ref: 's' }), q({ saved: hex, kind: 'toString', ref: 's' }),
    q({ saved: '12', kind: 'agent', ref: 's' }), q({ saved: hex, kind: 'board', ref: 's' }),
    q({ saved: hex.toUpperCase(), kind: 'agent', ref: 's' }), q({ saved: hex + '0', kind: 'agent', ref: 's' }), q({ saved: hex.slice(1), kind: 'agent', ref: 's' }),
    q({ saved: '0', kind: 'board', ref: 's' }), q({ saved: '012', kind: 'board', ref: 's' }), q({ saved: '1'.repeat(17), kind: 'board', ref: 's' }), q({ saved: '-1', kind: 'board', ref: 's' }),
    q({ saved: hex, kind: 'agent', ref: '' }), q({ saved: hex, kind: 'agent', ref: '   ' }), q({ saved: hex, kind: 'agent', ref: '..' }), q({ saved: hex, kind: 'agent', ref: '../../etc' }), q({ saved: hex, kind: 'agent', ref: 'a/b' }), q({ saved: hex, kind: 'agent', ref: 'a\\b' }), q({ saved: hex, kind: 'agent', ref: 'x..y' }), q({ saved: hex, kind: 'agent', ref: '.' }),
    q({ saved: hex, kind: 'agent', ref: 'a\nb' }), q({ saved: hex, kind: 'agent', ref: 'a\tb' }), q({ saved: hex, kind: 'agent', ref: 'a\u200bb' }), q({ saved: hex, kind: 'agent', ref: 'x'.repeat(201) }),
];
for (const s of bad) assert.equal(parseSavedLink(s, true), null, 'not a link: ' + JSON.stringify(s));
for (const server of ['../x', 'a/b', 'a b', '', 'A'.repeat(33), 'x\ny', '%2e%2e', 'a?b=c', '-'.repeat(40)]) {
    assert.equal(parseSavedLink(q({ saved: hex, kind: 'agent', ref: 's', server }), true), null, 'not a server: ' + JSON.stringify(server));
}
assert.equal(parseSavedLink(q({ saved: hex, kind: 'agent', ref: 'x'.repeat(200) }), true).ref.length, 200, '200 characters is the limit');

// The URL built from an item reads back as the same item.
for (const it of [{ kind: 'agent', ref: 'sess-1', key: hex, server: 'local' }, { kind: 'board', ref: 'a board & more?', key: '77', server: 'box-b' }, { kind: 'agent', ref: 'é#x', key: hex, server: 'local' }]) {
    const url = savedLinkUrl(it);
    assert.ok(url.startsWith('/?saved='), url);
    assert.deepEqual(parseSavedLink(url.slice(1), true), it, url);
}
assert.ok(!savedLinkUrl({ kind: 'agent', ref: 's', key: hex, server: 'local' }).includes('server='), 'this computer is the default');
console.log('saved_link: ok');
