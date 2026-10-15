const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const http = require('node:http');
const BASE = require('./test_base').BASE;

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
    await ev(`(async()=>{const {state}=await import((window.__coralStatic||'/static/')+'state.js');state.currentSession={type:'live',session_id:'artifact-preview-test'};await import((window.__coralStatic||'/static/')+'changed_files.js');})()`);
    // The preview is a tabbed pane (#preview-body); closing every tab is its reset.
    await ev(`(async()=>{const m=await import((window.__coralStatic||'/static/')+'preview_pane.js');window.__closePreview=()=>m.closeAllTabs();
      // Condition waits (#3118): resolve once cond() holds (or after 3 s, and the assertion that follows reports it).
      window.__until=(cond,ms=3000)=>new Promise(res=>{const end=Date.now()+ms;(function f(){let ok=false;try{ok=!!cond()}catch{}ok||Date.now()>end?res(ok):setTimeout(f,10)})()});})()`);
    const uri = 'coral://artifacts/a7cfde2b2f1a96d3a9086c715ff5c04afbcb3823daa611dd642fb377e2b23f44';
    const result = await ev(`(async()=>{
      const real=window.fetch; window.__artifactReads=0;
      window.fetch=async (url,opts)=>{
        if(!String(url).includes('/api/artifacts/')) return real(url,opts);
        return new Response(new ReadableStream({start(){window.__artifactReads++}}),{status:200,headers:{'Content-Type':'application/zip','Content-Length':'6096917','Content-Disposition':'inline; filename="final-ember-accord-acceptance.zip"'}});
      };
      window.openFilePreview(${JSON.stringify(uri)}); await window.__until(()=>/binary artifact/.test(document.querySelector('#preview-body')?.textContent||''));
      return {text:document.querySelector('#preview-body')?.textContent||'',link:document.querySelector('#preview-body a')?.getAttribute('download')||'',reads:window.__artifactReads};
    })()`);
    assert.match(result.text, /binary artifact is not rendered inline/);
    assert.equal(result.link, '');
    assert.equal(result.reads, 1);
    await ev(`window.__closePreview()`);
    const markdown = await ev(`(async()=>{
      window.fetch=async()=>new Response('# Safe preview\\n\\nThis is markdown.',{status:200,headers:{'Content-Type':'text/markdown','Content-Length':'25','Content-Disposition':'inline; filename="report.md"'}});
      window.openFilePreview(${JSON.stringify(uri)}); await window.__until(()=>document.querySelector('#preview-body h1'));
      return {heading:document.querySelector('#preview-body h1')?.textContent||'',raw:document.querySelector('#preview-body')?.textContent||''};
    })()`);
    assert.equal(markdown.heading, 'Safe preview');
    assert.match(markdown.raw, /This is markdown/);
    await ev(`window.__closePreview()`);
    const manifest = await ev(`(async()=>{
      const manifestUri='coral://artifacts/a7cfde2b2f1a96d3a9086c715ff5c04afbcb3823daa611dd642fb377e2b23f44';
      const childMd='coral://artifacts/b7cfde2b2f1a96d3a9086c715ff5c04afbcb3823daa611dd642fb377e2b23f44';
      const missing='coral://artifacts/c7cfde2b2f1a96d3a9086c715ff5c04afbcb3823daa611dd642fb377e2b23f44';
      const childImage='coral://artifacts/d7cfde2b2f1a96d3a9086c715ff5c04afbcb3823daa611dd642fb377e2b23f44';
      const payload=JSON.stringify([{name:'<unsafe>.md',uri:childMd,media_type:'text/markdown'},{name:'missing.txt',uri:missing,media_type:'text/plain'},{name:'journey.png',uri:childImage,media_type:'image/png'}]);
      window.__manifestFetches=0;
      window.fetch=async (url)=>{
        const u=String(url);
        // Count artifact reads only: a background poll landing here must not move the count.
        if(u.includes('/api/artifacts/')) window.__manifestFetches++;
        if(u.includes(manifestUri.slice(-64))) return new Response(payload,{status:200,headers:{'Content-Type':'application/json','Content-Disposition':'inline; filename="qa-manifest.json"'}});
        if(u.includes(childMd.slice(-64))) return new Response('# Child report',{status:200,headers:{'Content-Type':'text/markdown','Content-Disposition':'inline; filename="child.md"'}});
        return new Response('missing',{status:404});
      };
      window.openFilePreview(manifestUri); await window.__until(()=>document.querySelectorAll('.artifact-manifest-download').length===3);
      const links=[...document.querySelectorAll('.artifact-manifest-download')];
      links.forEach(a=>a.addEventListener('click',e=>e.preventDefault(),{once:true}));
      links[0].click(); links[2].click();
      const list={names:[...document.querySelectorAll('.artifact-manifest-name')].map(e=>e.textContent),hasUnsafeMarkup:!!document.querySelector('.artifact-manifest-list img'),rawButton:!!document.querySelector('.artifact-manifest-raw'),downloadNames:links.map(e=>e.download),downloadHrefs:links.map(e=>e.getAttribute('href')),fetchesAfterDownloads:window.__manifestFetches};
      // A tab opened by single click is a replaceable 'preview' tab; pin the collection (double-click) so the entry opens beside it.
      document.querySelector('.preview-tab').dispatchEvent(new MouseEvent('dblclick',{bubbles:true})); await new Promise(r=>setTimeout(r,40));
      document.querySelector('.artifact-manifest-open').click(); await window.__until(()=>document.querySelectorAll('.preview-tab').length>=2&&document.querySelector('#preview-body h1'));
      const tabsAfterOpen=document.querySelectorAll('.preview-tab').length;
      const child={heading:document.querySelector('#preview-body h1')?.textContent||'',backButton:'Back'};
      document.querySelector('.preview-tab').click(); await window.__until(()=>document.querySelector('.artifact-manifest-list'));
      const returnedToManifest=!!document.querySelector('.artifact-manifest-list');
      document.querySelector('.artifact-manifest-raw').click(); await window.__until(()=>document.querySelector('#preview-body pre'));
      const rawView={hasRaw:document.querySelector('#preview-body pre')?.textContent.includes('missing.txt')||false,returnButton:!!document.querySelector('.artifact-manifest-back')};
      document.querySelector('.artifact-manifest-back').click(); await new Promise(r=>setTimeout(r,20));
      window.__closePreview(); window.openFilePreview(missing); await window.__until(()=>/Artifact unavailable/.test(document.querySelector('#preview-body')?.textContent||''));
      return {list,child,tabsAfterOpen,returnedToManifest,rawView,error:document.querySelector('#preview-body')?.textContent||''};
    })()`);
    assert.deepEqual(manifest.list.names,['<unsafe>.md','missing.txt','journey.png']);
    assert.equal(manifest.list.hasUnsafeMarkup,false);
    assert.equal(manifest.list.rawButton,true);
    assert.deepEqual(manifest.list.downloadNames,['<unsafe>.md','missing.txt','journey.png']);
    assert.equal(manifest.list.downloadHrefs[2].startsWith('/api/artifacts/'),true);
    assert.equal(manifest.list.fetchesAfterDownloads,1);
    assert.equal(manifest.child.heading,'Child report');
    assert.equal(manifest.tabsAfterOpen,2,'the entry opens as its own tab and the collection stays open');
    assert.equal(manifest.returnedToManifest,true);
    assert.equal(manifest.rawView.hasRaw,true);
    assert.equal(manifest.rawView.returnButton,true);
    assert.match(manifest.error,/Artifact unavailable/);
    await ev(`window.__closePreview()`);
    const malformed = await ev(`(async()=>{
      window.fetch=async()=>new Response('[{"name":"bad","uri":"file:///tmp/nope"}]',{status:200,headers:{'Content-Type':'application/json','Content-Disposition':'inline; filename="unrelated.json"'}});
      window.openFilePreview(${JSON.stringify(uri)}); await window.__until(()=>document.querySelector('.artifact-manifest-list, #preview-body pre'));
      return {manifest:!!document.querySelector('.artifact-manifest-list'),raw:document.querySelector('#preview-body pre')?.textContent||''};
    })()`);
    assert.equal(malformed.manifest,false);
    assert.match(malformed.raw,/file:\/\/\/tmp\/nope/);

    // ── Untrusted artifact content never runs and never becomes live markup in the page ──────────────────────────────
    // ── The framed document is isolated: no referrer, no network, no messages, no reach into the page ────────────────
    // A counting server on loopback stands in for "anywhere on the network". Hostile HTML tries every way out it can.
    const hits = [];
    const sink = http.createServer((req, res) => { hits.push(req.method + ' ' + req.url); res.writeHead(200, { 'Content-Type': 'text/css' }); res.end('body{}'); });
    await new Promise(r => sink.listen(0, '127.0.0.1', r));
    const sinkUrl = 'http://127.0.0.1:' + sink.address().port;
    let iso;
    try {
      iso = await ev(`(async()=>{
        const wait=ms=>new Promise(r=>setTimeout(r,ms)); const sink=${JSON.stringify(sinkUrl)};
        // Positive control: the counting server really does see a request the page itself makes.
        // An XHR, not an image (the dashboard's CSP, img-src 'self' data: blob:, blocks remote images: #3515)
        // and not fetch (earlier sections stub it). A cross-origin GET is still sent; only its answer is withheld.
        await new Promise(res=>{const x=new XMLHttpRequest();x.onloadend=res;x.open('GET',sink+'/control');x.send();});
        window.__closePreview(); window.__messages=0; window.__canary='intact';
        window.addEventListener('message',()=>{window.__messages++});
        const hostileHtml='<h1>probe</h1>'
          +'<img src="'+sink+'/img"><link rel="stylesheet" href="'+sink+'/link.css"><style>@import url("'+sink+'/import.css"); body{background:url("'+sink+'/bg")}</style>'
          +'<iframe src="'+sink+'/frame"></iframe><meta http-equiv="refresh" content="0;url='+sink+'/refresh">'
          +'<form id=f action="'+sink+'/form" method="post"><input name=a value=b></form>'
          +'<script>fetch("'+sink+'/fetch");new Image().src="'+sink+'/imgjs";parent.postMessage("hostile","*");top.postMessage("hostile","*");parent.__canary="pwned";top.__canary="pwned";document.getElementById("f").submit();<\\/script>';
        window.fetch=async()=>new Response(hostileHtml,{status:200,headers:{'Content-Type':'text/html','Content-Disposition':'inline; filename="probe.html"'}});
        window.openFilePreview(${JSON.stringify(uri)}); await wait(1500); // sleep-ok: proves a negative, hostile content gets this long to reach out
        const frame=document.querySelector('#preview-body iframe');
        return {hasFrame:!!frame,referrer:frame?frame.referrerPolicy:null,messages:window.__messages,canary:window.__canary};
      })()`);
      await new Promise(r => setTimeout(r, 500)); // sleep-ok: late loads (proves a negative)
    } finally {
      // Drop open sockets first: Linux Chrome keeps one to the sink that never sent a request (a
      // preconnect), and close() alone waits for it until headersTimeout, over a minute on Linux.
      sink.closeAllConnections();
      await new Promise(r => sink.close(r));
    }
    await ev(`window.__closePreview()`);
    // Collected and reported together so each property shows up on its own when it is the one that breaks.
    const isoProblems = [];
    const controlSeen = hits.some(h => h.endsWith(' /control'));
    if (!controlSeen) isoProblems.push('positive control failed: the counting server saw no request from the page itself, so zero hits would prove nothing');
    for (let i = hits.length - 1; i >= 0; i--) if (hits[i].endsWith(' /control')) hits.splice(i, 1);
    if (!iso.hasFrame) isoProblems.push('no iframe rendered for the probe');
    if (iso.referrer !== 'no-referrer') isoProblems.push('iframe referrerPolicy is ' + JSON.stringify(iso.referrer) + ', want "no-referrer"');
    if (hits.length) isoProblems.push('requests left the frame: ' + hits.join(', '));
    if (iso.messages !== 0) isoProblems.push(iso.messages + ' postMessage event(s) reached the parent');
    if (iso.canary !== 'intact') isoProblems.push('parent window canary was changed to ' + JSON.stringify(iso.canary));
    assert.deepEqual(isoProblems, [], 'framed hostile HTML must be fully isolated');

    const hostile = await ev(`(async()=>{
      const out={}; const wait=ms=>new Promise(r=>setTimeout(r,ms)); const open=async()=>{window.openFilePreview(${JSON.stringify(uri)}); await wait(500);};
      const respond=(body,type,name,url)=>{window.fetch=async()=>{const r=new Response(body,{status:200,headers:{'Content-Type':type,'Content-Disposition':'inline; filename="'+name+'"'}}); if(url) Object.defineProperty(r,'url',{value:url}); return r;};};
      // 1) HTML: script and an onerror handler, trying to reach the parent
      window.__closePreview(); respond('<h1>Hi</h1><script>window.top.__pwnScript=1;parent.document.title="pwn"</script><img src=x onerror="window.top.__pwnImg=1">','text/html','x.html'); await open();
      const frame=document.querySelector('#preview-body iframe');
      out.html={hasFrame:!!frame,sandbox:frame?frame.getAttribute('sandbox'):null,tokens:frame?frame.sandbox.length:-1,contentDocument:frame?frame.contentDocument===null:false,
        srcdocHasCsp:!!frame&&/Content-Security-Policy[^>]*default-src 'none'/.test(frame.getAttribute('srcdoc')||''),scriptsInPage:document.querySelectorAll('#preview-body script').length,
        pwnScript:window.__pwnScript===undefined,pwnImg:window.__pwnImg===undefined,title:document.title!=='pwn',
        parentHasMarkup:!!document.querySelector('#preview-body > h1, #preview-body > img')};
      // 2) SVG: an image, never inline markup
      window.__closePreview(); respond('<svg xmlns="http://www.w3.org/2000/svg" onload="window.__pwnSvg=1"><script>window.__pwnSvg2=1</script><circle r="5"/></svg>','image/svg+xml','x.svg','data:image/svg+xml;charset=utf-8,'+encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" width="5" height="5"/>')); await open();
      out.svg={inlineSvg:document.querySelectorAll('#preview-body svg').length,imgs:document.querySelectorAll('#preview-body img').length,objects:document.querySelectorAll('#preview-body object, #preview-body embed').length,
        pwn:window.__pwnSvg===undefined&&window.__pwnSvg2===undefined};
      // 3) Markdown: sanitized
      window.__closePreview(); respond('# T\\n\\n<script>window.__pwnMd=1<\\/script><img src=x onerror="window.__pwnMd2=1"><a href="javascript:window.__pwnMd3=1">x</a>','text/markdown','x.md'); await open();
      out.md={scripts:document.querySelectorAll('#preview-body script').length,handlers:document.querySelectorAll('#preview-body [onerror], #preview-body [onload]').length,
        jsLinks:[...document.querySelectorAll('#preview-body a')].filter(a=>/^javascript:/i.test(a.getAttribute('href')||'')).length,
        pwn:window.__pwnMd===undefined&&window.__pwnMd2===undefined};
      // 4) A REPOSITORY .html file (not an artifact) takes a different code path in the preview pane: same guarantees
      window.__closePreview();
      const st=(await import((window.__coralStatic||'/static/')+'state.js')).state; st.currentSession={type:'live',name:'fixture',session_id:'repo-html',working_directory:'/fixture'};
      window.fetch=async()=>new Response(JSON.stringify({content:'<h1>Repo page</h1><script>window.top.__pwnRepo=1<\\/script><img src=x onerror="window.top.__pwnRepo2=1">',filepath:'x.html',working_directory:'/fixture'}),{status:200});
      // .htm and any letter case take the same rendered, sandboxed path (retired repository_html_preview suite's case check)
      const caseFrames={};
      for(const nm of ['x.htm','UPPER.HTML']){window.__closePreview();window.openFilePreview(nm);await wait(400);const cf=document.querySelector('#preview-body iframe');caseFrames[nm]=!!cf&&cf.getAttribute('sandbox')==='';}
      out.caseFrames=caseFrames;
      window.__closePreview();
      window.openFilePreview('x.html'); await wait(500);
      const rf=document.querySelector('#preview-body iframe');
      out.repo={hasFrame:!!rf,sandbox:rf?rf.getAttribute('sandbox'):null,isolated:rf?rf.contentDocument===null:false,csp:!!rf&&/default-src 'none'/.test(rf.getAttribute('srcdoc')||''),
        pwn:window.__pwnRepo===undefined&&window.__pwnRepo2===undefined,scriptsInPage:document.querySelectorAll('#preview-body script').length};
      window.__closePreview();
      return out;
    })()`);
    assert.equal(hostile.html.hasFrame, true, 'HTML artifacts render in an iframe');
    assert.equal(hostile.html.sandbox, '', 'the iframe sandbox attribute is empty: no allow-scripts, no allow-same-origin');
    assert.equal(hostile.html.tokens, 0);
    assert.equal(hostile.html.contentDocument, true, 'the sandboxed document is opaque to the page');
    assert.equal(hostile.html.srcdocHasCsp, true, "the framed document carries a default-src 'none' policy");
    assert.equal(hostile.html.scriptsInPage, 0);
    assert.equal(hostile.html.parentHasMarkup, false, 'artifact HTML is never inserted into the page itself');
    assert.deepEqual([hostile.html.pwnScript, hostile.html.pwnImg, hostile.html.title], [true, true, true], 'no script from the artifact ran');
    assert.equal(hostile.svg.inlineSvg, 0, 'SVG is never inline markup');
    assert.equal(hostile.svg.objects, 0);
    assert.ok(hostile.svg.imgs >= 1, 'SVG is shown through <img>');
    assert.equal(hostile.svg.pwn, true);
    assert.equal(hostile.repo.hasFrame, true, 'a repository .html file renders in an iframe');
    assert.deepEqual(hostile.caseFrames, { 'x.htm': true, 'UPPER.HTML': true }, '.htm and upper-case .HTML render in a sandboxed frame too');
    assert.equal(hostile.repo.sandbox, '', 'the repository HTML iframe sandbox attribute is empty');
    assert.equal(hostile.repo.isolated, true);
    assert.equal(hostile.repo.csp, true);
    assert.equal(hostile.repo.pwn, true, 'no script from a repository HTML file ran');
    assert.equal(hostile.repo.scriptsInPage, 0);
    assert.deepEqual([hostile.md.scripts, hostile.md.handlers, hostile.md.jsLinks], [0, 0, 0], 'markdown is sanitized');
    assert.equal(hostile.md.pwn, true);
    console.log('PASS artifact preview safety and browsable manifest collection');
  } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
