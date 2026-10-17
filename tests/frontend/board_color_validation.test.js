// Pure-node unit test: the board colour read from localStorage is only ever a #rrggbb colour, so it
// is safe in a style attribute (#4195):
//   node board_color_validation.test.js
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
    globalThis.location = { protocol: 'http:', host: 'localhost' };
    const store = {};
    globalThis.localStorage = {
        getItem: (k) => (k in store ? store[k] : null),
        setItem: (k, v) => { store[k] = String(v); },
        removeItem: (k) => { delete store[k]; },
    };
    const { _getBoardColor } = await import(pathToFileURL(path.join(dir, 'board_accents.js')).href);
    const put = (colors) => { store['coral-board-colors'] = JSON.stringify(colors); };

    // Colours the picker stores come back as they are (it accepts upper case too).
    put({ a: '#81a1c1', b: '#ABCDEF' });
    assert.strictEqual(_getBoardColor('a'), '#81a1c1');
    assert.strictEqual(_getBoardColor('b'), '#ABCDEF');
    assert.strictEqual(_getBoardColor('missing'), null);

    // Hostile or malformed stored values read as no colour.
    const hostile = [
        'red;background:url(https://evil.example/x)', '#81a1c1;background:url(//x)', '#81a1c1" onclick="x', '#81a1c1 ',
        ' #81a1c1', '#81a1c', '#81a1c11', '81a1c1', 'rgb(1,2,3)', 'var(--x)', 'url(x)', '', '#ggg000',
        '#81a1c1\n', 42, true, null, ['#81a1c1'], { c: '#81a1c1' },
    ];
    for (const v of hostile) {
        put({ a: v });
        assert.strictEqual(_getBoardColor('a'), null, `stored ${JSON.stringify(v)} is not a colour`);
    }

    // A board named like an Object property does not read the prototype.
    put({});
    for (const name of ['__proto__', 'constructor', 'toString', 'hasOwnProperty']) assert.strictEqual(_getBoardColor(name), null, name);

    // Corrupt storage reads as no colour, not an error.
    store['coral-board-colors'] = '{not json';
    assert.strictEqual(_getBoardColor('a'), null);
    store['coral-board-colors'] = 'null';
    assert.strictEqual(_getBoardColor('a'), null);
    store['coral-board-colors'] = '"string"';
    assert.strictEqual(_getBoardColor('a'), null);

    console.log('board_color_validation: ok');
})().catch((e) => { console.error(e); process.exit(1); });
