// A Save that changed only something the Settings dialog saves apart from its payload says "Settings saved", not "Nothing to save" (#4423):
// the wake setting and the terminal backend choice are saved by terminal_backend.js, the database encryption choice by its own PUT.
//   - an unchanged Save says "Nothing to save: no setting changed.";
//   - only the wake switch changed: "Settings saved" (and the wake setting is saved);
//   - only the terminal backend choice changed: the dialog counts it as a change (hasUnsavedBackendChange), without applying it.
// The server is a tmux one; a fetch wrapper serves a pty backend status so the wake and backend parts of the dialog show.
// The shard's own wake setting is put back at the end.
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
  const until = async (expr, what, ms = 15000) => {
    const end = Date.now() + ms;
    while (Date.now() < end) { if (await ev(expr)) return; await new Promise(r => setTimeout(r, 50)); }
    throw new Error('timed out waiting for ' + what);
  };
  const lastToast = () => ev(`[...document.querySelectorAll('.toast')].map(t => t.textContent).pop() || ''`);
  const clearToasts = () => ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
  const openDialog = async () => {
    await ev(`window.showSettingsModal(); true`);
    await until(`document.getElementById('tb-autowake')?.hidden === false && document.querySelector('input[name="terminal-backend"]')`, 'the wake and backend parts of the dialog');
    // The dialog fills its CLI path fields a moment after it opens; a Save before that drops them, which is not what this test is about.
    await until(`document.getElementById('settings-cli-path-claude').dataset.filled === '1'`, 'the dialog to finish filling');
  };
  const save = () => ev(`import(${MOD}+'modals.js').then(m=>m.applySettings()).then(()=>true)`);
  let original = null;
  try {
    await Page.enable();
    // The fake goes in before the page's own code runs.
    await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
      const inner = window.fetch;
      window.fetch = (url, opts = {}) => {
        const u = String(url), m = (opts.method || 'GET').toUpperCase();
        if (/\\/api\\/system\\/terminal-backend$/.test(u) && m === 'GET') return Promise.resolve(new Response(JSON.stringify({ saved: 'pty', running: 'pty', forced: false, choice: true, restart_required: false, can_restart: false, running_agents: 0, leftover_tmux_agents: 0 }), { status: 200, headers: { 'content-type': 'application/json' } }));
        return inner(url, opts);
      };
    })();` });
    const loaded = Page.loadEventFired();
    await Page.navigate({ url: BASE + '/' });
    await loaded;
    await until(`!!window.__coralStatic || document.readyState === 'complete'`, 'the page');
    original = await ev(`fetch('/api/settings').then(r=>r.json()).then(d=>({ on: d.settings.auto_wake_after_restart, cap: d.settings.auto_wake_cap }))`);

    // 1. Nothing changed: the one line. A fresh server has no stored settings, so the first Save writes the dialog's defaults: do that first.
    await openDialog();
    await save();
    await openDialog();
    await clearToasts();
    await save();
    assert.match(await lastToast(), /Nothing to save: no setting changed\./, 'an unchanged Save says there was nothing to save');

    // 2. Only the wake switch changed: it is saved, and the Save says saved.
    await openDialog();
    const wasOn = await ev(`document.getElementById('tb-autowake-on').checked`);
    await ev(`document.getElementById('tb-autowake-on').checked = ${!wasOn}; true`);
    await clearToasts();
    await save();
    assert.match(await lastToast(), /Settings saved/, 'a Save that changed only the wake setting says saved');
    assert.doesNotMatch(await lastToast(), /Nothing to save/);
    const stored = await ev(`fetch('/api/settings').then(r=>r.json()).then(d=>d.settings.auto_wake_after_restart)`);
    assert.equal(String(stored), String(!wasOn), 'the wake setting was saved');

    // 3. Only the terminal backend choice differs: the dialog counts it as a change (not applied here: that restarts Coral).
    await openDialog();
    const tb = `import(${MOD}+'terminal_backend.js')`;
    assert.equal(await ev(`${tb}.then(m => m.hasUnsavedBackendChange())`), false, 'nothing picked differently: no change');
    await ev(`(() => { const r = [...document.querySelectorAll('input[name="terminal-backend"]')].find(x => !x.checked); r.checked = true; return true; })()`);
    assert.equal(await ev(`${tb}.then(m => m.hasUnsavedBackendChange())`), true, 'a different backend picked is a change');
    console.log('PASS a Save with only a side change is not "nothing to save"');
  } finally {
    try {
      if (original) await ev(`fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(${JSON.stringify({ auto_wake_after_restart: original.on ?? '', auto_wake_cap: original.cap ?? '' })})}).then(r=>r.status)`);
    } catch { /* the page is gone */ }
    await client.close();
  }
})().catch(error => { console.error(error); process.exit(1); });
