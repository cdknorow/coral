// #3563: "Attach files agents link on the board" (board_auto_attach) is off by default. A
// Coral that never set it shows the switch unchecked, and saving Settings then sends false;
// a saved "True" (how booleans are stored) shows it checked; turning it on saves true. Both
// Settings loaders (modals.js and settings_core.js) agree.
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
  const setup = settings => ev(`(() => {
    const boxes=['settings-telemetry-enabled','settings-remote-access-enabled','settings-debug-input-log','settings-board-auto-attach'];
    document.body.innerHTML=boxes.map(id=>'<input type="checkbox" id="'+id+'">').join('')
      +'<input id="settings-renderer-select" value="xterm">'
      +'<div id="settings-remote-access-status"></div><div id="settings-debug-input-log-status" hidden></div>'
      +'<button id="settings-debug-input-log-delete" hidden>Delete the log</button><div id="settings-modal"></div>';
    window.__settingsPut=[];
    window.fetch=async (url,opts={})=>{const u=String(url);
      if(u.endsWith('/api/settings')&&opts.method==='PUT'){window.__settingsPut.push(JSON.parse(opts.body));return new Response('{}');}
      if(u.endsWith('/api/settings'))return new Response(JSON.stringify({settings:${JSON.stringify(settings)}}));
      if(u.includes('/api/system/privacy'))return new Response(JSON.stringify({telemetry_enabled:true,remote_access_enabled:false,remote_access_effective:false,debug_input_log:{enabled:false,forced:false}}));
      if(u.includes('/api/themes/'))return new Response(JSON.stringify({theme:{base:'dark',variables:{}}}));
      return new Response('{}');};
  })()`);
  const checked = () => ev(`document.getElementById('settings-board-auto-attach').checked`);
  try {
    await Page.enable(); await Page.navigate({ url: BASE }); await Page.loadEventFired();

    for (const mod of ['modals.js', 'settings_core.js']) {
      // Never set: unchecked. An empty value counts as unset.
      for (const settings of [{}, { board_auto_attach: '' }]) {
        await setup(settings);
        await ev(`import(${MOD}+'${mod}').then(m=>m.loadSettings())`);
        assert.equal(await checked(), false, `${mod}: off when unset (${JSON.stringify(settings)})`);
      }
      // Saved on or off: shown as saved.
      await setup({ board_auto_attach: 'True' });
      await ev(`import(${MOD}+'${mod}').then(m=>m.loadSettings())`);
      assert.equal(await checked(), true, `${mod}: a saved True is on`);
      await setup({ board_auto_attach: 'False' });
      await ev(`import(${MOD}+'${mod}').then(m=>m.loadSettings())`);
      assert.equal(await checked(), false, `${mod}: a saved False is off`);
    }

    // Saving Settings on a Coral that never set it sends false, not the old default true.
    await setup({});
    await ev(`import(${MOD}+'modals.js').then(m=>m.loadSettings())`);
    await ev(`import(${MOD}+'modals.js').then(m=>m.applySettings())`);
    assert.equal(await ev(`window.__settingsPut[0].board_auto_attach`), false, 'saved as off');

    // Turning it on from the dashboard saves true.
    await ev(`(async()=>{document.getElementById('settings-board-auto-attach').checked=true;await import(${MOD}+'modals.js').then(m=>m.applySettings())})()`);
    assert.equal(await ev(`window.__settingsPut[1].board_auto_attach`), true, 'turned on');

    // Saving with no switch on the page (Settings not rendered) keeps the stored state: off
    // when unset, on when saved on.
    for (const [settings, want] of [[{}, false], [{ board_auto_attach: 'True' }, true]]) {
      await setup(settings);
      await ev(`import(${MOD}+'modals.js').then(m=>m.loadSettings())`);
      await ev(`document.getElementById('settings-board-auto-attach').remove()`);
      await ev(`import(${MOD}+'modals.js').then(m=>m.applySettings())`);
      assert.equal(await ev(`window.__settingsPut[0].board_auto_attach`), want, `saved without the switch: ${JSON.stringify(settings)}`);
    }

    // Opening the real Settings dialog (showSettingsModal) shows the same state. The stub is
    // installed BEFORE any page script runs, so the dashboard's own boot-time settings load gets
    // the same answer: before, that load (the real server, where the setting is off) could land
    // after the stubbed one and turn the switch back off (a flake on a loaded runner).
    for (const [settings, want] of [[{}, false], [{ board_auto_attach: '' }, false], [{ board_auto_attach: 'True' }, true]]) {
      const { identifier } = await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
        const real = window.fetch;
        window.fetch = (url, opts = {}) => String(url && url.url || url).endsWith('/api/settings') && ((opts && opts.method) || 'GET') === 'GET'
          ? Promise.resolve(new Response(JSON.stringify({ settings: ${JSON.stringify(settings)} }), { headers: { 'Content-Type': 'application/json' } }))
          : real(url, opts);
      })()` });
      try {
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        const got = await ev(`(async () => {
          const m = await import(${MOD}+'modals.js');
          await m.loadSettings();
          await m.showSettingsModal();
          const box = document.getElementById('settings-board-auto-attach');
          return box ? box.checked : 'missing';
        })()`);
        assert.equal(got, want, `the Settings dialog: ${JSON.stringify(settings)}`);
      } finally {
        await Page.removeScriptToEvaluateOnNewDocument({ identifier });
      }
    }

    console.log('PASS board auto-attach setting: off by default, shown as saved, saved from the dashboard');
  } finally { await client.close(); }
})().catch(error => { console.error(error); process.exit(1); });
