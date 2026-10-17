// A working-mode preset is saved with one PUT that replaces its name and instructions together. The Team settings form
// opened with both, so saving one used to put back the other as the form last saw it over a change another client had
// made meanwhile (#4423). Now Save reads the preset again and applies only what was changed in the form.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
// A team name unique to this run, so a reused server starts from a new team.
const TEAM = 'preset-stale-' + Date.now().toString(36);

(async () => {
  const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
  const { Page, Runtime } = client;
  const ev = async expression => {
    const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
    return result.result.value;
  };
  const { waitFor } = require('./helpers/wait').bind(ev);
  const URL = `/api/board/${encodeURIComponent(TEAM)}/working-mode/presets`;
  const formOpen = () => waitFor(`document.querySelector('.working-mode-form')?.dataset.initialState !== undefined`, 'the working-mode form to load');
  const statusSet = () => waitFor(`(t => t && !/saving|resetting|loading/i.test(t))(document.querySelector('.working-mode-status')?.textContent.trim())`, 'the status message');
  const serverPreset = id => ev(`fetch(${JSON.stringify(URL)}).then(r=>r.json()).then(d=>(d.presets||[]).find(p=>p.id===${JSON.stringify(id)})||null)`);
  // Another client: a PUT straight to the server.
  const otherClientSave = (id, name, instructions) => ev(`fetch(${JSON.stringify(URL + '/')}+encodeURIComponent(${JSON.stringify(id)}),{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({id:${JSON.stringify(id)},name:${JSON.stringify(name)},instructions:${JSON.stringify(instructions)}})}).then(r=>r.status)`);
  const field = (name, text) => ev(`(()=>{const e=document.querySelector('.working-mode-form').elements[${JSON.stringify(name)}];e.value=${JSON.stringify(text)};e.dispatchEvent(new Event('input',{bubbles:true}));return true;})()`);
  const saveClick = () => ev(`(()=>{document.querySelector('.working-mode-status').textContent='';document.querySelector('.preset-save').click();return true;})()`);
  const formValue = name => ev(`document.querySelector('.working-mode-form').elements[${JSON.stringify(name)}].value`);
  try {
    await Page.enable(); await Page.navigate({ url: BASE }); await Page.loadEventFired();
    await ev(`import((window.__coralStatic||'/static/')+'team_working_mode.js').then(m => { window.showTeamWorkingMode = m.showTeamWorkingMode; })`);
    await ev(`window.showTeamWorkingMode(${JSON.stringify(TEAM)})`);
    await formOpen();

    // A custom preset, made in the form.
    await ev(`document.querySelector('.preset-new').click()`);
    const id = await formValue('preset');
    assert.ok(id.startsWith('custom-'));
    await field('preset_name', 'Mine');
    await field('preset_instructions', 'original instructions');
    await saveClick(); await statusSet();
    assert.equal((await serverPreset(id)).instructions, 'original instructions');

    // Another client renames it; this (stale) form changes only the instructions.
    assert.equal(await otherClientSave(id, 'Renamed elsewhere', 'original instructions'), 200);
    await field('preset_instructions', 'instructions edited here');
    await saveClick(); await statusSet();
    let p = await serverPreset(id);
    assert.equal(p.name, 'Renamed elsewhere', 'a rename made meanwhile is not put back');
    assert.equal(p.instructions, 'instructions edited here', 'the edit made here is saved');
    assert.equal(await formValue('preset_name'), 'Renamed elsewhere', 'and the form shows what was saved');

    // Another client edits the instructions; this form changes only the name.
    assert.equal(await otherClientSave(id, 'Renamed elsewhere', 'instructions edited elsewhere'), 200);
    await field('preset_name', 'Renamed here');
    await saveClick(); await statusSet();
    p = await serverPreset(id);
    assert.equal(p.name, 'Renamed here', 'the rename made here is saved');
    assert.equal(p.instructions, 'instructions edited elsewhere', 'an instructions edit made meanwhile is not put back');

    // Both changed here: both are saved (nothing elsewhere changed).
    await field('preset_name', 'Both'); await field('preset_instructions', 'both edited here');
    await saveClick(); await statusSet();
    p = await serverPreset(id);
    assert.deepEqual([p.name, p.instructions], ['Both', 'both edited here']);
    console.log('PASS preset save applies only what the form changed');
  } finally {
    await client.close();
  }
})().catch(error => { console.error(error); process.exit(1); });
