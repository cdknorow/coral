import assert from 'node:assert/strict';
globalThis.location = { search: '' };
globalThis.localStorage = { getItem: () => null, setItem: () => {} };
globalThis.document = { createElement: () => ({ textContent: '', get innerHTML() {
    return this.textContent.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
} }) };
const { highlightExcerpt, isSearchResponseUsable } = await import('../../coral-go/internal/server/frontend/static/chat_search.js');

assert.equal(isSearchResponseUsable('complete'), true);
assert.equal(isSearchResponseUsable('partial'), true);
assert.equal(isSearchResponseUsable('unavailable'), false);
assert.equal(isSearchResponseUsable('ok'), false);

// Offsets are UTF-16 code units from the search API. This also verifies that
// excerpts remain text even when a transcript contains HTML-looking content.
const excerpt = '<img src=x onerror=alert(1)> café';
const html = highlightExcerpt(excerpt, [[0, 5], [30, 34]]);
assert.match(html, /^<mark>&lt;img/);
assert.doesNotMatch(html, /<img\b/i);
assert.match(html, /afé/);

// Overlapping and out-of-range offsets must produce stable, escaped output.
const safe = highlightExcerpt('alpha beta', [[0, 5], [3, 8], [-2, 99]]);
assert.match(safe, /<mark>alpha<\/mark>/);
assert.match(safe, /beta/);
console.log('chat_search_unit: ok');
