/*
 * Navigation-result tests: the conversion from raw LSP locations into what the
 * definition jump, the multi-target picker, and the references panel actually
 * render and open.
 *
 * These exercise the pure helpers in lsp_client.js. The DOM-level pieces in
 * changed_files.js (openWorkspaceFile and the back stack) are covered
 * separately — see the note at the bottom of this file.
 */

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

const source = await readFile(new URL('./lsp_client.js', import.meta.url), 'utf8');
const moduleURL = `data:text/javascript;base64,${Buffer.from(source).toString('base64')}`;
const {
    fileURIToRelativePath,
    groupLocations,
    lspPositionToOffset,
    offsetToLspPosition,
} = await import(moduleURL);

const WORKSPACE = '/private/var/checkout';

// resolveTargets mirrors what the definition and references paths do with a
// result: turn each location into {filepath, range} pairs keyed by file.
function resolveTargets(locations, workspace = WORKSPACE) {
    return [...groupLocations(locations, workspace).entries()]
        .flatMap(([filepath, entries]) => entries.map(entry => ({ filepath, ...entry })));
}

test('a single definition resolves to the right file and range', () => {
    const [target] = resolveTargets([{
        uri: `file://${WORKSPACE}/internal/api/server.go`,
        range: { start: { line: 41, character: 5 }, end: { line: 41, character: 16 } },
    }]);

    assert.equal(target.filepath, 'internal/api/server.go');
    assert.equal(target.line, 42, 'display line must be one-based');
    assert.equal(target.column, 6, 'display column must be one-based');
    assert.deepEqual(target.range.start, { line: 41, character: 5 },
        'the raw zero-based range must survive for the editor selection');
    assert.deepEqual(target.range.end, { line: 41, character: 16 });
});

test('a LocationLink definition resolves via targetUri and targetSelectionRange', () => {
    // gopls returns LocationLink when the client advertises linkSupport; the
    // selection range is the identifier, the target range is the whole decl.
    const [target] = resolveTargets([{
        targetUri: `file://${WORKSPACE}/pkg/store/db.go`,
        targetRange: { start: { line: 9, character: 0 }, end: { line: 20, character: 1 } },
        targetSelectionRange: { start: { line: 9, character: 5 }, end: { line: 9, character: 9 } },
    }]);

    assert.equal(target.filepath, 'pkg/store/db.go');
    assert.deepEqual(target.range.start, { line: 9, character: 5 },
        'the selection range should win over the full declaration range');
    assert.equal(target.line, 10);
    assert.equal(target.column, 6);
});

test('multiple definitions produce one picker entry per target', () => {
    const targets = resolveTargets([
        {
            uri: `file://${WORKSPACE}/a/impl_linux.go`,
            range: { start: { line: 3, character: 5 }, end: { line: 3, character: 8 } },
        },
        {
            uri: `file://${WORKSPACE}/a/impl_darwin.go`,
            range: { start: { line: 7, character: 5 }, end: { line: 7, character: 8 } },
        },
    ]);

    assert.equal(targets.length, 2, 'the picker must offer every candidate');
    assert.deepEqual(targets.map(t => t.filepath), ['a/impl_linux.go', 'a/impl_darwin.go']);
    assert.deepEqual(targets.map(t => t.line), [4, 8]);
    assert.equal(new Set(targets.map(t => `${t.filepath}:${t.line}:${t.column}`)).size, 2,
        'picker entries must be distinguishable to the user');
});

test('references group by file, preserving per-file order and one-based display', () => {
    const groups = groupLocations([
        { uri: `file://${WORKSPACE}/main.go`, range: { start: { line: 10, character: 1 }, end: { line: 10, character: 4 } } },
        { uri: `file://${WORKSPACE}/pkg/use.go`, range: { start: { line: 2, character: 0 }, end: { line: 2, character: 3 } } },
        { uri: `file://${WORKSPACE}/main.go`, range: { start: { line: 4, character: 8 }, end: { line: 4, character: 11 } } },
    ], WORKSPACE);

    assert.deepEqual([...groups.keys()], ['main.go', 'pkg/use.go'],
        'each file appears once, in first-seen order');
    assert.equal(groups.get('main.go').length, 2, 'both hits in one file group together');
    assert.deepEqual(groups.get('main.go').map(r => r.line), [11, 5],
        'entries keep server order within a file');
    assert.equal(groups.get('pkg/use.go')[0].column, 1);
});

