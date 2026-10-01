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
    const manifest = await ev(`(async()=>{
      const manifestUri='coral://artifacts/a7cfde2b2f1a96d3a9086c715ff5c04afbcb3823daa611dd642fb377e2b23f44';
      const childMd='coral://artifacts/b7cfde2b2f1a96d3a9086c715ff5c04afbcb3823daa611dd642fb377e2b23f44';
      const missing='coral://artifacts/c7cfde2b2f1a96d3a9086c715ff5c04afbcb3823daa611dd642fb377e2b23f44';
      const childImage='coral://artifacts/d7cfde2b2f1a96d3a9086c715ff5c04afbcb3823daa611dd642fb377e2b23f44';
      const payload=JSON.stringify([{name:'<unsafe>.md',uri:childMd,media_type:'text/markdown'},{name:'missing.txt',uri:missing,media_type:'text/plain'},{name:'journey.png',uri:childImage,media_type:'image/png'}]);
      window.__manifestFetches=0;
      window.fetch=async (url)=>{
        const u=String(url);
        window.__manifestFetches++;
        if(u.includes(manifestUri.slice(-64))) return new Response(payload,{status:200,headers:{'Content-Type':'application/json','Content-Disposition':'inline; filename="qa-manifest.json"'}});
        if(u.includes(childMd.slice(-64))) return new Response('# Child report',{status:200,headers:{'Content-Type':'text/markdown','Content-Disposition':'inline; filename="child.md"'}});
        return new Response('missing',{status:404});
      };
      window.openFilePreview(manifestUri); await new Promise(r=>setTimeout(r,80));
      const links=[...document.querySelectorAll('.artifact-manifest-download')];
      links.forEach(a=>a.addEventListener('click',e=>e.preventDefault(),{once:true}));
      links[0].click(); links[2].click();
      const list={names:[...document.querySelectorAll('.artifact-manifest-name')].map(e=>e.textContent),hasUnsafeMarkup:!!document.querySelector('.artifact-manifest-list img'),rawButton:!!document.querySelector('.artifact-manifest-raw'),downloadNames:links.map(e=>e.download),downloadHrefs:links.map(e=>e.getAttribute('href')),fetchesAfterDownloads:window.__manifestFetches};
      document.querySelector('.artifact-manifest-open').click(); await new Promise(r=>setTimeout(r,80));
      const child={heading:document.querySelector('#inline-preview-body h1')?.textContent||'',backButton:document.querySelector('.inline-preview-back')?.title||''};
      document.querySelector('.inline-preview-back').click(); await new Promise(r=>setTimeout(r,40));
      const returnedToManifest=!!document.querySelector('.artifact-manifest-list');
      document.querySelector('.artifact-manifest-raw').click(); await new Promise(r=>setTimeout(r,40));
      const rawView={hasRaw:document.querySelector('#inline-preview-body pre')?.textContent.includes('missing.txt')||false,returnButton:!!document.querySelector('.artifact-manifest-back')};
      document.querySelector('.artifact-manifest-back').click(); await new Promise(r=>setTimeout(r,20));
      window._closeInlinePreview(); window.openFilePreview(missing); await new Promise(r=>setTimeout(r,80));
      return {list,child,returnedToManifest,rawView,error:document.querySelector('#inline-preview-body')?.textContent||''};
    })()`);
    assert.deepEqual(manifest.list.names,['<unsafe>.md','missing.txt','journey.png']);
    assert.equal(manifest.list.hasUnsafeMarkup,false);
    assert.equal(manifest.list.rawButton,true);
    assert.deepEqual(manifest.list.downloadNames,['<unsafe>.md','missing.txt','journey.png']);
    assert.equal(manifest.list.downloadHrefs[2].startsWith('/api/artifacts/'),true);
    assert.equal(manifest.list.fetchesAfterDownloads,1);
    assert.equal(manifest.child.heading,'Child report');
    assert.equal(manifest.child.backButton,'Back');
    assert.equal(manifest.returnedToManifest,true);
    assert.equal(manifest.rawView.hasRaw,true);
    assert.equal(manifest.rawView.returnButton,true);
    assert.match(manifest.error,/Artifact unavailable/);
    await ev(`window._closeInlinePreview()`);
    const malformed = await ev(`(async()=>{
      window.fetch=async()=>new Response('[{"name":"bad","uri":"file:///tmp/nope"}]',{status:200,headers:{'Content-Type':'application/json','Content-Disposition':'inline; filename="unrelated.json"'}});
      window.openFilePreview(${JSON.stringify(uri)}); await new Promise(r=>setTimeout(r,80));
      return {manifest:!!document.querySelector('.artifact-manifest-list'),raw:document.querySelector('#inline-preview-body pre')?.textContent||''};
    })()`);
    assert.equal(malformed.manifest,false);
    assert.match(malformed.raw,/file:\/\/\/tmp\/nope/);
    console.log('PASS artifact preview safety and browsable manifest collection');
  } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
