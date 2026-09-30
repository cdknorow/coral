const assert = require('node:assert/strict');
const fs = require('node:fs');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL;
if (!BASE || /:8420(\/|$)/.test(BASE)) throw new Error('isolated server required');
(async () => {
  const client = await CDP({ port: Number(process.env.CDP_PORT) });
  const { Page, Runtime } = client;
  const ev = async expression => {
    const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
    return result.result.value;
  };
  try {
    await Page.enable(); await Page.navigate({ url: BASE }); await Page.loadEventFired();
    await ev(`import('/static/modals.js')`);
    await ev(`(() => { document.body.innerHTML='<main><div id="fixture"></div></main>'; window.fetch=async url => { const u=String(url); if(u.includes('/api/settings')) return new Response(JSON.stringify({settings:{default_model_claude:'claude-opus-5',default_model_codex:'gpt-6-sol'}})); if(u.includes('/api/agent-models')) return new Response(JSON.stringify({claude:['claude-opus-5'],codex:['gpt-6-sol'],agy:[],pi:[]})); return new Response('{}'); }; window._invalidateDefaultModels?.(); })()`);
    const change = async (value, type) => ev(`(async()=>{ window.renderAgentConfigForm('fixture',{value:{agentType:${JSON.stringify(type)},model:${JSON.stringify(value)}}}); await new Promise(r=>setTimeout(r,120)); const s=document.querySelector('.acf-agent-type'); s.value=${JSON.stringify(type === 'claude' ? 'codex' : 'claude')}; s.dispatchEvent(new Event('change',{bubbles:true})); await new Promise(r=>setTimeout(r,120)); return {type:s.value,model:document.querySelector('.acf-model').value,dirty:document.getElementById('fixture').dataset.acfModelDirty}; })()`);
    const claudeToCodex = await change('claude-opus-5','claude');
    assert.deepEqual(claudeToCodex,{type:'codex',model:'gpt-6-sol',dirty:'false'});
    const codexToClaude = await change('gpt-6-sol','codex');
    assert.deepEqual(codexToClaude,{type:'claude',model:'claude-opus-5',dirty:'false'});
    const unchanged = await ev(`(async()=>{window.renderAgentConfigForm('fixture',{value:{agentType:'codex',model:'gpt-6-sol'}});await new Promise(r=>setTimeout(r,120));document.querySelector('.acf-agent-type').dispatchEvent(new Event('change',{bubbles:true}));await new Promise(r=>setTimeout(r,120));return {type:document.querySelector('.acf-agent-type').value,model:document.querySelector('.acf-model').value};})()`);
    assert.deepEqual(unchanged,{type:'codex',model:'gpt-6-sol'});
    const custom = await change('my-custom-codex-model','claude');
    assert.deepEqual(custom,{type:'codex',model:'my-custom-codex-model',dirty:'true'});
    fs.writeFileSync('/tmp/coral-1492-provider-switch.png', Buffer.from((await Page.captureScreenshot()).data,'base64'));
    console.log(JSON.stringify({claudeToCodex,codexToClaude,unchanged,custom}));
  } finally { await client.close(); }
})().catch(error => { console.error(error); process.exit(1); });
