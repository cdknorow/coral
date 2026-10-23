// #6583: "Remind agents to use Coral artifacts" (nudge_claude_artifacts) is on by default. Both Settings loaders
// (modals.js and settings_core.js) show it checked when unset and as saved otherwise (the server
// stores "true"/"false"; older Corals stored a JSON false as "False"); Apply saves a boolean; the
// real Settings dialog has it; the server refuses anything but a boolean.
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
    const boxes=['settings-telemetry-enabled','settings-remote-access-enabled','settings-debug-input-log','settings-board-auto-attach','settings-auto-resume','settings-auto-goals','settings-nudge-claude-artifacts'];
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
  const checked = () => ev(`document.getElementById('settings-nudge-claude-artifacts').checked`);
  const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
  try {
    await Page.enable(); await Page.navigate({ url: BASE }); await Page.loadEventFired();
    // The real server first (before the page's fetch is replaced): only a boolean is taken.
    const put = body => ev(`fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:${JSON.stringify(JSON.stringify(body))}}).then(r=>r.status)`);
    assert.equal(await put({ nudge_claude_artifacts: 'sometimes' }), 400, 'the server refuses a non-boolean');
    assert.equal(await put({ nudge_claude_artifacts: false }), 200, 'the server takes false');
    const stored = await ev(`fetch('/api/settings').then(r=>r.json()).then(j=>(j.settings||j).nudge_claude_artifacts)`);
    assert.equal(String(stored).toLowerCase(), 'false', 'stored as false');
    assert.equal(await put({ nudge_claude_artifacts: true }), 200, 'and back on');

    for (const mod of ['modals.js', 'settings_core.js']) {
      for (const settings of [{}, { nudge_claude_artifacts: '' }, { nudge_claude_artifacts: 'true' }]) {
        await setup(settings);
        await ev(`import(${MOD}+'${mod}').then(m=>m.loadSettings())`);
        assert.equal(await checked(), true, `${mod}: on (${JSON.stringify(settings)})`);
      }
      for (const off of ['false', 'False']) {
        await setup({ nudge_claude_artifacts: off });
        await ev(`import(${MOD}+'${mod}').then(m=>m.loadSettings())`);
        assert.equal(await checked(), false, `${mod}: a saved ${off} is off`);
      }
    }

    // Apply on a Coral that never set it sends true; turning it off sends false.
    await setup({});
    await ev(`import(${MOD}+'modals.js').then(m=>m.loadSettings())`);
    await ev(`import(${MOD}+'modals.js').then(m=>m.applySettings())`);
    assert.equal(await ev(`window.__settingsPut[0].nudge_claude_artifacts`), true, 'saved as on');
    await ev(`(async()=>{document.getElementById('settings-nudge-claude-artifacts').checked=false;await import(${MOD}+'modals.js').then(m=>m.applySettings())})()`);
    assert.equal(await ev(`window.__settingsPut[1].nudge_claude_artifacts`), false, 'turned off');

    // The real dialog has the switch, off, with its label.
    await Page.navigate({ url: BASE }); await Page.loadEventFired();
    const row = await ev(`(async () => {
      const m = await import(${MOD}+'modals.js');
      await m.loadSettings();
      await m.showSettingsModal();
      const box = document.getElementById('settings-nudge-claude-artifacts');
      const label = document.querySelector('label[for="settings-nudge-claude-artifacts"]');
      return box ? { checked: box.checked, label: label && label.textContent.trim() } : 'missing';
    })()`);
    assert.deepEqual(row, { checked: true, label: "Remind agents to use Coral artifacts" }, 'the Settings dialog');

    console.log('PASS nudge-claude-artifacts setting: on by default, shown as saved, saved as a boolean, validated');
  } finally {
    await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
    await client.close();
  }
})().catch(error => { console.error(error); process.exit(1); });
