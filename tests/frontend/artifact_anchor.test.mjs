// The comment anchor (#5182), plain strings: what a quote is compared as, the anchor built from a selection, the lines found in the source, a quote located again after the text moved or repeated,
// "text changed" when it is gone, and the text index that maps squashed offsets back to nodes. Run on its own: node artifact_anchor.test.mjs
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const { squash, buildAnchor, lineRange, locateQuote, indexText, QUOTE_MAX, CONTEXT } = await import(pathToFileURL(path.resolve(here, '../../coral-go/pkg/frontend/static/artifact_anchor.js')).href);

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
assert.equal(ix.text, 'Hello world'); assert.equal(ix.length, ix.text.length);
assert.deepEqual([ix.at(0).node === nodes[0], ix.at(0).offset], [true, 1]); assert.equal(ix.at(5).node, nodes[0]); assert.deepEqual([ix.at(6).node === nodes[1], ix.at(6).offset], [true, 0]);
assert.deepEqual([ix.at(10).node === nodes[2], ix.at(10).offset], [true, 1], 'the last character maps to its node');
assert.equal(indexText([]).text, ''); assert.equal(indexText([{ nodeValue: '   ' }]).text, '');
// the typed-array index says what the character-by-character one said (#5255): the same text and the same node and offset for every character, over every kind of white space JavaScript's \s knows
{
    const reference = (ns) => { let text = ''; const at = []; for (const node of ns) { const s = node.nodeValue; for (let i = 0; i < s.length; i++) { const ws = /\s/.test(s[i]); if (ws && (text === '' || text.endsWith(' '))) continue; text += ws ? ' ' : s[i]; at.push({ node, offset: i }); } } if (text.endsWith(' ')) { text = text.slice(0, -1); at.pop(); } return { text, at }; };
    const alphabet = ['a', 'b', 'é', ' ', ' ', '\n', '\t', '\r', '\v', '\f', ' ', ' ', ' ', ' ', '\u2028', '\u2029', ' ', ' ', '　', '\ufeff', '\u200b', '\u0085', '😀'];
    let seed = 12345; const rnd = (n) => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed % n; };
    for (let round = 0; round < 300; round++) {
        const ns = Array.from({ length: rnd(6) }, () => ({ nodeValue: Array.from({ length: rnd(12) }, () => alphabet[rnd(alphabet.length)]).join('') }));
        const want = reference(ns), got = indexText(ns);
        assert.equal(got.text, want.text, 'round ' + round + ': the text');
        assert.equal(got.length, want.at.length, 'round ' + round + ': one entry a character');
        for (let i = 0; i < got.length; i++) { const a = got.at(i); assert.ok(a.node === want.at[i].node && a.offset === want.at[i].offset, `round ${round}: character ${i} maps to the same node and offset`); }
    }
}
// lineRange, rewritten to squash the source once and keep it (#5255), says what the character-by-character version said: same lines for quotes found whole, by their first and last words, and not found
{
    const refLineRange = (source, quote) => {
        if (!source || !quote) return null;
        let flat = ''; const from = [];
        for (let i = 0; i < source.length; i++) { const ws = /\s/.test(source[i]); if (ws && (flat === '' || flat.endsWith(' '))) continue; flat += ws ? ' ' : source[i]; from.push(i); }
        const lineAt = (fi) => source.slice(0, from[Math.min(fi, from.length - 1)]).split('\n').length;
        let start = flat.indexOf(quote);
        if (start >= 0) return [lineAt(start), lineAt(start + quote.length - 1)];
        const words = quote.split(' ');
        if (words.length < 2) return null;
        const first = words.slice(0, 3).join(' '), last = words.slice(-3).join(' ');
        start = flat.indexOf(first);
        const endAt = start >= 0 ? flat.indexOf(last, start) : -1;
        return start >= 0 && endAt >= 0 ? [lineAt(start), lineAt(endAt + last.length - 1)] : null;
    };
    const words = ['alpha', 'beta', 'gamma', 'delta', 'eps', 'x', 'y'];
    let seed = 777; const rnd = (n) => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed % n; };
    const seps = [' ', '  ', '\n', '\n\n', '\t', ' \n ', '\u00a0', '\r\n'];
    for (let round = 0; round < 200; round++) {
        let source = rnd(3) === 0 ? ' \n' : ''; const n = 1 + rnd(40);
        for (let i = 0; i < n; i++) source += words[rnd(words.length)] + seps[rnd(seps.length)];
        const ws = source.split(/\s+/).filter(Boolean);
        const a = rnd(Math.max(1, ws.length)), b = Math.min(ws.length, a + 1 + rnd(6));
        for (const quote of [ws.slice(a, b).join(' '), ws.slice(a, a + 1).join(' ') + ' nothere ' + ws.slice(b - 1, b).join(' '), 'absent words here', ws[a] || 'x']) {
            assert.deepEqual(lineRange(source, quote), refLineRange(source, quote), `round ${round}: ${JSON.stringify(quote)} in ${JSON.stringify(source)}`);
        }
    }
    assert.deepEqual(lineRange('one\ntwo\nthree', 'two'), [2, 2]); assert.equal(lineRange('', 'x'), null);
    const big = Array.from({ length: 3000 }, (_, i) => 'line ' + i + ' of the file').join('\n'); const t0 = Date.now();
    assert.deepEqual(lineRange(big, 'line 2999 of the file'), [3000, 3000]); for (let i = 0; i < 20; i++) lineRange(big, 'line ' + (i * 100) + ' of the file');
    assert.ok(Date.now() - t0 < 1500, 'twenty-one anchors on a 3,000-line source take ' + (Date.now() - t0) + ' ms: the source is squashed once, not for each');
}
console.log('artifact_anchor: ok');
