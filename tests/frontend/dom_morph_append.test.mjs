// morphHtml appending (#3881): when the new markup is the old markup plus more top-level nodes, the old nodes are kept and only the
// tail is parsed (a message posted to the board). A change in an old node, a node removed, a foreign rewrite of the parent, or a tail
// the parser would reshape still gives exactly what innerHTML gives.
//   node dom_morph_append.test.mjs
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const { morphHtml, morphStats, resetMorph } = await importUi('dom_morph');
const item = (i, extra = '') => `<div class="m" data-i="${i}">message ${i}${extra}</div>`;
const items = (n, extra) => Array.from({ length: n }, (_, i) => item(i, extra)).join('');
const plain = html => { const d = document.createElement('div'); d.innerHTML = html; return d.innerHTML; };

const box = document.createElement('div');
document.body.appendChild(box);
let added = morphHtml(box, items(3));
assert.equal(added.length, 3, 'the first fill adds every node');
const first = [...box.children];
first.forEach((n, i) => { n.__tag = i; });

let before = { ...morphStats };
added = morphHtml(box, items(4));
assert.equal(box.innerHTML, plain(items(4)), 'the result is what innerHTML gives');
assert.deepEqual([...box.children].map(n => n.__tag ?? null), [0, 1, 2, null], 'the old nodes are the same nodes; the new one is new');
assert.equal(added.length, 1, 'only the new node is reported');
assert.equal(morphStats.appended - before.appended, 1);
assert.equal(morphStats.full - before.full, 0, 'no full rebuild');

before = { ...morphStats };
added = morphHtml(box, items(7));
assert.equal(box.innerHTML, plain(items(7)));
assert.deepEqual([...box.children].map(n => n.__tag ?? null).slice(0, 3), [0, 1, 2]);
assert.equal(added.length, 3);
assert.equal(morphStats.full - before.full, 0);

// An old node changed as well: not an append; same answer as innerHTML.
before = { ...morphStats };
morphHtml(box, item(0, '!') + items(8).slice(item(0).length));
assert.equal(box.innerHTML, plain(item(0, '!') + items(8).slice(item(0).length)));

// Fewer nodes: full.
morphHtml(box, items(2));
assert.equal(box.innerHTML, plain(items(2)));

// Someone else rewrote the parent: the next update is a full one, still right.
morphHtml(box, items(3));
box.innerHTML = '<p>foreign</p>';
morphHtml(box, items(4));
assert.equal(box.innerHTML, plain(items(4)));

// A tail the parser would reshape (a stray end tag swallowed) is not appended piecemeal.
resetMorph(box);
morphHtml(box, items(2));
const odd = items(2) + '</div>' + item(2);
morphHtml(box, odd);
assert.equal(box.innerHTML, plain(odd));
console.log('dom_morph_append: ok');
