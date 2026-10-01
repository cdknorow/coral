const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
if (/:8420(\/|$)/.test(BASE)) throw new Error('Use an isolated test server');

(async () => {
  const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
  const { Page, Runtime } = client;
  const ev = async expression => {
    const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
    return r.result.value;
  };
  try {
    await Page.enable();
    await Page.navigate({ url: BASE });
    await Page.loadEventFired();
    await ev(`(async()=>{const {state}=await import('/static/state.js');state.currentSession={type:'live',session_id:'artifact-preview-test'};await import('/static/changed_files.js');})()`);
    const uri = 'coral://artifacts/a7cfde2b2f1a96d3a9086c715ff5c04afbcb3823daa611dd642fb377e2b23f44';
    const result = await ev(`(async()=>{
      const real=window.fetch; window.__artifactReads=0;
      window.fetch=async (url,opts)=>{
        if(!String(url).includes('/api/artifacts/')) return real(url,opts);
        return new Response(new ReadableStream({start(){window.__artifactReads++}}),{status:200,headers:{'Content-Type':'application/zip','Content-Length':'6096917','Content-Disposition':'inline; filename="final-ember-accord-acceptance.zip"'}});
      };
      window.openFilePreview(${JSON.stringify(uri)}); await new Promise(r=>setTimeout(r,80));
      return {text:document.querySelector('#inline-preview-body')?.textContent||'',link:document.querySelector('#inline-preview-body a')?.getAttribute('download')||'',reads:window.__artifactReads};
    })()`);
    assert.match(result.text, /binary artifact is not rendered inline/);
    assert.equal(result.link, '');
    assert.equal(result.reads, 1);
    await ev(`window._closeInlinePreview()`);
    const markdown = await ev(`(async()=>{
      window.fetch=async()=>new Response('# Safe preview\\n\\nThis is markdown.',{status:200,headers:{'Content-Type':'text/markdown','Content-Length':'25','Content-Disposition':'inline; filename="report.md"'}});
      window.openFilePreview(${JSON.stringify(uri)}); await new Promise(r=>setTimeout(r,80));
      return {heading:document.querySelector('#inline-preview-body h1')?.textContent||'',raw:document.querySelector('#inline-preview-body')?.textContent||''};
    })()`);
    assert.equal(markdown.heading, 'Safe preview');
    assert.match(markdown.raw, /This is markdown/);
    await ev(`window._closeInlinePreview()`);
    console.log('PASS artifact preview rejects large binary safely without parsing');
  } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
