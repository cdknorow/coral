// Static current-source browser fixture; never starts Coral or writes files via its API.
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const http=require('node:http');
const CDP=require('chrome-remote-interface');
const root=path.resolve(__dirname,'../../coral-go/internal/server/frontend');
const comparison=process.env.CORAL_HTML_PREVIEW_FIXTURE || path.resolve(__dirname,'../design/coral-workspace-refresh/strong-comparison.html');
// Design explorations may not be checked into a clean checkout. Still exercise
// self-contained HTML/data images there; an explicit fixture path must exist.
const embeddedPixel='data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=';
const comparisonHTML=fs.existsSync(comparison) || process.env.CORAL_HTML_PREVIEW_FIXTURE
 ? fs.readFileSync(comparison,'utf8')
 : '<!doctype html><style>body{font:18px sans-serif}img{width:40px;height:40px}</style><h1>Five visible changes — comparison fixture</h1>'+Array.from({length:5},()=>'<img src="'+embeddedPixel+'">').join('');
const source=process.env.CORAL_PREVIEW_SOURCE; // Optional original source for fail-before verification.
const shots=process.env.CORAL_SCREENSHOT_DIR;
const html=`<!doctype html><html data-theme="dark"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/static/css/variables.css"><link rel="stylesheet" href="/static/css/base.css"><link rel="stylesheet" href="/static/css/agentic.css"><link rel="stylesheet" href="/static/css/mobile.css"><style>body{margin:0}#agentic-state{display:flex;width:100%;max-width:none;height:100dvh}#agentic-panel-files{display:flex;width:100%;height:100%;flex-direction:column}.inline-preview-body{flex:1;min-height:0}.inline-preview-cm{flex:1;min-height:0}.material-icons{font-size:11px}</style><script src="/static/vendor/marked.min.js"></script><script src="/static/vendor/purify.min.js"></script><script src="/static/vendor/codemirror/codemirror-bundle.js"></script><div id="agentic-state"><div id="agentic-panel-files" class="agentic-panel active"></div></div></html>`;
(async()=>{
 let forbiddenRequests=0,client;
 const server=http.createServer((req,res)=>{
  if(req.url.startsWith('/forbidden')){forbiddenRequests++;res.writeHead(404).end();return;}
  if(req.url==='/'){res.setHeader('Content-Type','text/html');res.end(html);return;}
  if(req.url==='/fixture-comparison'){res.setHeader('Content-Type','text/plain');res.end(comparisonHTML);return;}
  const pathname=new URL(req.url,'http://localhost').pathname;
  const file=path.resolve(root,'.'+pathname);
  if(!pathname.startsWith('/static/')||!file.startsWith(root+path.sep)){res.writeHead(404).end();return;}
  try{res.setHeader('Content-Type',file.endsWith('.css')?'text/css':file.endsWith('.png')?'image/png':'text/javascript');res.end(fs.readFileSync(source&&pathname==='/static/changed_files.js'?source:file));}catch{res.writeHead(404).end();}
 });
 await new Promise(r=>server.listen(0,'127.0.0.1',r));
 const timer=setTimeout(()=>{console.error('HTML preview regression exceeded 45s');process.exit(1)},45000);
 try{
  client=await CDP({port:Number(process.env.CDP_PORT||9222)});const c=client;
  const ev=async expression=>{const r=await c.Runtime.evaluate({expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
  const settle=()=>ev('new Promise(r=>setTimeout(r,80))');
  // Sandboxed srcdoc is an out-of-process iframe in Chromium. Inspect it
  // through CDP, never by granting the parent same-origin access.
  const frameEval=async expression=>{
   const {targetInfos}=await c.Target.getTargets();const target=targetInfos.find(t=>t.type==='iframe'&&t.url==='about:srcdoc');
   if(target){const frame=await CDP({port:Number(process.env.CDP_PORT||9222),target:target.targetId});try{const r=await frame.Runtime.evaluate({expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value}finally{await frame.close()}}
   const {frameTree}=await c.Page.getFrameTree();assert.ok(frameTree.childFrames?.length,'HTML rendered in child frame');const {executionContextId}=await c.Page.createIsolatedWorld({frameId:frameTree.childFrames[0].frame.id,worldName:'preview-inspection'});const r=await c.Runtime.evaluate({expression,contextId:executionContextId,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;
  };
  const shot=async name=>{if(!shots)return;fs.mkdirSync(shots,{recursive:true});fs.writeFileSync(path.join(shots,name+'.png'),Buffer.from((await c.Page.captureScreenshot({format:'png'})).data,'base64'))};
  await c.Page.enable();await c.Emulation.setDeviceMetricsOverride({width:1200,height:900,deviceScaleFactor:1,mobile:false});await c.Page.navigate({url:`http://127.0.0.1:${server.address().port}`});await c.Page.loadEventFired();
  await ev(`(async()=>{
   window.comparisonHTML=await (await fetch('/fixture-comparison')).text();
   window.s=(await import('/static/state.js')).state;window.f=await import('/static/changed_files.js');
   s.currentSession={type:'live',name:'fixture',session_id:'html-a',working_directory:'/fixture',board_project:'fixture'};
   window.frameMessages=0;window.parentCompromised=false;window.pending=[];window.defer=false;window.writes=[];
   addEventListener('message',e=>{if(e.data==='html-ran')frameMessages++});
   window.minimal='<style>body{font:20px sans-serif;background:#e8f0ff;padding:24px;color:#123}h1{color:#135}</style><h1>Rendered repository HTML</h1><img alt="embedded pixel" src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="><img src="/forbidden.png"><script>parent.postMessage("html-ran","*");parent.parentCompromised=true;<\/script>';
   window.fetch=async(url,options={})=>{if(options.method&&options.method!=='GET')writes.push(url);const u=new URL(url,location.origin);const fp=u.searchParams.get('filepath');if(u.pathname.endsWith('/file-content')||u.pathname.endsWith('/file-original')){const content=fp==='strong-comparison.html'?comparisonHTML:fp==='notes.md'?'# Markdown still renders':fp==='code.js'?'const answer = 42;':u.pathname.endsWith('/file-original')?'<h1>Original</h1>':minimal;const response=()=>new Response(JSON.stringify({content,filepath:fp,working_directory:'/fixture'}));if(defer&&u.pathname.endsWith('/file-content'))return new Promise(r=>pending.push(()=>r(response())));return response()}return new Response('{}')};
   f.initFileSearch();f.syncFilesViewerSession();
  })()`);
  for(const name of ['minimal.html','minimal.htm','UPPER.HTML']){
   await ev(`f.openFilePreview(${JSON.stringify(name)})`);await settle();
   assert.equal(await ev(`!!document.querySelector('#inline-preview-body iframe')`),true,name+' Preview must render a webpage, not highlighted source');
   assert.deepEqual(await ev(`(()=>{const f=document.querySelector('#inline-preview-body iframe');return {sandbox:f.getAttribute('sandbox'),referrer:f.referrerPolicy,isolated:f.contentDocument===null}})()`),{sandbox:'',referrer:'no-referrer',isolated:true});
   assert.match(await ev(`document.querySelector('iframe').srcdoc`),/default-src 'none'/);
   assert.equal(await frameEval(`document.querySelector('h1').textContent`),'Rendered repository HTML');
   assert.equal(await frameEval(`document.querySelector('img').naturalWidth`),1);
  }
  assert.equal(await ev('frameMessages'),0);assert.equal(await ev('parentCompromised'),false);assert.equal(forbiddenRequests,0);
  await ev(`window._switchMode('edit')`);assert.ok(await ev(`document.querySelector('#inline-preview-cm .cm-content').textContent.includes('<h1>Rendered repository HTML</h1>')`),'Edit retains HTML source');
  await ev(`window._switchMode('preview')`);await settle();assert.equal(await frameEval(`document.querySelector('h1').textContent`),'Rendered repository HTML');
  await ev(`window.savedCM=window.CoralCM;window.CoralCM=null;window._switchMode('diff')`);
  assert.ok(await ev(`document.querySelector('#inline-preview-body pre code').textContent.includes('<h1>Rendered repository HTML</h1>')`),'Diff fallback must remain source');assert.equal(await ev(`!!document.querySelector('#inline-preview-body iframe')`),false);
  await ev(`window.CoralCM=savedCM;window._closeInlinePreview();f.openFilePreview('notes.md')`);assert.equal(await ev(`document.querySelector('.notes-rendered h1').textContent`),'Markdown still renders');
  await ev(`f.openFilePreview('code.js')`);assert.equal(await ev(`document.querySelector('pre code').textContent`),'const answer = 42;');
  await ev(`f.openFilePreview('strong-comparison.html')`);await settle();
  assert.match(await frameEval(`document.querySelector('h1').textContent`),/Five visible changes/);
  assert.equal(await frameEval(`new Promise(resolve=>{const check=()=>{const imgs=[...document.images];if(imgs.length>=5&&imgs.every(x=>x.complete&&x.naturalWidth>0))resolve(true);else setTimeout(check,20)};check()})`),true,'Actual comparison embedded screenshots render');await shot('repository-html-desktop');
  await ev(`window._closeInlinePreview()`);assert.equal(await ev(`!!document.querySelector('#files-source-picker')`),true,'Back restores source tabs');
  await ev(`defer=true;f.openFilePreview('late.html')`);await ev(`defer=false;s.currentSession={...s.currentSession,session_id:'html-b'};f.syncFilesViewerSession();pending.splice(0).forEach(r=>r())`);await settle();assert.equal(await ev(`!!document.querySelector('#inline-preview-body iframe')`),false,'Stale response cannot resurrect preview after switch');
  await c.Emulation.setDeviceMetricsOverride({width:390,height:844,deviceScaleFactor:1,mobile:true});await ev(`f.openFilePreview('strong-comparison.html')`);await settle();assert.equal(await ev(`document.querySelector('.mobile-file-preview-overlay iframe').getAttribute('sandbox')`),'');assert.ok(await ev(`(()=>{const r=document.querySelector('.mobile-file-preview-overlay').getBoundingClientRect();return r.left>=0&&r.right<=innerWidth})()`));await shot('repository-html-mobile');await ev(`window._closeInlinePreview()`);assert.equal(await ev(`!!document.querySelector('.mobile-file-preview-overlay')`),false);
  assert.deepEqual(await ev('writes'),[]);assert.equal(forbiddenRequests,0);assert.equal(await ev('frameMessages'),0);
  console.log('PASS repository HTML/HTM/case, real comparison/data images, sandbox/CSP/no parent scripts or requests, Edit source, Preview return, Diff fallback source, Markdown/code, Back/stale session and mobile. No writes.');
 }finally{clearTimeout(timer);if(client)await client.close();server.closeAllConnections();await new Promise(r=>server.close(r));}
})().catch(e=>{console.error(e);process.exitCode=1});
