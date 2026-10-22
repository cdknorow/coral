// #3613: "Resume agents stopped by an API error" (auto_resume_after_api_error) is off by default.
// Both Settings loaders (modals.js and settings_core.js) show it unchecked when unset and as
// saved otherwise; Apply saves a boolean; the real Settings dialog has it; the server refuses
// anything but a boolean.
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
    const boxes=['settings-telemetry-enabled','settings-remote-access-enabled','settings-debug-input-log','settings-board-auto-attach','settings-auto-resume'];
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
  const checked = () => ev(`document.getElementById('settings-auto-resume').checked`);
  const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
  try {
    await Page.enable(); await Page.navigate({ url: BASE }); await Page.loadEventFired();
    // The real server first (before the page's fetch is replaced): only a boolean is taken.
    const put = body => ev(`fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:${JSON.stringify(JSON.stringify(body))}}).then(r=>r.status)`);
    assert.equal(await put({ auto_resume_after_api_error: 'yes' }), 400, 'the server refuses a non-boolean');
    assert.equal(await put({ auto_resume_after_api_error: true }), 200, 'the server takes true');
    const stored = await ev(`fetch('/api/settings').then(r=>r.json()).then(j=>(j.settings||j).auto_resume_after_api_error)`);
    assert.equal(stored, 'true', 'stored as "true"');
    assert.equal(await put({ auto_resume_after_api_error: false }), 200, 'and back off');

    for (const mod of ['modals.js', 'settings_core.js']) {
      for (const settings of [{}, { auto_resume_after_api_error: '' }, { auto_resume_after_api_error: 'false' }]) {
        await setup(settings);
        await ev(`import(${MOD}+'${mod}').then(m=>m.loadSettings())`);
        assert.equal(await checked(), false, `${mod}: off (${JSON.stringify(settings)})`);
      }
      await setup({ auto_resume_after_api_error: 'true' });
      await ev(`import(${MOD}+'${mod}').then(m=>m.loadSettings())`);
      assert.equal(await checked(), true, `${mod}: a saved true is on`);
    }

    // Apply on a Coral that never set it sends false; turning it on sends true.
    await setup({});
    await ev(`import(${MOD}+'modals.js').then(m=>m.loadSettings())`);
    await ev(`import(${MOD}+'modals.js').then(m=>m.applySettings())`);
    assert.equal(await ev(`window.__settingsPut[0].auto_resume_after_api_error`), false, 'saved as off');
    await ev(`(async()=>{document.getElementById('settings-auto-resume').checked=true;await import(${MOD}+'modals.js').then(m=>m.applySettings())})()`);
    assert.equal(await ev(`window.__settingsPut[1].auto_resume_after_api_error`), true, 'turned on');

    // The real dialog has the switch, off, with its label.
    await Page.navigate({ url: BASE }); await Page.loadEventFired();
    const row = await ev(`(async () => {
      const m = await import(${MOD}+'modals.js');
      await m.loadSettings();
      await m.showSettingsModal();
      const box = document.getElementById('settings-auto-resume');
      const label = document.querySelector('label[for="settings-auto-resume"]');
      return box ? { checked: box.checked, label: label && label.textContent.trim() } : 'missing';
    })()`);
    assert.deepEqual(row, { checked: false, label: 'Resume after API errors' }, 'the Settings dialog');

    console.log('PASS auto-resume setting: off by default, shown as saved, saved as a boolean, validated');
  } finally {
    await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
    await client.close();
  }
})().catch(error => { console.error(error); process.exit(1); });
