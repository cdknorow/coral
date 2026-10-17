// The Settings dialog opens with the settings the server has NOW and Save sends only what the person changed
// (v1.4.79 gate: a CLI path set elsewhere came back empty after a Save from a page that had loaded earlier).
//   - a path another client saved after this page loaded shows in the dialog (a moment after it opens);
//   - a path another client saves while the dialog is open survives a Save of an unrelated field;
//   - a path changed in the dialog still saves, and one cleared on purpose is cleared.
// The shard's own settings are put back at the end: its CLI paths are the stand-ins the CDP runner set.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
  const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
  const { Page, Runtime } = client;
  const ev = async expression => {
    const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
    return result.result.value;
  };
  const MOD = `(window.__coralStatic||'/static/')`;
  // Another client: a PUT straight to the server, not through this page's dialog.
  const otherClientPut = body => ev(`fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(${JSON.stringify(body)})}).then(r=>r.status)`);
  const server = () => ev(`fetch('/api/settings').then(r=>r.json()).then(d=>d.settings)`);
  const field = (id, value) => ev(`(()=>{const e=document.getElementById(${JSON.stringify(id)});e.value=${JSON.stringify(value)};e.dispatchEvent(new Event('input',{bubbles:true}));return e.value;})()`);
  const value = id => ev(`document.getElementById(${JSON.stringify(id)}).value`);
  const openDialog = () => ev(`import(${MOD}+'modals.js').then(m=>m.showSettingsModal()).then(()=>true)`);
  const save = () => ev(`import(${MOD}+'modals.js').then(m=>m.applySettings()).then(()=>true)`);
  const CLAUDE = 'settings-cli-path-claude', CODEX = 'settings-cli-path-codex', WDIR = 'settings-working-dir';
  let original = {};
  try {
    await Page.enable(); await Page.navigate({ url: BASE }); await Page.loadEventFired();
    await ev(`new Promise(r=>{const t=setInterval(()=>{if(window.__coralStatic||document.readyState==='complete'){clearInterval(t);r(true)}},50)})`);
    original = await server();
    const keep = k => original[k] || '';
    const exe1 = '/bin/echo', exe2 = '/usr/bin/true';

    // 1. A path another client saved after this page loaded shows when the dialog opens.
    assert.equal(await otherClientPut({ cli_path_claude: exe1 }), 200);
    await openDialog();
    for (let i = 0; i < 60 && await value(CLAUDE) !== exe1; i++) await new Promise(r => setTimeout(r, 50));
    assert.equal(await value(CLAUDE), exe1, 'the dialog shows the settings as the server has them now');

    // 2. A path another client saves while the dialog is open survives a Save of an unrelated field.
    assert.equal(await otherClientPut({ cli_path_codex: exe2, cli_path_claude: exe2 }), 200); // codex new, claude changed again
    await field(WDIR, '/tmp/coral-stale-save-test');
    await save();
    let s = await server();
    assert.equal(s.default_working_dir, '/tmp/coral-stale-save-test', 'the field the person changed is saved');
    assert.equal(s.cli_path_codex, exe2, 'a path another client saved meanwhile is not overwritten');
    assert.equal(s.cli_path_claude, exe2, 'a path the dialog showed (and did not touch) is not written back over the newer one');

    // 3. A path changed in the dialog still saves.
    await openDialog();
    await field(CLAUDE, exe1);
    await save();
    s = await server();
    assert.equal(s.cli_path_claude, exe1, 'a changed path is saved');
    assert.equal(s.cli_path_codex, exe2, 'and the one beside it is as it was');

    // 4. A field cleared on purpose is cleared.
    await openDialog();
    await field(CODEX, '');
    await save();
    s = await server();
    assert.equal(s.cli_path_codex || '', '', 'a path cleared on purpose is cleared');
    assert.equal(s.cli_path_claude, exe1, 'and the other is untouched');
    console.log('PASS settings dialog opens fresh and saves only what changed');
  } finally {
    // Back to the shard's own values.
    try {
      await ev(`fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(${JSON.stringify({
        cli_path_claude: original.cli_path_claude || '', cli_path_codex: original.cli_path_codex || '', default_working_dir: original.default_working_dir || '' })})}).then(r=>r.status)`);
    } catch { /* the page is gone: nothing to restore through it */ }
    await client.close();
  }
})().catch(error => { console.error(error); process.exit(1); });
