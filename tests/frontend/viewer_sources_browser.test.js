// Isolated real-browser coverage of four viewer sources and lazy explorer scope.
const assert=require('node:assert/strict');
const fs=require('node:fs');
const CDP=require('chrome-remote-interface');
const base=process.env.CORAL_URL;
if(!base||/:8420(\/|$)/.test(base))throw Error('Use an isolated server');
(async()=>{
 const c=await CDP({port:Number(process.env.CDP_PORT||9222)});
 const ev=async expression=>{const r=await c.Runtime.evaluate({expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 let imageRequests=[];
 c.Fetch.requestPaused(async ({requestId,request})=>{
  const u=new URL(request.url);imageRequests.push({filepath:u.searchParams.get('filepath'),session:u.searchParams.get('session_id'),raw:u.searchParams.get('raw')});
  const valid=u.searchParams.get('filepath')==='image.png'&&u.searchParams.get('session_id')==='a'&&u.searchParams.get('raw')==='1';
  await c.Fetch.fulfillRequest({requestId,responseCode:valid?200:404,responseHeaders:[{name:'Content-Type',value:valid?'image/png':'application/json'}],body:valid?'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=':Buffer.from(JSON.stringify({error:'File not found'})).toString('base64')});
 });
 const settle=()=>ev('new Promise(r=>setTimeout(r,80))');
 const click=async s=>{await ev(`document.querySelector(${JSON.stringify(s)}).click()`);await settle();};
 const tab=s=>click(`[data-files-source="${s}"]`);
 const tree=()=>ev(`document.querySelector('#file-explorer-view').textContent`);
 const shot=async name=>{if(!process.env.CORAL_SCREENSHOT_DIR)return;fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR,{recursive:true});const {data}=await c.Page.captureScreenshot({format:'png'});fs.writeFileSync(process.env.CORAL_SCREENSHOT_DIR+'/'+name+'.png',Buffer.from(data,'base64'));};
 try{
 await c.Page.enable();await c.Fetch.enable({patterns:[{urlPattern:'*/file-content?*',resourceType:'Image',requestStage:'Request'}]});await c.Emulation.setDeviceMetricsOverride({width:1100,height:850,deviceScaleFactor:1,mobile:false});
 await c.Page.navigate({url:base});await c.Page.loadEventFired();
 await ev(`(async()=>{
 const {state}=await import('/static/state.js');const f=await import('/static/changed_files.js');window.s=state;window.f=f;
 s.currentSession={type:'live',name:'UI',session_id:'a',board_project:'team',working_directory:'/repo-a/subdir'};
 const p=document.getElementById('agentic-panel-files');document.body.append(p);p.style.cssText='display:flex;position:fixed;top:20px;left:20px;width:680px;height:760px;z-index:99999;background:var(--bg-primary)';
 window.calls=[];window.pending=[];window.defer=false;window.locked=true;
 window.fetch=async url=>{
 const u=new URL(url,location.origin);calls.push(u.pathname+u.search);
 if(u.pathname.endsWith('/search-files')&&u.searchParams.get('view')==='explorer'){
 const sid=u.searchParams.get('session_id'),dir=u.searchParams.get('dir'),offset=Number(u.searchParams.get('offset'));
 if(dir==='locked'&&window.locked)return new Response('{}',{status:403});
 const names=dir==='.'?(offset?['later.txt']:['image.png','src/','.env','empty/','locked/']):dir==='src'?['main.go','nested/']:dir==='src/nested'?['worker.go']:[];
 const data={root:'/repo-'+sid,entries:names.map(n=>({name:n.replace(/\\/$/,''),path:(dir==='.'?'':dir+'/')+n.replace(/\\/$/,''),type:n.endsWith('/')?'dir':'file'})),has_more:dir==='.'&&!offset};
 const respond=()=>new Response(JSON.stringify(data));if(window.defer)return new Promise(r=>pending.push(()=>r(respond())));return respond();
 }
 if(u.pathname.endsWith('/artifacts')){const sid=u.searchParams.get('session_id');const respond=()=>new Response(JSON.stringify({project:'team',...(sid?{session_id:sid}:{}),artifacts:[{name:sid?'Personal '+sid:'Team shared',inline:true,available:true,media_type:'text/plain',task_id:1,content_url:'/api/board/team/tasks/1/artifact-content?source=completion&index=0'}]}));if(window.defer)return new Promise(r=>pending.push(()=>r(respond())));return respond();}
 if(u.pathname.endsWith('/file-content')){
 // Like GetFileContent, resolve within the repository root, not the agent's
 // subdirectory. Unknown paths (including a prepended absolute root) fail.
 const filepath=u.searchParams.get('filepath');
 const files={'src/main.go':'package main\\n// Main preview fixture','src/nested/worker.go':'package worker\\n// Nested preview fixture'};
 const content=Object.hasOwn(files,filepath)?files[filepath]:undefined;
 if(filepath?.split('/').includes('..'))return new Response(JSON.stringify({error:'Path traversal not allowed'}),{status:403});
 if(content===undefined)return new Response(JSON.stringify({error:'File not found'}),{status:404});
 return new Response(JSON.stringify({filepath,content,working_directory:'/repo-'+u.searchParams.get('session_id')}));
 }
 if(u.pathname.endsWith('/artifact-content'))return new Response('Artifact body');
 return new Response(JSON.stringify({files:[]}));
 };f.initFileSearch();f.syncFilesViewerSession();
 })()`);
 assert.deepEqual(await ev(`[...document.querySelectorAll('[data-files-source]')].map(x=>x.textContent)`),['Files','Browse','Artifacts','Team Artifacts']);
 assert.equal(await ev(`calls.filter(x=>x.includes('view=explorer')||x.includes('/artifacts?')).length`),0);
 await ev(`document.querySelector('[data-files-source=files]').focus();document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true}))`);await settle();assert.equal(await ev(`calls.filter(x=>x.includes('view=explorer')).length`),1);
 assert.equal(await ev(`document.querySelector('.explorer-item').dataset.directory`),'true');assert.match(await tree(),/\.env/);
 await click('[data-path="src"]');assert.equal(await ev(`calls.filter(x=>x.includes('view=explorer')).length`),2);
 await tab('files');await tab('browse');assert.ok(await ev(`!!document.querySelector('[data-path="src/main.go"]')`));
 assert.equal(await ev(`s.currentSession.working_directory`),'/repo-a/subdir');
 assert.equal(await ev(`document.querySelector('.explorer-root').textContent`),'/repo-a');
 // Explicitly check the fixture rejects the old bug and a cwd-relative guess.
 assert.deepEqual(await ev(`Promise.all(['/repo-a/src/main.go','subdir/src/main.go','missing.go','../escape.go'].map(async filepath=>{const r=await fetch('/api/sessions/live/UI/file-content?'+new URLSearchParams({session_id:'a',filepath}));return [r.status,(await r.json()).error]}))`),[[404,'File not found'],[404,'File not found'],[404,'File not found'],[403,'Path traversal not allowed']]);
 await click('[data-path="src/main.go"]');
 assert.match(await ev(`document.getElementById('inline-preview-body').textContent`),/Main preview fixture/,'Browse must render file content, not a File not found error');
 assert.equal(await ev(`new URL(calls.filter(x=>x.includes('/file-content?')).at(-1),location.origin).searchParams.get('filepath')`),'src/main.go');
 await ev('window._closeInlinePreview()');await settle();
 assert.ok(await ev(`!!document.querySelector('[data-path="src/main.go"]')`),'Back preserves expanded tree');
 assert.equal(await ev(`document.querySelector('[data-files-source=browse]').getAttribute('aria-selected')`),'true');
 await click('[data-path="src/nested"]');await click('[data-path="src/nested/worker.go"]');
 assert.match(await ev(`document.getElementById('inline-preview-body').textContent`),/Nested preview fixture/);
 assert.equal(await ev(`new URL(calls.filter(x=>x.includes('/file-content?')).at(-1),location.origin).searchParams.get('filepath')`),'src/nested/worker.go');
 await ev('window._closeInlinePreview()');await settle();assert.equal(await ev(`document.querySelector('[data-path="src/nested"]').getAttribute('aria-expanded')`),'true');
 await click('[data-path="image.png"]');
 assert.deepEqual(imageRequests.at(-1),{filepath:'image.png',session:'a',raw:'1'});
 assert.ok(await ev(`document.querySelector('#inline-preview-body img')?.naturalWidth>0`),'relative raw image URL must render');
 await ev('window._closeInlinePreview()');await settle();assert.ok(await ev(`!!document.querySelector('[data-path="src/nested/worker.go"]')`),'image Back preserves nested tree');
 await click('[data-path="empty"]');assert.match(await tree(),/Empty directory/);
 await click('[data-path="locked"]');assert.match(await tree(),/403/);
 await ev('window.locked=false');await click('[aria-label="Retry locked"]');assert.doesNotMatch(await tree(),/403/);
 await click('[aria-label="Load more root directory"]');assert.match(await tree(),/later.txt/);
 await ev(`document.querySelector('[data-path="src"]').focus();document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true}))`);
 assert.equal(await ev('document.activeElement.dataset.parent'),'src');await shot('browse-desktop');
 await tab('artifacts');assert.match(await ev(`document.querySelector('#team-artifacts-view').textContent`),/Personal a/);
 await click('.team-artifact-preview');assert.match(await ev(`document.getElementById('inline-preview-body').textContent`),/Artifact body/);await ev('window._artifactBack()');await settle();assert.equal(await ev(`document.querySelector('[data-files-source=artifacts]').getAttribute('aria-selected')`),'true');
 await tab('team-artifacts');assert.match(await ev(`document.querySelector('#team-artifacts-view').textContent`),/Team shared/);
 await tab('artifacts');assert.equal(await ev(`calls.filter(x=>x.includes('/artifacts?')).length`),2);
 await ev(`window.defer=true;document.querySelector('.team-artifacts-heading button').click()`);await settle();
 await ev(`window.defer=false;s.currentSession={...s.currentSession,session_id:'b',working_directory:'/repo-b/subdir'};f.syncFilesViewerSession();pending.splice(0).forEach(r=>r())`);await settle();
 assert.match(await ev(`document.querySelector('#team-artifacts-view').textContent`),/Personal b/);assert.doesNotMatch(await ev(`document.querySelector('#team-artifacts-view').textContent`),/Personal a/);
 await tab('team-artifacts');assert.equal(await ev(`calls.filter(x=>x.includes('/artifacts?')&&!x.includes('session_id')).length`),1,'same-team cache retained');
 await tab('browse');assert.doesNotMatch(await tree(),/main.go/);assert.match(await tree(),/repo-b/);
 await ev(`window.defer=true;document.querySelector('#file-explorer-view .explorer-heading button').click()`);await settle();await tab('files');
 await ev(`window.defer=false;s.currentSession={...s.currentSession,session_id:'c',working_directory:'/repo-c'};f.syncFilesViewerSession();pending.splice(0).forEach(r=>r())`);await settle();await tab('browse');assert.match(await tree(),/repo-c/);assert.doesNotMatch(await tree(),/repo-b/);
 await click('[data-path=src]');await click('[data-path="src/main.go"]');await ev(`s.currentSession={...s.currentSession,session_id:'d',working_directory:'/repo-d'};f.syncFilesViewerSession()`);await settle();assert.equal(await ev(`!!document.querySelector('.inline-preview-header')`),false);assert.match(await tree(),/repo-d/);
 await ev(`window.defer=true;document.querySelector('#file-explorer-view .explorer-heading button').click()`);await settle();await tab('files');await ev(`window.defer=false;pending.splice(0).forEach(r=>r())`);await settle();assert.equal(await ev(`document.querySelector('#file-explorer-view').hidden`),true);await tab('browse');assert.match(await tree(),/repo-d/);
 await c.Emulation.setDeviceMetricsOverride({width:390,height:844,deviceScaleFactor:1,mobile:true});
 await ev(`document.getElementById('agentic-panel-files').style.width='350px'`);
 assert.ok(await ev(`[...document.querySelectorAll('[data-files-source]')].every(x=>{const r=x.getBoundingClientRect();return r.left>=0&&r.right<=390})`));await shot('browse-mobile');
 console.log('PASS four viewer tabs: lazy tree, strict relative file API, rendered text/nested/image previews, Back/cache, empty/error/retry/pagination, keyboard, personal/team isolation, stale responses, mobile');
 }finally{await c.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
