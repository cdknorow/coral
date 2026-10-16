// The Browse tab's file tree by keyboard (#4026 follow-up). The directory answers are stubbed on the page.
//   - the tree is one Tab stop (the rows rove with the arrows); the copy/preview/edit buttons inside file rows are not extra Tab stops
//     for every file, and they are named;
//   - arrows, Right/Left, Home/End move and open/close folders; Enter on a file previews it;
//   - a row's action buttons stay visible while one of them has the focus.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const press = async (k, code, vk, modifiers = 0) => { const base = { key: k, code, windowsVirtualKeyCode: vk, modifiers }; await Input.dispatchKeyEvent({ type: 'keyDown', ...base, ...(k === 'Enter' ? { text: '\r' } : {}) }); await Input.dispatchKeyEvent({ type: 'keyUp', ...base }); };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const orig = window.fetch; window.fetch = (url, opts = {}) => { const u = String(url);
            if (/\\/search-files\\?/.test(u) && /view=explorer/.test(u)) { const dir = new URL(u, location.href).searchParams.get('dir');
                const entries = dir === '.' ? [{ name: 'src', type: 'dir', path: 'src' }, { name: 'README.md', type: 'file', path: 'README.md' }, { name: 'notes.txt', type: 'file', path: 'notes.txt' }]
                    : dir === 'src' ? [{ name: 'main.go', type: 'file', path: 'src/main.go' }, { name: 'util.go', type: 'file', path: 'src/util.go' }] : [];
                return Promise.resolve(new Response(JSON.stringify({ entries, root: '/work/demo' }), { headers: { 'content-type': 'application/json' } })); }
            return orig(url, opts); }; return true; })()`);
        await ev(`window.__m.add('tree-bot', 'A', 3, { sleeping: false }); true`);
        await new Promise(r => setTimeout(r, 600)); // sleep-ok: the first live list renders from several answers; no single completion event
        await ev(`window.selectLiveSession('tree-bot', 'claude', 'tree-bot-id', 'local'); window.switchAgenticTab('browse', 'top'); true`);
        await waitFor(`document.querySelectorAll('#file-explorer-view .explorer-item').length === 3`, 'the root rows', 8000);
        const rows = `[...document.querySelectorAll('#file-explorer-view .explorer-item')]`;
        const cur = () => ev(`document.activeElement.dataset && document.activeElement.dataset.path || document.activeElement.className`);

        console.log('1. One Tab stop, named buttons');
        const stops = await ev(`[...document.querySelectorAll('#file-explorer-view button, #file-explorer-view [tabindex]')].filter(e => e.tabIndex >= 0 && !e.disabled).map(e => e.className.split(' ')[0] + ':' + (e.dataset.path || e.textContent.trim()))`);
        const treeStops = stops.filter(s => /explorer-item|explorer-action-btn/.test(s));
        assert.equal(treeStops.length, 1, 'the tree has one Tab stop (got ' + JSON.stringify(treeStops) + ')');
        const unnamed = await ev(`[...document.querySelectorAll('#file-explorer-view .explorer-action-btn')].filter(b => !(b.getAttribute('aria-label') || '').trim()).length`);
        assert.equal(unnamed, 0, 'every row action button has an aria-label');

        console.log('2. Arrows, Right/Left, Home/End, Enter');
        await ev(`${rows}[0].focus(); true`);
        assert.equal(await cur(), 'src');
        await press('ArrowRight', 'ArrowRight', 39);
        await waitFor(`document.querySelectorAll('#file-explorer-view .explorer-item').length === 5`, 'the folder opens', 5000);
        assert.equal(await ev(`${rows}[0].getAttribute('aria-expanded')`), 'true');
        await press('ArrowRight', 'ArrowRight', 39);
        assert.equal(await cur(), 'src/main.go', 'Right on an open folder moves to its first child');
        await press('ArrowDown', 'ArrowDown', 40);
        assert.equal(await cur(), 'src/util.go');
        await press('ArrowLeft', 'ArrowLeft', 37);
        assert.equal(await cur(), 'src', 'Left on a file goes to its folder');
        await press('ArrowLeft', 'ArrowLeft', 37);
        await waitFor(`document.querySelectorAll('#file-explorer-view .explorer-item').length === 3`, 'the folder closes', 5000);
        await press('End', 'End', 35);
        assert.equal(await cur(), 'README.md', 'End goes to the last row (folders first, then by name)');
        await press('Home', 'Home', 36);
        assert.equal(await cur(), 'src');
        await press('ArrowDown', 'ArrowDown', 40);
        assert.equal(await cur(), 'notes.txt');
        await press('ArrowDown', 'ArrowDown', 40);
        assert.equal(await cur(), 'README.md');
        await press('Enter', 'Enter', 13);
        await waitFor(`/README\\.md/.test(document.getElementById('preview-tabs').textContent)`, 'Enter on a file opens it in the preview pane', 6000);

        console.log('3. A row\'s action buttons stay visible while one has the focus');
        const vis = await ev(`(() => { const row = document.querySelector('#file-explorer-view .explorer-item[data-path="README.md"]'); const btn = row.querySelector('.explorer-action-btn'); const style = document.createElement('style'); style.textContent = '#file-explorer-view * { transition: none !important; }'; document.head.appendChild(style);
            btn.focus(); const o = getComputedStyle(row.querySelector('.explorer-actions')).opacity; style.remove(); return { focused: document.activeElement === btn, opacity: o }; })()`);
        assert.equal(vis.focused, true, 'an action button takes focus');
        assert.equal(vis.opacity, '1', 'the actions are visible while one of them has the focus');
        console.log('   ✓ one Tab stop, named buttons, tree keys, visible actions');
        console.log('\nPASS file tree by keyboard');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
