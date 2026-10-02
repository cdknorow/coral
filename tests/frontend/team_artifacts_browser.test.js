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
    const { Page, Runtime, Input } = client;
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
            window.__calls = []; window.__mode = 'normal'; window.__pending = [];
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
                    if(u.includes('b'.repeat(64))) { const r = new Response('',{headers:{'Content-Type':'image/png'}}); Object.defineProperty(r,'url',{value:'data:image/svg+xml,%3Csvg xmlns=%22http://www.w3.org/2000/svg%22 width=%22200%22 height=%22100%22%3E%3Crect width=%22200%22 height=%22100%22 fill=%22teal%22/%3E%3C/svg%3E'}); return r; }
                    return new Response('# Design review\\n\\nThe team artifact browser preserves Files as the default view.',{headers:{'Content-Type':u.includes('artifact-content')?'text/plain':'text/markdown','Content-Disposition':'inline; filename="review.md"'}});
                }
                if(u.includes('/api/')) return new Response(JSON.stringify({files:[]}),{headers:{'Content-Type':'application/json'}});
                return real(url, options);
            };
            files.initFileSearch(); files.syncFilesViewerSession();
        })()`);
        assert.deepEqual(await ev('window.__calls'), [], 'Files default must not fetch artifacts');
        assert.equal(await ev(`document.querySelector('[data-files-source="files"]').getAttribute('aria-pressed')`), 'true');
        // Native button keyboard interaction, not just programmatic click.
        await ev(`document.querySelector('[data-files-source="artifacts"]').focus()`);
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
        await shot('team-artifacts-list');
        await click('.team-artifact-name'); await settle();
        assert.match(await ev(`document.getElementById('inline-preview-body').textContent`), /preserves Files/);
        assert.equal(await ev(`document.querySelector('.inline-preview-filepath').textContent`), 'Design review.md');
        await shot('team-artifacts-preview');
        await ev('window._artifactBack()'); await settle();
        assert.equal(await ev(`document.querySelector('[data-files-source="artifacts"]').getAttribute('aria-pressed')`), 'true');
        assert.equal((await ev(`window.__calls.filter(u=>u.includes('/artifacts?'))`)).length, 1, 'back reuses list');
        await click('.team-artifact-row:nth-child(3) .team-artifact-name'); await settle();
        assert.match(await ev(`document.getElementById('inline-preview-body').textContent`), /preserves Files/);
        await ev('window._artifactBack()'); await settle();
        // Image preview uses the existing image renderer.
        await click('.team-artifact-row:nth-child(2) .team-artifact-name'); await settle();
        assert.ok(await ev(`!!document.querySelector('#inline-preview-body img')`));
        await ev('window._artifactBack()'); await settle();
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
        await click('.team-artifact-name'); await settle();
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
        await click('[data-files-source="artifacts"]'); await settle();
        assert.match(await text(), /Select an agent on a team/);
        assert.equal((await ev('window.__calls')).length, count);
        // The same controls and existing preview overlay work on narrow screens.
        await client.Emulation.setDeviceMetricsOverride({width:390,height:844,deviceScaleFactor:1,mobile:true});
        await ev(`document.getElementById('agentic-panel-files').style.width='350px'; window.__mode='normal'; window.__items=[{name:'Mobile notes.md',media_type:'text/markdown',available:true,uri:'coral://artifacts/'+'a'.repeat(64)}]; window.__state.currentSession.board_project='mobile-team'; window.__files.syncFilesViewerSession()`); await settle();
        if (SHOTS) { const {data} = await Page.captureScreenshot({format:'png'}); fs.writeFileSync(path.join(SHOTS,'team-artifacts-mobile.png'), Buffer.from(data,'base64')); }
        await click('.team-artifact-name'); await settle();
        assert.ok(await ev(`!!document.querySelector('.mobile-file-preview-overlay')`));
        assert.match(await ev(`document.querySelector('.mobile-file-preview-overlay').textContent`), /Mobile notes/);
        await ev('window._artifactBack()'); await settle();
        assert.equal(await ev(`!!document.querySelector('.mobile-file-preview-overlay')`), false);
        console.log('PASS: lazy/default loading, keyboard controls, metadata, safe links, previews/back/download, empty/error/retry, team changes and stale responses');
    } finally { await client.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