test('a location with no usable range is skipped rather than rendered broken', () => {
    const groups = groupLocations([
        { uri: `file://${WORKSPACE}/main.go` },
        { range: { start: { line: 0, character: 0 }, end: { line: 0, character: 1 } } },
        { uri: `file://${WORKSPACE}/ok.go`, range: { start: { line: 0, character: 0 }, end: { line: 0, character: 1 } } },
    ], WORKSPACE);

    assert.deepEqual([...groups.keys()], ['ok.go']);
});

test('empty results produce no groups, which is the "No references found" state', () => {
    assert.equal(groupLocations([], WORKSPACE).size, 0);
    assert.equal(groupLocations(null, WORKSPACE).size, 0);
});

test('navigation targets resolve against the canonical workspace root', () => {
    // The M2 fix: the broker reports a canonical root and returns canonical
    // URIs, so containment must succeed for a symlinked checkout...
    const canonical = '/private/var/folders/x/real-checkout';
    const [target] = resolveTargets(
        [{
            uri: `file://${canonical}/main.go`,
            range: { start: { line: 0, character: 0 }, end: { line: 0, character: 4 } },
        }],
        canonical);
    assert.equal(target.filepath, 'main.go');

    // ...while a genuinely external path is still refused.
    assert.throws(
        () => groupLocations([{
            uri: 'file:///etc/passwd',
            range: { start: { line: 0, character: 0 }, end: { line: 0, character: 1 } },
        }], canonical),
        /outside the workspace/);

    // A sibling directory sharing a name prefix must not pass containment.
    assert.throws(
        () => groupLocations([{
            uri: `file://${canonical}-other/main.go`,
            range: { start: { line: 0, character: 0 }, end: { line: 0, character: 1 } },
        }], canonical),
        /outside the workspace/);
});

test('paths with spaces and Unicode survive the round trip to a relative path', () => {
    const workspace = '/private/var/my checkout';
    const [target] = resolveTargets([{
        uri: `file://${workspace}/pkg/${encodeURIComponent('模块')}/${encodeURIComponent('文件 name.go')}`,
        range: { start: { line: 0, character: 0 }, end: { line: 0, character: 1 } },
    }], workspace);
    assert.equal(target.filepath, 'pkg/模块/文件 name.go');
});

test('selection offsets restore correctly across Unicode and non-BMP text', () => {
    // Opening a reference selects a range; the conversion must land on the
    // same characters the language server meant, including astral planes.
    const text = 'package main\n// 模块 🚀 emoji\nfunc 名前() {}\n';
    const cases = [
        { line: 1, character: 3 },
        { line: 1, character: 6 },  // just before the surrogate pair
        { line: 1, character: 8 },  // just after it
        { line: 2, character: 5 },
    ];
    for (const position of cases) {
        const offset = lspPositionToOffset(text, position);
        assert.deepEqual(offsetToLspPosition(text, offset), position,
            `round trip failed for line ${position.line} char ${position.character}`);
    }

    // A non-BMP character occupies two UTF-16 units, which is what LSP counts.
    const emojiStart = lspPositionToOffset(text, { line: 1, character: 6 });
    const emojiEnd = lspPositionToOffset(text, { line: 1, character: 8 });
    assert.equal(text.slice(emojiStart, emojiEnd), '🚀',
        'a non-BMP selection must cover exactly the emoji');
});

test('a selection range converts to a contiguous editor slice', () => {
    const text = 'alpha beta\ngamma delta\n';
    const range = { start: { line: 1, character: 0 }, end: { line: 1, character: 5 } };
    const from = lspPositionToOffset(text, range.start);
    const to = lspPositionToOffset(text, range.end);
    assert.equal(text.slice(from, to), 'gamma');
});

test('out-of-range positions clamp instead of throwing', () => {
    const text = 'one\ntwo\n';
    assert.equal(lspPositionToOffset(text, { line: 99, character: 99 }), text.length);
    assert.equal(lspPositionToOffset(text, { line: -1, character: -1 }), 0);
    assert.deepEqual(offsetToLspPosition(text, 9999), offsetToLspPosition(text, text.length));
});

/*
 * NOT COVERED HERE: openWorkspaceFile / navigateWorkspaceBack in
 * changed_files.js. The back stack itself is simple (push previous
 * {filepath, mode, selection, scrollTop}, cap at 50, pop on back), but it is
 * interleaved with _previewState, _cmView and _openInlinePane, so it cannot be
 * imported under Node without stubbing the editor and the DOM. Testing it
 * properly needs a small pure history helper extracted from that module; that
 * file is owned by the Lead Developer, so the extraction is proposed on the
 * board rather than done unilaterally here.
 */
