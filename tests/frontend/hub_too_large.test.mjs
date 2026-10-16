// #3718: a reply Coral Hub refused as too large to check for secrets (a JSON reply over 2 MiB,
// {"code":"hub_reply_too_large"}) is said as such, never shown as an empty view:
//   - refusedTooLarge tells it from another 502 or a normal reply, and leaves the body readable;
//   - the changed-files panel says there are too many files to show, not "No changed files".
//   node hub_too_large.test.mjs
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

const dom = installDom({ mode: 'cloud' });
const w = dom.window;
const utils = await importUi('utils');
const json = (status, body) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const refused = json(502, { error: 'too large', code: 'hub_reply_too_large' });
assert.strictEqual(await utils.refusedTooLarge(refused), true, 'the refusal');
assert.strictEqual((await refused.json()).code, 'hub_reply_too_large', 'its body is still readable');
assert.strictEqual(await utils.refusedTooLarge(json(502, { error: 'bad gateway' })), false, 'another 502');
assert.strictEqual(await utils.refusedTooLarge(json(200, { code: 'hub_reply_too_large' })), false, 'a 200');
assert.strictEqual(await utils.refusedTooLarge(new Response('<html>', { status: 502, headers: { 'Content-Type': 'text/html' } })), false, 'not JSON');
assert.strictEqual(await utils.refusedTooLarge(null), false);

const { state } = await importUi('state');
const cf = await importUi('changed_files');
state.currentSession = { type: 'live', name: 'qa-agent', session_id: 'qa-sid', agent_type: 'claude' };
const list = () => w.document.getElementById('changed-files-list').textContent;
let reply = json(502, { error: 'too large', code: 'hub_reply_too_large' });
globalThis.fetch = w.fetch = async () => reply.clone();
await cf.loadChangedFiles('qa-agent', 'qa-sid');
assert.match(list(), /Too many changed files to show through Coral Hub/, 'the panel says why: ' + list());
assert.doesNotMatch(list(), /No changed files/);
reply = json(200, { files: [] });
await cf.loadChangedFiles('qa-agent', 'qa-sid');
assert.match(list(), /No changed files/, 'an empty list is still "No changed files"');
reply = json(502, { error: 'bad gateway' });
await cf.loadChangedFiles('qa-agent', 'qa-sid');
assert.match(list(), /No changed files/, 'another failure is not called too large');

console.log('hub_too_large.test.mjs: ok');
process.exit(0);
