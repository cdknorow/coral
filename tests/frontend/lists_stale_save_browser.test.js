// Saved personas, saved team templates and macros are each one list in one setting. A save used to write this page's copy
// of the list (loaded at start), so an item another client had added meanwhile was lost, and a macro deleted by position
// could delete the wrong one (#4423). Now a change is applied to the list the server has at that moment.
// The shard's own lists are put back at the end.
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
  const KEYS = ['saved_personas', 'saved_team_templates', 'macros'];
  const otherClientPut = body => ev(`fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(${JSON.stringify(body)})}).then(r=>r.status)`);
  const server = async key => JSON.parse((await ev(`fetch('/api/settings').then(r=>r.json()).then(d=>d.settings)`))[key] || '[]');
  let original = {};
  const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
  try {
    await Page.enable(); await Page.navigate({ url: BASE }); await Page.loadEventFired();
    await ev(`new Promise(r=>{const t=setInterval(()=>{if(window.__coralStatic||document.readyState==='complete'){clearInterval(t);r(true)}},50)})`);
    original = await ev(`fetch('/api/settings').then(r=>r.json()).then(d=>d.settings)`);

    // Personas: another client saves one after this page loaded; this page then saves its own.
    assert.equal(await otherClientPut({ saved_personas: JSON.stringify([{ name: 'other-persona', prompt: 'p', flags: '' }]) }), 200);
    await ev(`import(${MOD}+'persona_store.js').then(m=>m._upsertSavedPersonas([{name:'my-persona',prompt:'q',flags:''}])).then(()=>true)`);
    assert.deepEqual((await server('saved_personas')).map(p => p.name), ['other-persona', 'my-persona'], "a persona another client saved is kept");
    // Saving a persona of the same name replaces it, not adds a second.
    await ev(`import(${MOD}+'persona_store.js').then(m=>m._upsertSavedPersonas([{name:'my-persona',prompt:'changed',flags:''}])).then(()=>true)`);
    const personas = await server('saved_personas');
    assert.deepEqual(personas.map(p => p.name), ['other-persona', 'my-persona']);
    assert.equal(personas[1].prompt, 'changed');

    // Team templates, the same.
    assert.equal(await otherClientPut({ saved_team_templates: JSON.stringify([{ name: 'other-template', agents: [] }]) }), 200);
    await ev(`import(${MOD}+'persona_store.js').then(m=>m._upsertSavedTeamTemplates([{name:'my-template',agents:[]}])).then(()=>true)`);
    assert.deepEqual((await server('saved_team_templates')).map(t => t.name), ['other-template', 'my-template'], 'a template another client saved is kept');

    // Macros: adding keeps what another client added; deleting removes the macro clicked, not what is at its position now.
    assert.equal(await otherClientPut({ macros: JSON.stringify([{ label: 'A', command: 'a' }, { label: 'B', command: 'b' }]) }), 200);
    await ev(`(()=>{document.getElementById('macro-label-input').value='C';document.getElementById('macro-command-input').value='c';return true;})()`);
    await ev(`import(${MOD}+'controls.js').then(m=>m.addMacro()).then(()=>true)`);
    assert.deepEqual((await server('macros')).map(x => x.label), ['A', 'B', 'C'], 'a macro another client added is kept');
    // This page now holds [A, B, C]. Another client puts X first; this page deletes B by the position it sees (1).
    assert.equal(await otherClientPut({ macros: JSON.stringify([{ label: 'X', command: 'x' }, { label: 'A', command: 'a' }, { label: 'B', command: 'b' }, { label: 'C', command: 'c' }]) }), 200);
    await ev(`import(${MOD}+'controls.js').then(m=>m.deleteMacro(1)).then(()=>true)`);
    assert.deepEqual((await server('macros')).map(x => x.label), ['X', 'A', 'C'], 'the macro the person clicked is deleted, and the one added elsewhere kept');
    console.log('PASS saved personas, templates and macros are changed on the server list');
  } finally {
    try {
      const back = {};
      for (const k of KEYS) back[k] = original[k] || '';
      await ev(`fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(${JSON.stringify(back)})}).then(r=>r.status)`);
    } catch { /* the page is gone */ }
    await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
    await client.close();
  }
})().catch(error => { console.error(error); process.exit(1); });
