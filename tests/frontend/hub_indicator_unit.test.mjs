// The Coral Hub indicator (#3544): which states show and how, the hub's host, and a label
// that names the state, the host and the time, never a token, code or id.
import assert from 'node:assert/strict';
import { indicatorState, hubHost, indicatorLabel } from '../../coral-go/pkg/frontend/static/hub_indicator.js';

for (const [state, want] of [['connected', 'connected'], ['connecting', 'connecting'], ['retrying', 'connecting'],
    ['revoked', 'disconnected'], ['key_unreadable', 'disconnected'], ['upgrade_required', 'disconnected'],
    ['off', null], ['idle', null], ['enrolling', null], ['something-new', null]]) {
    assert.equal(indicatorState({ state }), want, state);
}
assert.equal(indicatorState({ state: 'connected', loading: true }), null, 'loading: not final yet');
// Enrolled with the hub's old host (#3546): disconnected, not hidden; plain idle stays hidden.
const moved = { state: 'idle', last_error_code: 'hub_moved', hub_url: 'https://app.coralhub.dev',
    last_error: "this server is enrolled with a different hub (https://app.coralai.ai); run 'coral cloud connect --force' to enroll with https://app.coralhub.dev" };
assert.equal(indicatorState(moved), 'disconnected');
assert.equal(indicatorState({ state: 'idle', last_error_code: 'code_expired' }), null);
assert.equal(indicatorLabel(moved, 'disconnected', new Date()), 'Coral Hub moved to app.coralhub.dev: run coral cloud connect --force', 'the fixed message, the new host only');
assert.equal(indicatorState(null), null);

assert.equal(hubHost('https://app.coralhub.dev'), 'app.coralhub.dev');
assert.equal(hubHost('http://127.0.0.1:9000/x'), '127.0.0.1:9000');
assert.equal(hubHost('javascript:alert(1)'), '');
assert.equal(hubHost(''), '');

const now = new Date('2026-10-15T09:00:00Z');
const st = { state: 'connected', hub_url: 'https://app.coralhub.dev', connected_at: '2026-10-15T07:42:00Z',
    server_id: 'srv_secret_id', user_code: 'ABCD-EFGH', label: 'Work Mac' };
const l = indicatorLabel(st, 'connected', now, 'en-US');
assert.match(l, /^Coral Hub: connected to app\.coralhub\.dev since \d{1,2}:42/);
for (const secret of ['srv_secret_id', 'ABCD-EFGH']) assert.ok(!l.includes(secret), 'no id or code in the label');
assert.match(indicatorLabel({ ...st, connected_at: '2026-10-12T07:42:00Z' }, 'connected', now, 'en-US'), /since Oct 12, /, 'another day: the date too');
assert.equal(indicatorLabel({ state: 'connected', hub_url: 'https://h.test' }, 'connected', now), 'Coral Hub: connected to h.test');
assert.equal(indicatorLabel({ state: 'retrying', hub_url: 'https://h.test' }, 'connecting', now), 'Coral Hub: reconnecting to h.test');
assert.equal(indicatorLabel({ state: 'connecting' }, 'connecting', now), 'Coral Hub: connecting');
assert.equal(indicatorLabel({ state: 'revoked', hub_url: 'https://h.test' }, 'disconnected', now), 'Coral Hub: disconnected from h.test (the hub no longer accepts this Coral)');
console.log('hub_indicator_unit: ok');
