// test_base.js resolves the suites' server URL from CORAL_TEST_URL only and refuses production.
'use strict';
const assert = require('assert');
const { execFileSync } = require('./helpers/child_process.js');
const path = require('path');

const MOD = path.join(__dirname, 'test_base.js');
// Load test_base.js in a child with exactly env, so its load-time check runs as a suite would see it.
function load(env) {
    try {
        const out = execFileSync(process.execPath, ['-e', `process.stdout.write(require(${JSON.stringify(MOD)}).BASE)`],
            { env: { PATH: process.env.PATH, ...env }, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
        return { code: 0, out };
    } catch (e) { return { code: e.status, out: e.stderr }; }
}

// Default, and CORAL_URL is ignored even when it names the live server.
assert.deepStrictEqual(load({}), { code: 0, out: 'http://127.0.0.1:8462' });
assert.deepStrictEqual(load({ CORAL_URL: 'http://127.0.0.1:8420' }), { code: 0, out: 'http://127.0.0.1:8462' });
assert.deepStrictEqual(load({ CORAL_URL: 'http://127.0.0.1:8420', CORAL_TEST_URL: 'http://127.0.0.1:8475/' }), { code: 0, out: 'http://127.0.0.1:8475' });

// Refused with exit 2: production port on any host, no port, a default port, not a URL, wrong scheme.
// The old CORAL_TEST_ALLOW_PROD hatch no longer overrides anything.
for (const url of ['http://127.0.0.1:8420', 'http://localhost:8420/', 'http://192.168.1.5:8420', 'http://localhost',
    'http://127.0.0.1:80', 'not a url', 'ws://127.0.0.1:8475']) {
    for (const extra of [{}, { CORAL_TEST_ALLOW_PROD: '1' }]) {
        const r = load({ CORAL_TEST_URL: url, ...extra });
        assert.strictEqual(r.code, 2, `${url} ${JSON.stringify(extra)} should be refused: ${JSON.stringify(r)}`);
        assert.match(r.out, /CORAL_TEST_URL|production/);
    }
}
console.log('test_base: ok');
