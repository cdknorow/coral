// #3256: the "Log terminal input (debug)" toggle. Off by default; saving sends
// debug_input_log; a visible note says when the log is on (it records typed text,
// which may include secrets); CORAL_DEBUG_INPUT/--debug-input show it checked and
// disabled.
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
  const setup = (settings, log) => ev(`(() => {
    const boxes=['settings-telemetry-enabled','settings-remote-access-enabled','settings-debug-input-log'];
    document.body.innerHTML=boxes.map(id=>'<input type="checkbox" id="'+id+'">').join('')
      +'<input id="settings-renderer-select" value="xterm">'
      +'<div id="settings-remote-access-status"></div><div id="settings-debug-input-log-status" hidden></div>'
      +'<button id="settings-debug-input-log-delete" hidden>Delete the log</button><div id="settings-modal"></div>';
    window.__settingsPut=[]; window.__log=${JSON.stringify(log)}; window.__deleteStatus=200;
    window.fetch=async (url,opts={})=>{const u=String(url);
      if(u.endsWith('/api/settings')&&opts.method==='PUT'){const b=JSON.parse(opts.body);window.__settingsPut.push(b);window.__log.enabled=window.__log.forced===true||b.debug_input_log===true;if(window.__log.enabled)window.__log.file_exists=true;return new Response('{}');}
      if(u.endsWith('/api/system/debug-input-log')&&opts.method==='DELETE'){if(window.__deleteStatus!==200)return new Response(JSON.stringify({error:'the terminal input debug log is on: turn it off before deleting it'}),{status:window.__deleteStatus});window.__log.file_exists=false;return new Response(JSON.stringify({deleted:1,debug_input_log:window.__log}));}
      if(u.endsWith('/api/settings'))return new Response(JSON.stringify({settings:${JSON.stringify(settings)}}));
      if(u.includes('/api/system/privacy'))return new Response(JSON.stringify({telemetry_enabled:true,remote_access_enabled:false,remote_access_effective:false,debug_input_log:window.__log}));
      if(u.includes('/api/themes/'))return new Response(JSON.stringify({theme:{base:'dark',variables:{}}}));
      return new Response('{}');};
  })()`);
  const view = () => ev(`(()=>{const b=document.getElementById('settings-debug-input-log'),n=document.getElementById('settings-debug-input-log-status'),d=document.getElementById('settings-debug-input-log-delete');return {checked:b.checked,disabled:b.disabled,noteHidden:n.hidden,note:n.textContent,deleteHidden:d.hidden};})()`);
  try {
    await Page.enable(); await Page.navigate({ url: BASE }); await Page.loadEventFired();

    // Off by default: unchecked, no note.
    await setup({}, { enabled: false, forced: false, path: '/d/input-debug.log' });
    await ev(`import(${MOD}+'modals.js').then(m=>m.loadSettings())`);
    let v = await view();
    assert.equal(v.checked, false, 'off by default');
    assert.equal(v.noteHidden, true, 'no note while off');

    // Turning it on saves debug_input_log: true and shows the note at once.
    await ev(`(async()=>{document.getElementById('settings-debug-input-log').checked=true;await import(${MOD}+'modals.js').then(m=>m.applySettings())})()`);
    assert.equal(await ev(`window.__settingsPut[0].debug_input_log`), true);
    v = await view();
    assert.equal(v.noteHidden, false, 'the note shows once it is on');
    // #60: redacted, typing kept as its length, and still not free of text sent to agents.
    assert.match(v.note, /^On: .*\/d\/input-debug\.log, with the secrets Coral recognises removed and your own typing kept as its length only\. It can still hold board messages/);

    assert.equal(v.deleteHidden, true, 'no Delete while it is on');

    // Turning it off saves false; the file is kept, and the note says so with a Delete button.
    await ev(`(async()=>{document.getElementById('settings-debug-input-log').checked=false;await import(${MOD}+'modals.js').then(m=>m.applySettings())})()`);
    assert.equal(await ev(`window.__settingsPut[1].debug_input_log`), false);
    v = await view();
    assert.equal(v.noteHidden, false);
    assert.match(v.note, /^Off\. \/d\/input-debug\.log is kept \(its older copy was removed\): it can hold text sent to agents, so delete it when you no longer need it\. It is deleted after 7 days without a write\.$/);
    assert.equal(v.deleteHidden, false, 'Delete is offered for the kept file');

    // Delete removes it: the note and the button go away.
    await ev(`document.getElementById('settings-debug-input-log-delete').click()`);
    await ev(`(async()=>{const d=document.getElementById('settings-debug-input-log-delete');for(let i=0;i<200&&!d.hidden;i++)await new Promise(r=>setTimeout(r,5));})()`); // until the click's stubbed fetch has re-rendered
    v = await view();
    assert.equal(v.noteHidden, true);
    assert.equal(v.deleteHidden, true);

    // A refused delete (the log was turned on meanwhile) shows the server's reason.
    await setup({}, { enabled: false, forced: false, path: '/d/input-debug.log', file_exists: true });
    await ev(`import(${MOD}+'modals.js').then(m=>m.loadSettings())`);
    await ev(`window.__deleteStatus=409`);
    await ev(`import(${MOD}+'settings_core.js').then(m=>m.deleteDebugInputLog())`);
    assert.match((await view()).note, /turn it off before deleting it/);

    // A saved "True" (how booleans are stored) loads as checked, with the note.
    await setup({ debug_input_log: 'True' }, { enabled: true, forced: false, path: '/d/input-debug.log' });
    await ev(`import(${MOD}+'modals.js').then(m=>m.loadSettings())`);
    v = await view();
    assert.equal(v.checked, true);
    assert.equal(v.noteHidden, false);

    // Forced on by the environment: checked, disabled, and the note says why.
    await setup({}, { enabled: true, forced: true, path: '/d/input-debug.log' });
    await ev(`import(${MOD}+'modals.js').then(m=>m.loadSettings())`);
    v = await view();
    assert.equal(v.checked, true);
    assert.equal(v.disabled, true, 'the setting cannot turn off an env/flag-forced log');
    assert.match(v.note, /CORAL_DEBUG_INPUT or --debug-input/);
    // Applying Settings while forced keeps the SAVED value (off here): the forced state
    // must not be saved, or the log would stay on after the env/flag is removed.
    await ev(`import(${MOD}+'modals.js').then(m=>m.applySettings())`);
    assert.equal(await ev(`window.__settingsPut[0].debug_input_log`), false, 'forced on is not saved as the setting');

    console.log('PASS debug input log toggle: off by default, saved live, visible note, forced state');
  } finally { await client.close(); }
})().catch(error => { console.error(error); process.exit(1); });
