// formatDurationSec (#3152):  node duration_format.test.mjs
// Minutes are rounded on the total so they carry into the hour: never "60m"
// or "69h 60m" (the agent row's execution time used to show both).
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const { formatDurationSec } = await importUi('utils');

for (const [sec, want] of [
    [null, ''], [undefined, ''], [0, ''], [-5, ''], ['abc', ''],
    [1, '1s'], [59, '59s'], [59.9, '59s'],
    [60, '1m'], [89, '1m'], [90, '2m'], [3569, '59m'],
    [3570, '1h'], [3599, '1h'], [3600, '1h'], [3660, '1h 1m'],
    [7199, '2h'], [7230, '2h 1m'],
    [69 * 3600 + 3590, '70h'], [69 * 3600 + 3569, '69h 59m'],
]) {
    assert.strictEqual(formatDurationSec(sec), want, `formatDurationSec(${sec})`);
}
// No minute part ever reaches 60.
for (let s = 60; s < 200 * 3600; s += 7) {
    const out = formatDurationSec(s);
    const m = /(\d+)m$/.exec(out);
    assert.ok(!m || Number(m[1]) < 60, `${s}s -> ${out}`);
}
console.log('duration_format.test.mjs: ok');
