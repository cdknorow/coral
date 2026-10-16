// The rendered-markdown cache (#3866): the same (link policy, options, text) is rendered once; a different one is rendered on its
// own; a fenced block with a language is not kept while highlight.js is missing; the cache is bounded and empty text stays empty.
//   node markdown_cache.test.mjs
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
let parses = 0, sanitizes = 0;
const realParse = globalThis.marked.parse;
globalThis.marked.parse = (...a) => { parses++; return `<p>${realParse(...a)}</p>`; };
globalThis.DOMPurify = {
    sanitize(html) { sanitizes++; const t = document.createElement('template'); t.innerHTML = html; return t.content; },
    addHook() {}, removeHook() {}, removeAllHooks() {}, setConfig() {}, isSupported: true,
};
const { renderMarkdown } = await importUi('utils');

const a = renderMarkdown('hello there', undefined, 'chat');
const b = renderMarkdown('hello there', undefined, 'chat');
assert.equal(a, b);
assert.equal(parses, 1, 'the same text, options and link policy is parsed once');
renderMarkdown('hello there', undefined, 'board');
assert.equal(parses, 2, 'a different link policy is its own entry');
renderMarkdown('hello there', { breaks: true }, 'chat');
assert.equal(parses, 3, 'different options are their own entry');
renderMarkdown('hello there', { breaks: true }, 'chat');
assert.equal(parses, 3, 'and then cached too');
assert.equal(renderMarkdown('', undefined, 'chat'), '');

const fenced = '```js\nlet x = 1;\n```';
renderMarkdown(fenced, undefined, 'chat'); renderMarkdown(fenced, undefined, 'chat');
assert.equal(parses, 5, 'a fenced block with a language is rendered each time while highlight.js is missing');
globalThis.hljs = { getLanguage: () => null, highlight: () => ({ value: '' }) };
renderMarkdown(fenced, undefined, 'chat'); renderMarkdown(fenced, undefined, 'chat');
assert.equal(parses, 6, 'once highlight.js is there it is kept');
delete globalThis.hljs;

const before = parses;
for (let i = 0; i < 450; i++) renderMarkdown('message ' + i, undefined, 'chat');
assert.equal(parses, before + 450);
renderMarkdown('message 449', undefined, 'chat');
assert.equal(parses, before + 450, 'recent entries are kept');
renderMarkdown('message 0', undefined, 'chat');
assert.equal(parses, before + 451, 'the cache is bounded: the oldest entries were dropped');
renderMarkdown('x'.repeat(30000), undefined, 'chat'); renderMarkdown('x'.repeat(30000), undefined, 'chat');
assert.equal(parses, before + 453, 'very long text is not kept');
console.log('markdown_cache: ok');
