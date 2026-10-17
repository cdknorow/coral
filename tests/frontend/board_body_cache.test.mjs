// The board's per-message body cache (#4491), with the vendored marked and DOMPurify: a body renders the same cached and
// uncached, including hostile ones; changed text is sanitized again; the cache stays within its byte bound with 5,000 large
// messages and keeps the newest; and a poll that redraws 1,000 messages sanitizes only the new and changed ones (the #3866 LRU
// sanitized every message once a board held more than 400).
//   node board_body_cache.test.mjs
import assert from 'node:assert';
import fs from 'node:fs';
import path from 'node:path';
import { installDom, importUi, STATIC } from './helpers/jsdom_env.mjs';

const dom = installDom({ mode: 'local' });
const w = dom.window;
for (const f of ['vendor/marked.min.js', 'vendor/purify.min.js']) w.eval(fs.readFileSync(path.join(STATIC, f), 'utf8'));
globalThis.marked = w.marked;
globalThis.DOMPurify = w.DOMPurify;
let sanitizes = 0;
const realSanitize = w.DOMPurify.sanitize.bind(w.DOMPurify);
w.DOMPurify.sanitize = (...a) => { sanitizes++; return realSanitize(...a); };

const { renderBoardMessageBody, renderMarkdown, _boardBodyCacheStatsForTests: stats } = await importUi('utils');
const BOARD = { gfm: true, breaks: true };
const nextRender = () => new Promise(r => setTimeout(r, 0)); // a later task: a new render pass

// 1. Cached and uncached render the same, hostile text included, and the output is sanitized.
const bodies = [
    'plain **bold** and `code`\nsecond line',
    '- one\n- two\n\n| a | b |\n|---|---|\n| 1 | 2 |',
    '<img src=x onerror=PWNED()><script>PWNED()</script><a href="javascript:PWNED()" onclick="PWNED()">l</a>',
    '[x](javascript:PWNED()) <svg onload=PWNED()></svg> <iframe src=//evil></iframe> <form><input name=p></form>',
    '<b onmouseover=PWNED()>b</b> <a href="https://example.com/a" target=_blank>ok</a> <details open ontoggle=PWNED()>d</details>',
    'line one\\nline two\\nline three', // literal \n escapes (boardMessageText)
];
bodies.forEach((content, i) => {
    const m = { id: 100 + i, content };
    const text = content.includes('\\n') ? content.replace(/\\n/g, '\n') : content;
    const uncached = renderMarkdown(text, BOARD, 'board');
    const first = renderBoardMessageBody(m), second = renderBoardMessageBody({ ...m });
    assert.equal(first, uncached, `body ${i}: the first render is the uncached render`);
    assert.equal(second, uncached, `body ${i}: the cached render is the uncached render`);
    const box = w.document.createElement('div'); box.innerHTML = second;
    for (const el of box.querySelectorAll('*')) {
        for (const a of el.attributes) assert.ok(!/^on/i.test(a.name), `body ${i}: kept ${a.name} on <${el.tagName}>`);
    }
    for (const t of ['script', 'iframe', 'form', 'input', 'svg']) assert.equal(box.querySelectorAll(t).length, 0, `body ${i}: <${t}> survived`);
    assert.ok(!/javascript:/i.test(second), `body ${i}: a javascript: URL survived`);
});

// 2. Changed text (an edit, or the Hub prefix of an Operator post) is sanitized again; the same text is not.
await nextRender();
sanitizes = 0;
renderBoardMessageBody({ id: 7, content: 'first version' });
renderBoardMessageBody({ id: 7, content: 'first version' });
assert.equal(sanitizes, 1, 'the same id and text is sanitized once');
const edited = renderBoardMessageBody({ id: 7, content: 'second <img src=x onerror=PWNED()> version' });
assert.equal(sanitizes, 2, 'changed text is sanitized again');
assert.ok(edited.includes('second') && !/onerror/.test(edited));
assert.equal(renderBoardMessageBody({ id: 7, content: 'first version' }), renderMarkdown('first version', BOARD, 'board'),
    'going back to the first text gives its own render, not the edited one');
// The same id on another board (another server's numbering) with other text is its own entry, not the first board's.
assert.notEqual(renderBoardMessageBody({ id: 7, content: 'other board' }), renderBoardMessageBody({ id: 7, content: 'first version' }));
// Two texts with the same 32-bit hash under one id: the stored text decides, so neither is shown for the other.
assert.equal(renderBoardMessageBody({ id: 9, content: 'collide 471608' }), renderMarkdown('collide 471608', BOARD, 'board'));
assert.equal(renderBoardMessageBody({ id: 9, content: 'collide 1418646' }), renderMarkdown('collide 1418646', BOARD, 'board'),
    'a hash collision renders its own text');
// No id, or no text: rendered, never cached under a made-up key.
assert.equal(renderBoardMessageBody({ content: 'no id' }), renderMarkdown('no id', BOARD, 'board'));
assert.equal(renderBoardMessageBody({ id: 8, content: '' }), '');

// 3. A poll redrawing 1,000 messages sanitizes only the new and changed ones.
await nextRender();
const board = Array.from({ length: 1000 }, (_, i) => ({ id: 10000 + i, content: `message ${i} from **agent ${i % 20}**\nwith a second line` }));
board.forEach(renderBoardMessageBody);
await nextRender();
sanitizes = 0;
board.forEach(renderBoardMessageBody);
assert.equal(sanitizes, 0, 'redrawing the same 1,000 messages sanitizes none');
await nextRender();
const next = board.slice(25).concat(Array.from({ length: 20 }, (_, i) => ({ id: 11000 + i, content: `new message ${i}` })));
next[10] = { ...next[10], content: next[10].content + ' (edited)' };
next[500] = { ...next[500], content: '[redacted]' };
sanitizes = 0;
next.forEach(renderBoardMessageBody);
assert.equal(sanitizes, 22, 'a poll with 20 new and 2 changed messages sanitizes 22');

// 4. Within the byte bound with 5,000 large messages, keeping the newest, and stable over later in-order renders.
await nextRender();
const big = Array.from({ length: 5000 }, (_, i) => ({ id: 20000 + i, content: `big ${i} ` + 'lorem ipsum dolor sit amet '.repeat(60) }));
big.forEach(renderBoardMessageBody);
let s = stats();
assert.ok(s.bytes <= s.maxBytes, `the cache holds ${s.bytes} bytes, over its ${s.maxBytes} bound`);
assert.ok(s.entries > 100 && s.entries < 5000, `a large board fills the cache but does not fit (${s.entries} entries)`);
const kept = s.ids.filter(id => id >= 20000);
assert.equal(Math.max(...kept), 24999, 'the newest message is kept');
assert.equal(Math.min(...kept), 25000 - kept.length, 'the kept ones are the newest, contiguous');
for (let round = 0; round < 2; round++) {
    await nextRender();
    sanitizes = 0;
    big.forEach(renderBoardMessageBody);
    s = stats();
    assert.ok(s.bytes <= s.maxBytes, `round ${round}: over the bound`);
    assert.equal(sanitizes, 5000 - kept.length, `round ${round}: an in-order redraw only sanitizes what never fitted (the newest stay)`);
}
// Accounting matches the entries.
assert.ok(s.bytes > 0 && s.entries === s.ids.length);

console.log('board_body_cache: ok');
