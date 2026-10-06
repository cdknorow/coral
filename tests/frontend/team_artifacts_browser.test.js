// Focused browser regression for the optional Files / Team artifacts source.
// Run against an isolated Coral dev server and headless Chrome via CDP.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
if (/:8420(\/|$)/.test(BASE)) throw new Error('Use an isolated test server');
const SHOTS = process.env.CORAL_SCREENSHOT_DIR;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Input, Fetch } = client;
    let linkedRequests = 0;
    Fetch.requestPaused(async ({requestId}) => {
        linkedRequests++;
        const html = '<!doctype html><style>body{font:20px sans-serif;padding:32px;background:#eef5ff;color:#163455}</style><h1>Linked design guide</h1><p>This linked page is rendered inside Coral.</p><script>parent.postMessage("artifact-script-ran","*");document.body.dataset.scriptRan="yes"</script>';
        await Fetch.fulfillRequest({requestId,responseCode:200,responseHeaders:[{name:'Content-Type',value:'text/html'}],body:Buffer.from(html).toString('base64')});
    });
    const ev = async expression => {
        const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
        return result.result.value;
    };
    const settle = () => ev('new Promise(resolve => setTimeout(resolve, 100))');
    const click = selector => ev(`document.querySelector(${JSON.stringify(selector)}).click()`);
    const text = () => ev(`document.getElementById('team-artifacts-view')?.textContent || ''`);
    const shot = async name => {
        if (!SHOTS) return;
        fs.mkdirSync(SHOTS, { recursive: true });
        const { data } = await Page.captureScreenshot({ format: 'png', clip: { x: 20, y: 20, width: 680, height: 660, scale: 1 } });
        fs.writeFileSync(path.join(SHOTS, name + '.png'), Buffer.from(data, 'base64'));
    };
    try {
        await Page.enable();
        await client.Network.enable();
        await client.Network.setCacheDisabled({cacheDisabled:true});
        await Fetch.enable({patterns:[{urlPattern:'https://example.com/*',requestStage:'Request'}]});
        await client.Emulation.setDeviceMetricsOverride({width:1200,height:900,deviceScaleFactor:1,mobile:false});
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await ev(`(async () => {
            const {state} = await import('/static/state.js');
            const files = await import('/static/changed_files.js');
            window.__files = files; window.__state = state;
            state.currentSession = { type:'live', name:'test-ui', session_id:'ui-a', board_project:'design-team' };
            const panel = document.getElementById('agentic-panel-files');
            document.body.append(panel);
            panel.style.cssText = 'display:flex;position:fixed;top:20px;left:20px;width:680px;height:660px;z-index:99999;background:var(--bg-primary);border:1px solid var(--border)';
            window.__calls = []; window.__mode = 'normal'; window.__pending = []; window.__frameMessages=0;
            window.addEventListener('message',e=>{if(e.data==='artifact-script-ran')window.__frameMessages++});
            const real = window.fetch;
            const managed = 'a'.repeat(64);
            window.__items = [
                {id:managed, name:'Design review.md', media_type:'text/markdown',size:1800,task_id:2176,task_title:'Team artifact browser',created_at:'2026-10-02T15:00:00Z',uri:'coral://artifacts/'+managed,available:true,source:'completion',reference_count:2},
                {id:'b'.repeat(64),name:'b'.repeat(64),media_type:'image/png',size:125000,task_id:2176,task_title:'Desktop preview',created_at:'2026-10-02T15:05:00Z',uri:'/api/artifacts/'+'b'.repeat(64),available:true},
                {id:'inline',name:'Acceptance notes.txt',media_type:'text/plain',size:150,task_id:2176,inline:true,available:true,content_url:'/api/board/design-team/tasks/2176/artifact-content?source=completion&index=0'},
                {id:'missing',name:'Earlier screenshot.png',media_type:'image/png',task_id:2100,available:false},
                {id:'external',name:'Reference documentation',media_type:'text/html',task_id:2176,uri:'https://example.com/guide',available:true},
                {id:'unsafe',name:'<img src=x onerror=alert(1)>',task_id:2176,uri:'javascript:alert(1)',available:true},
            ];
            window.fetch = async (url, options) => {
                const u = String(url);
                if (u.includes('/api/board/') && u.includes('/artifacts?')) {
                    window.__calls.push(u);
                    const project = decodeURIComponent(u.split('/')[3]);
                    const respond = () => new Response(JSON.stringify({project, artifacts:window.__mode==='empty'?[]:u.includes('offset=100')?[]:window.__items, has_more:window.__mode==='paged' && u.includes('offset=0'), truncated:window.__mode==='paged'}), {headers:{'Content-Type':'application/json'}});
                    if(window.__mode==='deferred') return new Promise(resolve => window.__pending.push(() => resolve(respond())));
                    if(window.__mode==='error') return new Response('{}',{status:503});
                    return respond();
                }
                if(u.startsWith('/api/artifacts/') || u.includes('/artifact-content?')) {
                    window.__calls.push(u);
                    if(window.__previewFixture) return new Response(window.__previewFixture.content,{headers:{'Content-Type':window.__previewFixture.type,'Content-Disposition':window.__previewFixture.disposition || 'inline; filename="acceptance_report"',...(window.__previewFixture.size?{'Content-Length':String(window.__previewFixture.size)}:{})}});
                    if(window.__htmlContent) return new Response(window.__htmlContent,{headers:{'Content-Type':'text/plain'}});
                    if(u.includes('b'.repeat(64))) { const r = new Response('',{headers:{'Content-Type':'image/png'}}); Object.defineProperty(r,'url',{value:'data:image/svg+xml,%3Csvg xmlns=%22http://www.w3.org/2000/svg%22 width=%22200%22 height=%22100%22%3E%3Crect width=%22200%22 height=%22100%22 fill=%22teal%22/%3E%3C/svg%3E'}); return r; }
                    return new Response('# Design review\\n\\nThe team artifact browser preserves Files as the default view.',{headers:{'Content-Type':u.includes('artifact-content')?'text/plain':'text/markdown','Content-Disposition':'inline; filename="review.md"'}});
                }
                if(u.includes('/api/')) return new Response(JSON.stringify({files:[]}),{headers:{'Content-Type':'application/json'}});
                return real(url, options);
            };
            files.initFileSearch(); files.syncFilesViewerSession();
        })()`);
        assert.deepEqual(await ev('window.__calls'), [], 'Files default must not fetch artifacts');
        assert.equal(await ev(`document.querySelector('[data-files-source="files"]').getAttribute('aria-selected')`), 'true');
        // Native button keyboard interaction, not just programmatic click.
        await ev(`document.querySelector('[data-files-source="team-artifacts"]').focus()`);
        await Input.dispatchKeyEvent({ type:'keyDown', key:'Enter', code:'Enter', text:'\r', windowsVirtualKeyCode:13 });
        await Input.dispatchKeyEvent({ type:'keyUp', key:'Enter', code:'Enter', windowsVirtualKeyCode:13 });
        await settle();
        assert.equal((await ev('window.__calls')).length, 1, 'listing must not eagerly fetch blobs');
        assert.match(await text(), /Design review.md/);
        assert.match(await text(), /Desktop preview — artifact/);
        assert.doesNotMatch(await text(), /b{64}/);
        assert.match(await text(), /Missing/);
        assert.equal(await ev(`document.querySelectorAll('.team-artifact-name img').length`), 0, 'names are escaped');
        assert.equal(await ev(`document.querySelectorAll('#team-artifacts-view a[href^="javascript:"]').length`), 0);
        assert.equal(await ev(`document.querySelector('.team-artifact-row a').getAttribute('download')`), 'Design review.md');
        assert.equal(linkedRequests,0,'no external request before Preview');
        assert.equal(await ev(`document.querySelectorAll('.team-artifact-preview').length`),4);
        assert.deepEqual(await ev(`Array.from(document.querySelectorAll('.team-artifacts-list th'),n=>[n.textContent,n.scope])`), ['Name','Type','Size','Task','Created','Actions'].map(label=>[label,'col']));
        assert.equal(await ev(`document.querySelector('.team-artifact-col-type:is(td)').textContent`),'Markdown');
        await click('.team-artifact-details summary');
        assert.equal(await ev(`document.querySelector('.team-artifact-details').open`),true);
        assert.match(await ev(`document.querySelector('.team-artifact-details dl').textContent`),/MIMEtext\/markdown/);
        assert.match(await ev(`document.querySelector('.team-artifact-details dl').textContent`),/Task titleTeam artifact browser/);
        await click('.team-artifact-details summary');
        // Wide and narrow tables keep proper headers and readable secondary fields.
        await ev(`document.getElementById('agentic-panel-files').style.width='1000px'`);
        assert.notEqual(await ev(`getComputedStyle(document.querySelector('th.team-artifact-col-created')).display`),'none');
        await ev(`document.getElementById('agentic-panel-files').style.width='350px'`);
        assert.notEqual(await ev(`getComputedStyle(document.querySelector('th.team-artifact-col-created')).display`),'none');
        assert.ok(await ev(`document.querySelector('.team-artifacts-scroll').getBoundingClientRect().width<=350`));
        await ev(`document.querySelector('.team-artifacts-scroll').scrollLeft=1000`);
        assert.ok(await ev(`document.querySelector('.team-artifacts-scroll').scrollLeft>0`),'secondary columns are reachable');
        assert.ok(await ev(`{const action=document.querySelector('td.team-artifact-col-actions').getBoundingClientRect();const table=document.querySelector('.team-artifacts-scroll').getBoundingClientRect();action.left>=table.left && action.right<=table.right+1}`),'actions stay visible while table scrolls');
        await ev(`document.querySelector('.team-artifacts-scroll').scrollLeft=0;document.querySelector('.team-artifacts-scroll').focus()`);
        await Input.dispatchKeyEvent({type:'keyDown',key:'ArrowRight',code:'ArrowRight',windowsVirtualKeyCode:39});
        await Input.dispatchKeyEvent({type:'keyUp',key:'ArrowRight',code:'ArrowRight',windowsVirtualKeyCode:39});await settle();
        assert.ok(await ev(`document.querySelector('.team-artifacts-scroll').scrollLeft>0`),'table supports keyboard scrolling');
        await ev(`document.getElementById('agentic-panel-files').style.zoom='2';document.querySelector('.team-artifacts-scroll').scrollLeft=0`);
        assert.equal(await ev(`document.querySelectorAll('.team-artifacts-list th').length`),6,'200% zoom retains all columns');
        assert.ok(await ev(`document.querySelector('.team-artifacts-scroll').scrollWidth>document.querySelector('.team-artifacts-scroll').clientWidth`));
        await ev(`document.getElementById('agentic-panel-files').style.zoom=''`);
        await ev(`document.getElementById('agentic-panel-files').style.width='680px'`);
        await shot('team-artifacts-list');
        await click('.team-artifact-preview'); await settle();
        assert.match(await ev(`document.getElementById('inline-preview-body').textContent`), /preserves Files/);
        assert.equal(await ev(`document.querySelector('.inline-preview-filepath').textContent`), 'Design review.md');
        await shot('team-artifacts-preview');
        await ev('window._artifactBack()'); await settle();
        assert.equal(await ev(`document.querySelector('[data-files-source="team-artifacts"]').getAttribute('aria-selected')`), 'true');
        assert.equal((await ev(`window.__calls.filter(u=>u.includes('/artifacts?'))`)).length, 1, 'back reuses list');
        await click('.team-artifact-row:nth-child(3) .team-artifact-preview'); await settle();
        assert.match(await ev(`document.getElementById('inline-preview-body').textContent`), /preserves Files/);
        await ev('window._artifactBack()'); await settle();
        // Source tabs remain accessible inside previews. Keyboard movement
        // closes preview through its existing abort/generation path, then focuses
        // the replacement source tab (not a detached DOM node).
        await click('.team-artifact-preview'); await settle();
        assert.equal(await ev(`document.querySelectorAll('#preview-files-source-picker [role=tab]').length`),4);
        assert.ok(await ev(`Array.from(document.querySelectorAll('#preview-files-source-picker [role=tab]')).every(t=>!t.hasAttribute('aria-pressed') && document.getElementById(t.getAttribute('aria-controls')))`));
        assert.equal(await ev(`document.getElementById(document.querySelector('#inline-preview-body').getAttribute('aria-labelledby')).getAttribute('aria-selected')`),'true');
        assert.ok(await ev(`new Set(Array.from(document.querySelectorAll('[id]'),n=>n.id)).size===document.querySelectorAll('[id]').length`),'preview IDs are unique');
        await ev(`document.querySelector('#preview-files-source-team-artifacts').focus()`);
        await Input.dispatchKeyEvent({type:'keyDown',key:'Home',code:'Home',windowsVirtualKeyCode:36});
        assert.equal(await ev(`!!document.querySelector('.inline-preview-header')`),false);
        assert.equal(await ev(`document.activeElement.id`),'files-source-files');
        assert.equal(await ev(`document.activeElement.getAttribute('aria-selected')`),'true');
        await Input.dispatchKeyEvent({type:'keyDown',key:'End',code:'End',windowsVirtualKeyCode:35});await settle();
        assert.equal(await ev(`document.activeElement.id`),'files-source-team-artifacts');
        await click('.team-artifact-preview'); await settle();
        await click('#preview-files-source-team-artifacts');await settle();
        assert.equal(await ev(`!!document.querySelector('.inline-preview-header')`),false,'active source returns to its list');
        // A content response arriving after source navigation cannot resurrect a preview.
        await ev(`window.__normalFetch=window.fetch;window.fetch=(url,...args)=>String(url).startsWith('/api/artifacts/')?new Promise(resolve=>window.__lateArtifact=()=>resolve(new Response('STALE ARTIFACT',{headers:{'Content-Type':'text/plain'}}))):window.__normalFetch(url,...args)`);
        await click('.team-artifact-preview');await settle();
        await click('#preview-files-source-files');
        await ev(`window.__lateArtifact();window.fetch=window.__normalFetch`);await settle();
        assert.equal(await ev(`!!document.querySelector('.inline-preview-header')`),false);
        assert.doesNotMatch(await ev(`document.getElementById('agentic-panel-files').textContent`),/STALE ARTIFACT/);
        await click('#files-source-team-artifacts');await settle();
        // Image preview uses the existing image renderer.
        await click('.team-artifact-row:nth-child(2) .team-artifact-name'); await settle();
        assert.ok(await ev(`!!document.querySelector('#inline-preview-body img')`));
        await ev('window._artifactBack()'); await settle();
        // Linked preview uses a browser frame, never fetch() or a proxy.
        await click('.team-artifact-row:nth-child(5) .team-artifact-preview'); await settle();
        assert.equal(linkedRequests,1);
        assert.deepEqual(await ev(`(()=>{const f=document.querySelector('#inline-preview-body iframe');return {sandbox:f.getAttribute('sandbox'),referrer:f.referrerPolicy,originIsolated:f.contentDocument===null,title:f.title}})()`), {sandbox:'',referrer:'no-referrer',originIsolated:true,title:'Preview of Reference documentation'});
        assert.equal(await ev('window.__frameMessages'),0,'linked scripts cannot execute');
        assert.equal(await ev(`window.__calls.some(u=>u.startsWith('https://'))`),false,'no fetch proxy');
        assert.match(await ev(`document.getElementById('inline-preview-body').textContent`),/Some sites block embedding/);
        assert.equal(await ev(`document.querySelector('.inline-preview-header a').textContent`),'Open link');
        assert.equal(await ev(`document.querySelector('.inline-preview-header a').rel`),'noopener noreferrer');
        await shot('team-artifacts-linked-preview');
        await ev('window._artifactBack()'); await settle();
        assert.equal(await ev(`document.querySelectorAll('#agentic-panel-files iframe').length`),0);
        assert.equal(await ev(`document.querySelector('.team-artifact-row:nth-child(5) a').textContent`),'Open link');
        // Extensionless declared HTML must render, even when served text/plain.
        await ev(`window.__originalItems=window.__items;window.__htmlContent='<style>body{font:18px sans-serif;background:#edf7f3;padding:32px;color:#143c32}</style><h1>Logo system</h1><p>Rendered HTML artifact</p><script>parent.postMessage("artifact-script-ran","*")<\/script><img src="https://example.com/should-not-load.png">';window.__items=[{name:'logo-system',media_type:'text/html',task_id:2176,inline:true,available:true,content_url:'/api/board/design-team/tasks/2176/artifact-content?source=completion&index=0'}]`);
        await click('.team-artifacts-heading button'); await settle();
        await click('.team-artifact-preview'); await settle();
        assert.equal(await ev(`document.querySelector('#inline-preview-body iframe').getAttribute('sandbox')`),'');
        assert.match(await ev(`document.querySelector('#inline-preview-body iframe').srcdoc`),/default-src 'none'/);
        assert.match(await ev(`document.querySelector('#inline-preview-body iframe').srcdoc`),/Logo system/);
        assert.equal(await ev(`document.querySelector('#inline-preview-body iframe').contentDocument===null`),true);
        assert.equal(await ev('window.__frameMessages'),0,'HTML scripts cannot execute');
        assert.equal(linkedRequests,1,'HTML CSP prevents external image requests');
        assert.equal(await ev(`document.querySelector('.inline-preview-header a').textContent`),'Download');
        await shot('team-artifacts-html-preview');
        await ev('window._artifactBack()'); await settle();
        await ev(`window.__items=[{name:'managed-logo-system',media_type:'text/html',available:true,uri:'coral://artifacts/'+'c'.repeat(64)}]`);
        await click('.team-artifacts-heading button'); await settle();
        await click('.team-artifact-preview'); await settle();
        assert.match(await ev(`document.querySelector('#inline-preview-body iframe').srcdoc`),/Logo system/);
        assert.equal(await ev('window.__frameMessages'),0);
        assert.equal(linkedRequests,1);
        await ev('window._artifactBack()'); await settle();
        await ev(`window.__htmlContent=null;window.__items=window.__originalItems`);
        // Regression: extensionless acceptance_report must use declared or
        // response Markdown media type, not depend on a .md filename.
        const markdown = '# Acceptance report\n\n**Complete**\n\n- Preview rendered\n- Download retained\n';
        for (const [kind, type, item, rendered] of [
            ['managed', 'text/markdown', {name:'acceptance_report',available:true,uri:'coral://artifacts/'+'d'.repeat(64)}, true],
            ['inline', 'text/plain', {name:'acceptance_report',media_type:'text/markdown',task_id:2176,inline:true,available:true,content_url:'/api/board/design-team/tasks/2176/artifact-content?source=completion&index=0'}, true],
            ['plain', 'text/plain', {name:'acceptance_report',media_type:'text/plain',task_id:2176,inline:true,available:true,content_url:'/api/board/design-team/tasks/2176/artifact-content?source=completion&index=0'}, false],
        ]) {
            await ev(`window.__previewFixture=${JSON.stringify({content:markdown,type})};window.__items=[${JSON.stringify(item)}]`);
            await click('.team-artifacts-heading button'); await settle();
            await click('.team-artifact-preview'); await settle();
            const result = await ev(`(()=>{const body=document.getElementById('inline-preview-body');return {heading:body.querySelector('h1')?.textContent||'',strong:body.querySelector('strong')?.textContent||'',items:[...body.querySelectorAll('li')].map(x=>x.textContent),text:body.textContent}})()`);
            if (rendered) {
                assert.equal(result.heading,'Acceptance report',kind+' extensionless Markdown heading');
                assert.equal(result.strong,'Complete',kind+' extensionless Markdown emphasis');
                assert.deepEqual(result.items,['Preview rendered','Download retained'],kind+' extensionless Markdown list');
            } else {
                assert.equal(result.heading,'','plain text must not render Markdown');
                assert.equal(result.strong,'');
                assert.deepEqual(result.items,[]);
                assert.match(result.text,/# Acceptance report/);
                assert.match(result.text,/\*\*Complete\*\*/);
                assert.match(result.text,/- Preview rendered/);
            }
            assert.equal(await ev(`document.querySelector('.inline-preview-filepath').textContent`),'acceptance_report');
            assert.equal(await ev(`document.querySelector('.inline-preview-header a').textContent`),'Download');
            await shot('extensionless-'+kind+'-preview');
            await ev('window._artifactBack()'); await settle();
        }
        // A display label is not the original response filename. Keep both
        // hints: old uploads can be octet-stream despite an original .md name.
        for (const [kind, disposition, size, expected] of [
            ['quoted-markdown', 'inline; filename="media-latency-2026-10-05.md"', 6572, 'markdown'],
            ['unquoted-markdown', 'inline; filename=audit.MDOWN', 0, 'markdown'],
            ['encoded-markdown', "inline; filename=fallback.bin; filename*=UTF-8''audit%20notes.markdown", 0, 'markdown'],
            ['unknown-binary', 'inline; filename="audit.bin"', 0, 'binary'],
            ['no-filename', 'inline', 0, 'binary'],
            ['oversize-markdown', 'inline; filename="audit.md"', 3 * 1024 * 1024, 'limited'],
        ]) {
            const label='media-latency-audit-2026-10-05';
            await ev(`window.__previewFixture=${JSON.stringify({content:'# Image delivery latency, 2026-10-05\n\n- Preview rendered\n- Download retained\n',type:'application/octet-stream',disposition,size})};window.__items=[{name:${JSON.stringify(label)},media_type:'application/octet-stream',available:true,uri:'coral://artifacts/f8740cb4db8190d1ea14f5a7c9442d61c6ec92452dde8fb5ad825054ebce8474'}]`);
            await click('.team-artifacts-heading button'); await settle();
            await click('.team-artifact-preview'); await settle();
            const view=await ev(`(()=>{const b=document.getElementById('inline-preview-body');return {text:b.textContent,heading:b.querySelector('h1')?.textContent,items:[...b.querySelectorAll('li')].map(x=>x.textContent)}})()`);
            if(expected==='markdown') {
                assert.equal(view.heading,'Image delivery latency, 2026-10-05',kind+' must use response filename independently of extensionless label');
                assert.deepEqual(view.items,['Preview rendered','Download retained']);
            } else {
                assert.equal(view.heading,undefined);
                assert.match(view.text,expected==='binary'?/binary artifact is not rendered inline/:/Preview is limited/);
            }
            assert.equal(await ev(`document.querySelector('.inline-preview-filepath').textContent`),label,'display label is preserved');
            assert.equal(await ev(`document.querySelector('.inline-preview-header a').getAttribute('download')`),label);
            if(kind==='quoted-markdown')await shot('octet-stream-markdown-filename');
            await ev('window._artifactBack()');await settle();
        }
        await ev(`window.__previewFixture=null;window.__items=window.__originalItems`);
        // Empty, error and retry states.
        await ev(`window.__mode='empty'`); await click('.team-artifacts-heading button'); await settle();
        assert.match(await text(), /No artifacts shared/);
        await ev(`window.__mode='error'`); await click('.team-artifacts-heading button'); await settle();
        assert.match(await text(), /Unable to load team artifacts \(503\)/);
        await ev(`window.__mode='normal'`); await click('#team-artifacts-view > button'); await settle();
        assert.match(await text(), /Design review/);
        // Pagination appends metadata only and reports bounded server scans.
        await ev(`window.__mode='paged'`); await click('.team-artifacts-heading button'); await settle();
        assert.match(await text(), /Some older artifacts/);
        await click('.team-artifacts-more'); await settle();
        assert.ok((await ev('window.__calls')).some(u => u.endsWith('offset=6')));
        assert.equal(await ev(`document.querySelectorAll('.team-artifact-row').length`), 12);
        // A team switch during an open preview clears the previous team's content.
        await click('.team-artifact-row:nth-child(5) .team-artifact-preview'); await settle();
        await ev(`window.__mode='empty'; window.__state.currentSession.board_project='preview-next'; window.__files.syncFilesViewerSession()`); await settle();
        assert.equal(await ev(`!!document.getElementById('inline-preview-body')`), false);
        assert.match(await text(), /preview-next/);
        // Simulate a server ignoring AbortSignal; delayed old-team responses
        // still cannot populate a newly selected team's panel.
        await ev(`window.__mode='deferred'; window.__state.currentSession.board_project='old-team'; window.__files.syncFilesViewerSession()`);
        assert.match(await text(), /Loading team artifacts/);
        await ev(`window.__state.currentSession.board_project='new-team'; window.__files.syncFilesViewerSession(); window.__pending[0]()`);
        await settle();
        assert.doesNotMatch(await text(), /Design review/);
        assert.match(await text(), /new-team/);
        await ev(`window.__items=[]; window.__pending[1]()`); await settle();
        assert.match(await text(), /No artifacts shared/);
        // Switching to Files cancels lazy work and remains the selected source.
        await click('[data-files-source="files"]');
        const count = (await ev('window.__calls')).length;
        await ev(`window.__state.currentSession.board_project='third-team'; window.__files.syncFilesViewerSession()`); await settle();
        assert.equal((await ev('window.__calls')).length, count);
        await ev(`window.__state.currentSession.board_project=null; window.__files.syncFilesViewerSession()`);
        await click('[data-files-source="team-artifacts"]'); await settle();
        assert.match(await text(), /Select an agent on a team/);
        assert.equal((await ev('window.__calls')).length, count);
        // The same controls and existing preview overlay work on narrow screens.
        await client.Emulation.setDeviceMetricsOverride({width:390,height:844,deviceScaleFactor:1,mobile:true});
        await ev(`document.getElementById('agentic-panel-files').style.width='350px'; window.__mode='normal'; window.__items=[{name:'Mobile design guide',media_type:'text/html',available:true,uri:'https://example.com/mobile'}]; window.__state.currentSession.board_project='mobile-team'; window.__files.syncFilesViewerSession()`); await settle();
        if (SHOTS) { const {data} = await Page.captureScreenshot({format:'png'}); fs.writeFileSync(path.join(SHOTS,'team-artifacts-mobile.png'), Buffer.from(data,'base64')); }
        await click('.team-artifact-name'); await settle();
        assert.ok(await ev(`!!document.querySelector('.mobile-file-preview-overlay')`));
        assert.match(await ev(`document.querySelector('.mobile-file-preview-overlay').textContent`), /Mobile design guide/);
        assert.equal(await ev(`document.querySelector('.mobile-file-preview-overlay iframe').getAttribute('sandbox')`),'');
        assert.ok(await ev(`Array.from(document.querySelectorAll('#preview-files-source-picker button')).every(b=>b.getBoundingClientRect().height>=44)`),'mobile tab touch targets');
        assert.ok(await ev(`new Set(Array.from(document.querySelectorAll('[id]'),n=>n.id)).size===document.querySelectorAll('[id]').length`),'mobile preview IDs are unique');
        await ev('window._artifactBack()'); await settle();
        assert.equal(await ev(`!!document.querySelector('.mobile-file-preview-overlay')`), false);
        await click('.team-artifact-name'); await settle();
        await ev(`window.toggleAgenticPanel=(open)=>window.__openedFilesPanel=open;window.switchAgenticTab=(tab)=>window.__openedTab=tab`);
        await click('#preview-files-source-files');await settle();
        assert.equal(await ev(`!!document.querySelector('.mobile-file-preview-overlay')`),false);
        assert.equal(await ev(`window.__openedFilesPanel`),true);
        assert.equal(await ev(`window.__openedTab`),'files');
        assert.equal(await ev(`document.activeElement.id`),'files-source-files');
        console.log('PASS: semantic artifact table/responsive details, preview source navigation/keyboard/late abort/mobile, explicit managed/inline/linked Preview, retained actions, sandbox isolation, extensionless HTML/CSP and Markdown/plain text, lazy loading, back/team/mobile, errors/pagination/stale responses');
    } finally { await client.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
