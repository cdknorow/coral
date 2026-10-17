// Unit test for the list settings (saved personas, templates, macros): a change is applied to the list the server has now.
//   node settings_lists.test.mjs
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const m = await import(pathToFileURL(path.resolve(here, '../../coral-go/pkg/frontend/static/settings_lists.js')).href);

// A fake server: GET /api/settings answers {settings}, PUT stores what it is sent.
function server(settings, { getFails = false, putStatus = 200 } = {}) {
    const puts = [];
    globalThis.fetch = async (url, opts = {}) => {
        if (opts.method === 'PUT') { puts.push(JSON.parse(opts.body)); if (putStatus === 200) Object.assign(settings, JSON.parse(opts.body)); return new Response('{}', { status: putStatus }); }
        if (getFails) throw new Error('offline');
        return new Response(JSON.stringify({ settings }), { status: 200 });
    };
    return puts;
}

// parseList, upsertByName, removeFirst
assert.deepEqual(m.parseList('', ['d']), ['d']);
assert.deepEqual(m.parseList('not json', ['d']), ['d']);
assert.deepEqual(m.parseList('{"a":1}', ['d']), ['d']);
assert.deepEqual(m.parseList('[1,2]', ['d']), [1, 2]);
assert.deepEqual(m.upsertByName([{ name: 'a', v: 1 }, { name: 'b', v: 1 }], [{ name: 'b', v: 2 }, { name: 'c', v: 3 }]), [{ name: 'a', v: 1 }, { name: 'b', v: 2 }, { name: 'c', v: 3 }]);
assert.deepEqual(m.removeFirst([1, 2, 3, 2], x => x === 2), [1, 3, 2]);
assert.deepEqual(m.removeFirst([1, 3], x => x === 2), [1, 3]);

// An item another client added meanwhile is kept: the change lands on the server's list, not the page's copy.
let store = { saved_personas: JSON.stringify([{ name: 'mine' }, { name: 'theirs' }]) };
let puts = server(store);
let res = await m.updateSettingList('saved_personas', l => m.upsertByName(l, [{ name: 'new' }]), { local: [{ name: 'mine' }] });
assert.deepEqual(res.list.map(x => x.name), ['mine', 'theirs', 'new']);
assert.equal(res.raw, JSON.stringify(res.list));
assert.deepEqual(puts, [{ saved_personas: res.raw }], 'one PUT with just this setting');

// A delete by identity removes the item the person meant, not what is now at that position.
store = { macros: JSON.stringify([{ label: 'added elsewhere', command: 'x' }, { label: 'mine', command: 'y' }]) };
server(store);
res = await m.updateSettingList('macros', l => m.removeFirst(l, x => x.label === 'mine' && x.command === 'y'), { fallback: [{ label: 'default', command: 'd' }] });
assert.deepEqual(res.list, [{ label: 'added elsewhere', command: 'x' }]);

// No such setting yet: the fallback (defaults) is the list.
store = {};
server(store);
res = await m.updateSettingList('macros', l => [...l, { label: 'n', command: 'c' }], { fallback: [{ label: 'default', command: 'd' }] });
assert.deepEqual(res.list, [{ label: 'default', command: 'd' }, { label: 'n', command: 'c' }]);

// The server cannot be read: the page's copy is the list, as a save always did.
store = { saved_personas: '[]' };
server(store, { getFails: true });
res = await m.updateSettingList('saved_personas', l => m.upsertByName(l, [{ name: 'x' }]), { local: [{ name: 'mine' }] });
assert.deepEqual(res.list.map(x => x.name), ['mine', 'x']);

// A refused save throws.
server({}, { putStatus: 500 });
await assert.rejects(() => m.updateSettingList('macros', l => l, {}), /saving macros failed \(500\)/);
console.log('PASS settings lists');
