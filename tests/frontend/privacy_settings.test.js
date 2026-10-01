const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL;
if (!BASE || /:8420(\/|$)/.test(BASE)) throw new Error('isolated server required');

(async () => {
  const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
  const { Page, Runtime } = client;
  const ev = async expression => {
    const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
    return result.result.value;
  };
  try {
    await Page.enable(); await Page.navigate({ url: BASE }); await Page.loadEventFired();
    await ev(`(() => {
      const ids=['settings-theme','settings-renderer-select','settings-agent-type','settings-permission-mode','settings-working-dir','settings-fit-pane-width','settings-notify-needs-input','settings-check-updates','settings-show-scrollbars','settings-proxy-enabled','settings-proxy-enabled-codex','settings-file-search-mode','settings-file-search-limit','settings-git-diff-mode','settings-git-poll-interval','settings-telemetry-enabled','settings-remote-access-enabled'];
      document.body.innerHTML=ids.map(id=>id.includes('enabled')||id.includes('check')||id.includes('scrollbars')||id.includes('proxy')||id.includes('fit')||id.includes('notify')?'<input type="checkbox" id="'+id+'">':'<input id="'+id+'">').join('')+'<div id="settings-remote-access-status"></div><div id="settings-modal"></div>';
      window.__settingsPut=[]; window.__settingsStatus=200; window.fetch=async (url,opts={})=>{const u=String(url);if(u.endsWith('/api/settings')&&opts.method==='PUT'){if(window.__settingsStatus===200)window.__settingsPut.push(JSON.parse(opts.body));return new Response('{}',{status:window.__settingsStatus});}if(u.endsWith('/api/settings'))return new Response(JSON.stringify({settings:{telemetry_enabled:false,remote_access_enabled:false}}));if(u.includes('/api/system/privacy'))return new Response(JSON.stringify({telemetry_enabled:false,remote_access_enabled:false,remote_access_effective:true,remote_access_restart_required:true}));if(u.includes('/api/themes/'))return new Response(JSON.stringify({theme:{base:'dark',variables:{}}}));return new Response('{}');};
    })()`);
    await ev(`import('/static/modals.js').then(m=>m.loadSettings())`);
    assert.deepEqual(await ev(`({telemetry:document.getElementById('settings-telemetry-enabled').checked,remote:document.getElementById('settings-remote-access-enabled').checked,status:document.getElementById('settings-remote-access-status').textContent})`), { telemetry:false, remote:false, status:'Saved: disabled. Running now: remote access enabled; restart required to apply the saved value.' });
    await ev(`(async()=>{document.getElementById('settings-telemetry-enabled').checked=true;document.getElementById('settings-remote-access-enabled').checked=true;await import('/static/modals.js').then(m=>m.applySettings())})()`);
    const payload = await ev(`window.__settingsPut[0]`);
    assert.equal(payload.telemetry_enabled, true); assert.equal(payload.remote_access_enabled, true);
    await ev(`window.__settingsStatus=403`);
    await ev(`document.getElementById('settings-modal').style.display='flex'`);
    await ev(`import('/static/modals.js').then(m=>m.applySettings())`);
    assert.equal(await ev(`document.getElementById('settings-modal').style.display`), 'flex');
    console.log('PASS privacy settings load, save, effective-state notice');
  } finally { await client.close(); }
})().catch(error => { console.error(error); process.exit(1); });
