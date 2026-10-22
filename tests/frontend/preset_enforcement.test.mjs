// What the agent form says each CLI enforces (#4545, Security's #4550): preset_enforcement.js's Codex and
// Antigravity modes for every built-in preset match internal/agent/testdata/preset_enforcement.golden, which
// the Go translation (the one each launch uses) writes; the wording names the mode and says Deny and shell
// patterns aren't enforced there, and that Claude's Allow restricts nothing in Bypass Permissions mode.
//   node preset_enforcement.test.mjs
import assert from 'node:assert';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const { AGENT_PRESETS } = await importUi('agent_presets');
const pe = await importUi('preset_enforcement');

const golden = readFileSync(fileURLToPath(new URL('../../coral-go/internal/agent/testdata/preset_enforcement.golden', import.meta.url)), 'utf8');
const rows = new Map();
for (const line of golden.split('\n')) {
    if (!line || line.startsWith('#')) continue;
    const [name, agent, enforced] = line.split('\t');
    rows.set(`${name}\t${agent}`, enforced);
}
const codexToken = m => !m ? 'sandbox=default approval=default' : `sandbox=${m.sandbox} approval=${m.approval}${m.search ? ' search' : ''}`;
let checked = 0;
for (const p of AGENT_PRESETS) {
    if (p.agent_type) continue;
    assert.equal(codexToken(pe.codexEnforced(p.capabilities)), rows.get(`${p.name}\tcodex`), `${p.name}: Codex mode as the Go translation emits it`);
    assert.equal(`mode=${pe.agyEnforced(p.capabilities)}`, rows.get(`${p.name}\tagy`), `${p.name}: Antigravity mode as the Go translation emits it`);
    checked++;
}
assert.equal(checked * 3, rows.size, 'every golden row is a built-in preset');
// The combinations no preset uses, against the Go translation's branches
assert.deepEqual(pe.codexEnforced({ allow: ['shell'], deny: ['git_write'] }), { sandbox: 'workspace-write', approval: 'on-request', search: false });
assert.deepEqual(pe.codexEnforced({ allow: ['web_access'] }), { sandbox: 'read-only', approval: 'on-request', search: true });
assert.equal(pe.codexEnforced({}), null);
assert.equal(pe.agyEnforced({ allow: ['shell', 'file_write'], deny: ['file_write'] }), 'accept-edits', 'a Deny on Agy only changes the mode');

const fe = AGENT_PRESETS.find(p => p.name === 'Frontend Dev').capabilities;
let e = pe.presetEnforcement('codex', fe, 'bypassPermissions');
assert.match(e.text, /only a sandbox and an approval mode: a sandbox that can write the working folder, and it asks only when the model requests it/);
assert.match(e.text, /Deny entries and shell patterns are not enforced/);
assert.equal(e.denyEnforced, false);
assert.equal(e.patternsEnforced, false);
e = pe.presetEnforcement('codex', AGENT_PRESETS.find(p => p.name === 'Lead Developer').capabilities, 'default');
assert.match(e.text, /no sandbox, and it never asks.*Git Write together make it run without a sandbox/, 'Codex git_write: no sandbox (Security D)');
for (const t of ['agy', 'gemini', 'antigravity']) {
    e = pe.presetEnforcement(t, AGENT_PRESETS.find(p => p.name === 'Security Reviewer').capabilities, 'default');
    assert.match(e.text, /^Antigravity keeps only a mode: read-only planning/, t);
    assert.equal(e.denyEnforced, false, t);
}
e = pe.presetEnforcement('claude', fe, 'bypassPermissions');
assert.match(e.text, /Bypass Permissions mode: nothing asks, so Allow restricts nothing; only Deny blocks/);
assert.equal(e.denyEnforced, true);
e = pe.presetEnforcement('claude', fe, 'default');
assert.match(e.text, /Allow pre-approves these tools and others still run after it asks; Deny blocks/);
e = pe.presetEnforcement('claude', AGENT_PRESETS.find(p => p.name === 'Backend Dev').capabilities, 'default');
assert.match(e.text, /git patterns don't limit git while Shell is allowed/, 'Security F');
assert.equal(pe.presetEnforcement('terminal', {}, 'default').text, '');

// The permission mode a launch starts with when nobody picked one (#5836): Claude starts in Auto, not in a bypass of every prompt; the other CLIs keep their own default; a saved mode is
// the person's and applies to every CLI as before.
assert.equal(pe.defaultPermissionMode({}, 'claude'), 'auto');
assert.equal(pe.defaultPermissionMode(undefined, 'claude'), 'auto');
assert.equal(pe.defaultPermissionMode({}, ''), 'auto', 'no agent type named: Claude');
assert.equal(pe.defaultPermissionMode({}, undefined), 'auto');
assert.equal(pe.defaultPermissionMode({}, 'codex'), 'auto', 'Codex uses the workspace sandbox');
for (const other of ['agy', 'antigravity', 'gemini', 'pi']) assert.equal(pe.defaultPermissionMode({}, other), 'bypassPermissions', other + ' keeps its default');
for (const saved of ['bypassPermissions', 'default', 'plan', 'acceptEdits', 'dontAsk', 'auto']) {
    for (const type of ['claude', 'codex', 'agy', 'pi']) assert.equal(pe.defaultPermissionMode({ default_permission_mode: saved }, type), saved, `a saved ${saved} stays for ${type}`);
}
assert.equal(pe.defaultPermissionMode({ default_permission_mode: '' }, 'claude'), 'auto', 'an empty value is not a saved one');

// A select that still shows the default follows the agent type; a mode the person picked is never overridden
const fakeSelect = () => { const handlers = []; return { value: '', dataset: {}, addEventListener: (ev, fn) => { if (ev === 'change') handlers.push(fn); }, pick(v) { this.value = v; handlers.forEach(h => h()); } }; };
let sel = fakeSelect();
pe.syncDefaultPermissionMode(sel, {}, 'claude');
assert.equal(sel.value, 'auto');
pe.syncDefaultPermissionMode(sel, {}, 'codex');
assert.equal(sel.value, 'auto', 'Codex also uses Auto while the select follows the default');
pe.syncDefaultPermissionMode(sel, {}, 'agy');
assert.equal(sel.value, 'bypassPermissions', 'the type changed while it still showed the default');
pe.syncDefaultPermissionMode(sel, {}, 'claude');
assert.equal(sel.value, 'auto');
sel.pick('plan');
pe.syncDefaultPermissionMode(sel, {}, 'codex');
assert.equal(sel.value, 'plan', 'a picked mode stays when the agent type changes');
pe.syncDefaultPermissionMode(sel, { default_permission_mode: 'dontAsk' }, 'claude');
assert.equal(sel.value, 'plan');
sel = fakeSelect();
pe.syncDefaultPermissionMode(sel, { default_permission_mode: 'acceptEdits' }, 'claude');
assert.equal(sel.value, 'acceptEdits', 'a saved default is the default');
pe.syncDefaultPermissionMode(null, {}, 'claude'); // no select: nothing

console.log(`preset_enforcement: ok (${checked} presets)`);
