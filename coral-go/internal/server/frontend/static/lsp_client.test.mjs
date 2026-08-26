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
    pathToFileURI,
} = await import(moduleURL);

test('UTF-16 positions round-trip ASCII and non-BMP text', () => {
    const text = 'alpha\nA😀B\nomega';
    for (const offset of [0, 5, 6, 7, 9, 10, text.length]) {
        const position = offsetToLspPosition(text, offset);
        assert.equal(lspPositionToOffset(text, position), offset);
    }
    assert.deepEqual(offsetToLspPosition(text, 10), { line: 1, character: 4 });
});

test('file URIs preserve spaces and Unicode and remain workspace relative', () => {
    const uri = pathToFileURI('/tmp/my project', 'src/你好 file.go');
    assert.equal(uri, 'file:///tmp/my%20project/src/%E4%BD%A0%E5%A5%BD%20file.go');
    assert.equal(fileURIToRelativePath(uri, '/tmp/my project'), 'src/你好 file.go');
    assert.throws(() => fileURIToRelativePath('file:///tmp/other/x.go', '/tmp/my project'));
    const windowsURI = pathToFileURI('C:\\work tree', 'src\\main.go');
    assert.equal(fileURIToRelativePath(windowsURI, 'C:\\work tree'), 'src/main.go');
});

test('locations group by relative path with one-based display positions', () => {
    const locations = [{
        uri: 'file:///workspace/a.go',
        range: { start: { line: 2, character: 4 }, end: { line: 2, character: 7 } },
    }];
    const grouped = groupLocations(locations, '/workspace');
    assert.equal(grouped.get('a.go')[0].line, 3);
    assert.equal(grouped.get('a.go')[0].column, 5);
});
