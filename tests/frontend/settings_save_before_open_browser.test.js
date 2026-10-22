// A Save made before the Settings dialog has filled its fields must not clear the CLI paths (v1.4.80 gate: a suite opened
// the dialog without waiting for it and saved at once, and the program Coral runs for an agent type was cleared for the
// rest of the run). The CLI path fields are filled late in opening (after two requests); until then they are empty, and an
// empty field the person never touched is not a change. A field the person types into is still sent.
// The shard's own CLI paths are put back at the end.
// Two programs that exist: Coral refuses a CLI path that is not an executable file.
const EXE1 = (process.platform === 'win32' ? require('node:path').join(process.env.SystemRoot || 'C:\\Windows', 'System32', 'whoami.exe') : '/bin/echo'), EXE2 = (process.platform === 'win32' ? require('node:path').join(process.env.SystemRoot || 'C:\\Windows', 'System32', 'hostname.exe') : '/usr/bin/true');
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
  const put = body => ev(`fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(${JSON.stringify(body)})}).then(r=>r.status)`);
  const server = () => ev(`fetch('/api/settings').then(r=>r.json()).then(d=>d.settings)`);
  let original = {};
  const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
  try {
    await Page.enable(); await Page.navigate({ url: BASE }); await Page.loadEventFired();
    await ev(`new Promise(r=>{const t=setInterval(()=>{if(window.__coralStatic||document.readyState==='complete'){clearInterval(t);r(true)}},50)})`);
    original = await server();
    // This page knows the paths: they are in the settings it loaded.
    assert.equal(await put({ cli_path_claude: EXE1, cli_path_codex: EXE2 }), 200);
    await ev(`import(${MOD}+'settings_core.js').then(m=>m.loadSettings()).then(()=>true)`);

    // Open the dialog and Save in the same breath, as a suite that does not wait for it: the fields are not filled yet.
    const filled = await ev(`(async () => {
      const m = await import(${MOD}+'modals.js');
      m.showSettingsModal();                       // not awaited
      const before = document.getElementById('settings-cli-path-claude').value;
      document.getElementById('settings-fit-pane-width').checked = !document.getElementById('settings-fit-pane-width').checked;
      await m.applySettings();
      return before;
    })()`);
    assert.equal(filled, '', 'the CLI path field is not filled yet when the Save is made');
    const s = await server();
    assert.equal(s.cli_path_claude, EXE1, 'a Save before the fields are filled leaves the CLI paths alone');
    assert.equal(s.cli_path_codex, EXE2);

    // A path typed into the field (after the dialog filled it) is still saved, and one cleared on purpose is cleared.
    await ev(`import(${MOD}+'modals.js').then(m=>m.showSettingsModal()).then(()=>true)`);
    await ev(`new Promise(r=>{const t=setInterval(()=>{if(document.getElementById('settings-cli-path-claude').value===${JSON.stringify(EXE1)}){clearInterval(t);r(true)}},50)})`);
    await ev(`(()=>{const e=document.getElementById('settings-cli-path-codex');e.value='';e.dispatchEvent(new Event('input',{bubbles:true}));return true;})()`);
    await ev(`import(${MOD}+'modals.js').then(m=>m.applySettings()).then(()=>true)`);
    const s2 = await server();
    assert.equal(s2.cli_path_codex || '', '', 'a path cleared on purpose is cleared');
    assert.equal(s2.cli_path_claude, EXE1, 'and the other is untouched');
    console.log('PASS a Save before the dialog is filled does not clear the CLI paths');
  } finally {
    try {
      const back = {};
      for (const k of ['cli_path_claude', 'cli_path_codex']) back[k] = original[k] || '';
      await put(back);
    } catch { /* the page is gone */ }
    await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
    await client.close();
  }
})().catch(error => { console.error(error); process.exit(1); });
