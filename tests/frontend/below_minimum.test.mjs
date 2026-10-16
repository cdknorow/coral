// The Hub's below-minimum hint (#3915): belowMinimumText gives "Update Coral to <min_coral>" only when below_minimum is exactly true, never
// anything for absent, false or non-boolean values, takes min_coral only when it is a string, trims and cuts it to 32 characters, and
// the details panel's markup escapes it.
//   node below_minimum.test.mjs
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const m = await importUi('server_status');
const t = m.belowMinimumText;
assert.equal(t({ below_minimum: true, min_coral: '1.4.61' }), 'Update Coral to 1.4.61');
assert.equal(t({ below_minimum: true, min_coral: '  1.4.61 ' }), 'Update Coral to 1.4.61', 'trimmed');
assert.equal(t({ below_minimum: true }), 'Update Coral', 'below with no version');
assert.equal(t({ below_minimum: true, min_coral: 7 }), 'Update Coral', 'a non-string version is ignored');
assert.equal(t({ below_minimum: true, min_coral: '' }), 'Update Coral');
for (const e of [null, undefined, {}, { min_coral: '1.4.61' }, { below_minimum: false, min_coral: '1.4.61' }, { below_minimum: 'true', min_coral: '1.4.61' }, { below_minimum: 1 }]) {
    assert.equal(t(e), '', 'nothing for ' + JSON.stringify(e));
}
const long = t({ below_minimum: true, min_coral: 'x'.repeat(100) });
assert.equal(long, 'Update Coral to ' + 'x'.repeat(32), 'cut to 32 characters');
const hostile = t({ below_minimum: true, min_coral: '<b onclick=1>' });
assert.equal(hostile, 'Update Coral to <b onclick=1>', 'the text itself is kept as text: callers escape it');
console.log('below_minimum: ok');
