// The comment anchor (#5182), plain strings: what a quote is compared as, the anchor built from a selection, the lines found in the source, a quote located again after the text moved or repeated,
// "text changed" when it is gone, and the text index that maps squashed offsets back to nodes. Run on its own: node artifact_anchor.test.mjs
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const { squash, buildAnchor, lineRange, locateQuote, indexText, QUOTE_MAX, CONTEXT } = await import(path.resolve(here, '../../coral-go/pkg/frontend/static/artifact_anchor.js'));

assert.equal(squash('  a \n\t b   c '), 'a b c'); assert.equal(squash(null), '');
// buildAnchor
assert.equal(buildAnchor('before ', '   ', ' after', 'x'), null, 'white space only selects nothing');
const src = '# Plan\n\nAdd the new columns behind a flag.\nSwitch reads.\n';
let a = buildAnchor('Plan Add the ', 'new columns\nbehind a flag', '. Switch reads.', src);
assert.deepEqual(a, { quote: 'new columns behind a flag', prefix: 'Plan Add the', suffix: '. Switch reads.', start_line: 3, end_line: 3 });
assert.equal(buildAnchor('', 'x'.repeat(QUOTE_MAX + 50), '', '').quote.length, QUOTE_MAX, 'the quote is capped');
assert.equal(buildAnchor('p'.repeat(500), 'q', 's'.repeat(500), '').prefix.length, CONTEXT); assert.equal(buildAnchor('p'.repeat(500), 'q', 's'.repeat(500), '').suffix.length, CONTEXT);
// lines: whole quote, then first and last words, then null
assert.deepEqual(lineRange(src, 'Switch reads.'), [4, 4]);
assert.deepEqual(lineRange('one two\nthree four five six\nseven eight', 'two three four five six seven'), [1, 3], 'a quote with markers between its words is found by its ends');
assert.equal(lineRange(src, 'not there at all'), null); assert.equal(lineRange('', 'x'), null); assert.equal(lineRange(src, 'zzz'), null);
// locate
const text = 'Goal Move the store. Steps Add a flag. Risks Add a flag and wait. Done';
let hit = locateQuote(text, { quote: 'Move the store.' }); assert.deepEqual(hit, { start: 5, end: 20 });
const two = locateQuote(text, { quote: 'Add a flag', prefix: 'Risks', suffix: 'and wait.' });
assert.equal(text.slice(two.start - 6, two.start), 'Risks ', 'a repeat is told apart by its prefix and suffix');
const first = locateQuote(text, { quote: 'Add a flag', prefix: 'Steps', suffix: '. Risks' });
assert.equal(text.slice(first.start - 6, first.start), 'Steps ', 'and the other repeat by its own');
assert.equal(locateQuote(text, { quote: 'Add a flag' }).start, text.indexOf('Add a flag'), 'a tie takes the first');
assert.equal(locateQuote(text, { quote: 'gone words' }), null, 'text that changed is not found'); assert.equal(locateQuote(text, { whole: true }), null); assert.equal(locateQuote(text, null), null);
assert.deepEqual(locateQuote('A  b\n c', { quote: 'b c' }), null, 'the text is compared squashed, so the caller squashes it first');
// indexText over fake nodes
const nodes = [{ nodeValue: ' Hello  ' }, { nodeValue: 'wor' }, { nodeValue: 'ld \n' }];
const ix = indexText(nodes);
assert.equal(ix.text, 'Hello world'); assert.equal(ix.at.length, ix.text.length);
assert.deepEqual([ix.at[0].node === nodes[0], ix.at[0].offset], [true, 1]); assert.equal(ix.at[5].node, nodes[0]); assert.deepEqual([ix.at[6].node === nodes[1], ix.at[6].offset], [true, 0]);
assert.deepEqual([ix.at[10].node === nodes[2], ix.at[10].offset], [true, 1], 'the last character maps to its node');
assert.equal(indexText([]).text, ''); assert.equal(indexText([{ nodeValue: '   ' }]).text, '');
console.log('artifact_anchor: ok');
