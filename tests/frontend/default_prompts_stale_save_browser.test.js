// The Default Prompts dialog opens with the prompts the server has NOW and Save writes only the prompts the person
// changed (#4423): saving one prompt never puts back the other three as this page last saw them.
//   - a prompt another client saved after this page loaded shows when the dialog opens;
//   - a prompt another client saves while the dialog is open survives a Save of a different prompt;
//   - a Save with nothing changed makes no request;
//   - a prompt set back to the shipped default is saved as empty (so future default updates are picked up).
// The shard's own prompts are put back at the end.
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
  const KEYS = ['default_prompt_orchestrator', 'default_prompt_worker', 'team_reminder_orchestrator', 'team_reminder_worker'];
  const FIELD = { default_prompt_orchestrator: 'settings-prompt-orchestrator', default_prompt_worker: 'settings-prompt-worker', team_reminder_orchestrator: 'settings-team-reminder-orchestrator', team_reminder_worker: 'settings-team-reminder-worker' };
  const otherClientPut = body => ev(`fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(${JSON.stringify(body)})}).then(r=>r.status)`);
  const server = () => ev(`fetch('/api/settings').then(r=>r.json()).then(d=>d.settings)`);
  const value = key => ev(`document.getElementById(${JSON.stringify(FIELD[key])}).value`);
  const edit = (key, text) => ev(`(()=>{const e=document.getElementById(${JSON.stringify(FIELD[key])});e.value=${JSON.stringify(text)};e.dispatchEvent(new Event('input',{bubbles:true}));return true;})()`);
  const open = () => ev(`import(${MOD}+'default_prompts_dialog.js').then(m=>m.showDefaultPromptsModal()).then(()=>true)`);
  const save = () => ev(`import(${MOD}+'default_prompts_dialog.js').then(m=>m.saveDefaultPrompts()).then(()=>true)`);
  let original = {};
  try {
    await Page.enable(); await Page.navigate({ url: BASE }); await Page.loadEventFired();
    await ev(`new Promise(r=>{const t=setInterval(()=>{if(window.__coralStatic||document.readyState==='complete'){clearInterval(t);r(true)}},50)})`);
    original = await server();
    const defaults = await ev(`fetch('/api/settings/default-prompts').then(r=>r.json())`);

    // 1. A prompt another client saved after this page loaded shows when the dialog opens.
    assert.equal(await otherClientPut({ default_prompt_worker: 'WORKER from another client' }), 200);
    await open();
    assert.equal(await value('default_prompt_worker'), 'WORKER from another client', 'the dialog shows the prompts as the server has them now');

    // 2. A prompt another client saves while the dialog is open survives a Save of a different prompt.
    assert.equal(await otherClientPut({ team_reminder_worker: 'REMINDER from another client', default_prompt_worker: 'WORKER changed again' }), 200);
    await edit('default_prompt_orchestrator', 'ORCHESTRATOR edited here');
    await save();
    let s = await server();
    assert.equal(s.default_prompt_orchestrator, 'ORCHESTRATOR edited here', 'the prompt the person changed is saved');
    assert.equal(s.default_prompt_worker, 'WORKER changed again', 'a prompt the dialog showed (and did not touch) is not written back over the newer one');
    assert.equal(s.team_reminder_worker, 'REMINDER from another client', 'a prompt saved meanwhile is kept');

    // 3. A Save with nothing changed makes no request.
    await ev(`(()=>{window.__puts=[];const f=window.__realFetch=window.fetch;window.fetch=(u,o)=>{if(String(u).endsWith('/api/settings')&&o&&o.method==='PUT')window.__puts.push(o.body);return f(u,o);};return true;})()`);
    await open();
    await save();
    assert.deepEqual(await ev(`window.__puts`), [], 'nothing changed: nothing is sent');

    // 4. A prompt set back to the shipped default is saved as empty.
    await open();
    await edit('default_prompt_worker', defaults.default_prompt_worker);
    await save();
    assert.deepEqual((await ev(`window.__puts`)).map(b => JSON.parse(b)), [{ default_prompt_worker: '' }], 'only the changed prompt is sent');
    s = await server();
    assert.equal(s.default_prompt_worker || '', '', 'the default is stored as empty');
    assert.equal(s.default_prompt_orchestrator, 'ORCHESTRATOR edited here', 'and the others are as they were');

    // 5. A refused save leaves the dialog open with what was typed.
    await open();
    await edit('team_reminder_orchestrator', 'REMINDER typed and refused');
    await ev(`(()=>{window.fetch=(u,o)=>String(u).endsWith('/api/settings')&&o&&o.method==='PUT'?Promise.resolve(new Response('{}',{status:500})):fetch_(u,o);return true;})()`.replace('fetch_', 'window.__realFetch'));
    await save();
    assert.equal(await ev(`document.getElementById('default-prompts-modal').style.display`), 'flex', 'a refused save keeps the dialog open');
    assert.equal(await value('team_reminder_orchestrator'), 'REMINDER typed and refused', 'with what was typed');
    console.log('PASS default prompts dialog opens fresh and saves only what changed');
  } finally {
    try {
      const back = {};
      for (const k of KEYS) back[k] = original[k] || '';
      await ev(`fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(${JSON.stringify(back)})}).then(r=>r.status)`);
    } catch { /* the page is gone */ }
    await client.close();
  }
})().catch(error => { console.error(error); process.exit(1); });
