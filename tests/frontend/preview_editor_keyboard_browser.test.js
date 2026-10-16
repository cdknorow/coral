// The file editor in the side preview pane, by keyboard (#4026 follow-up):
//   - opening a file in the editor, the toolbar's buttons are reached by Tab and Save works from the keyboard (Enter on the
//     focused Save button writes the file with a PUT and shows "Saved"); Save used to throw before it sent anything.
//   - Escape then Tab leaves the editor (CodeMirror keeps Tab for indenting, so a keyboard user needs that way out).
// The file routes are stubbed on the page; the editor itself (CodeMirror) is the real one. Run against an isolated dev server.
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
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('edit-bot', 'A', 4, { sleeping: false }); true`);
        await new Promise(r => setTimeout(r, 600)); // sleep-ok: the first live list renders from several answers; no single completion event
        await ev(`window.selectLiveSession('edit-bot', 'claude', 'edit-bot-id', 'local'); true`);
        await waitFor(`!!document.getElementById('preview-pane')`, 'the preview pane', 8000);
        // The file routes: a GET answers a small file; a PUT is recorded.
        await ev(`(() => { window.__puts = []; const orig = window.fetch; window.fetch = (url, opts = {}) => { const u = String(url);
            if (/\\/file-content\\?/.test(u)) { const m = (opts.method || 'GET').toUpperCase();
                if (m === 'PUT') { window.__puts.push(JSON.parse(opts.body).content); return Promise.resolve(new Response('{"ok":true}', { headers: { 'content-type': 'application/json' } })); }
                return Promise.resolve(new Response(JSON.stringify({ content: 'line one\\nline two\\n' }), { headers: { 'content-type': 'application/json' } })); }
            return orig(url, opts); }; return true; })()`);
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'preview_pane.js'); m.openPreviewTab('notes/todo.txt', 0, { mode: 'edit' }); return true; })()`);
        await waitFor(`!!document.querySelector('#preview-body .cm-content')`, 'the editor', 15000);
        await ev(`document.querySelector('#preview-body .cm-content').focus(); true`);
        await Input.insertText({ text: 'EDIT ' });
        await waitFor(`document.querySelector('#preview-body .cm-content').textContent.includes('EDIT ')`, 'the typed text', 4000);

        console.log('1. Escape, Tab leaves the editor');
        await press('Escape', 'Escape', 27);
        await press('Tab', 'Tab', 9);
        assert.equal(await ev(`!!document.activeElement.closest('.cm-editor')`), false, 'after Escape and Tab the focus is outside the editor');
        console.log('   ✓ focus left the editor (now on ' + await ev(`document.activeElement.className || document.activeElement.tagName`) + ')');

        console.log('2. Save by keyboard');
        const saveFocus = await ev(`(() => { const b = document.querySelector('#preview-toolbar .inline-preview-save'); if (!b) return 'no save button'; b.focus(); return document.activeElement === b ? 'ok' : 'not focusable'; })()`);
        assert.equal(saveFocus, 'ok', 'the Save button takes focus');
        const errs = [];
        await ev(`window.addEventListener('unhandledrejection', e => { window.__rej = String(e.reason && e.reason.message || e.reason); }); true`);
        await press('Enter', 'Enter', 13);
        await waitFor(`window.__puts.length === 1 || !!window.__rej`, 'the save', 5000);
        assert.equal(await ev(`window.__rej || ''`), '', 'Save does not throw');
        assert.deepEqual(await ev(`window.__puts`), ['EDIT line one\nline two\n'], 'Save writes the edited text');
        await waitFor(`/Saved/.test(document.body.textContent)`, 'the Saved toast', 4000);
        console.log('   ✓ Enter on Save wrote the file and showed Saved');
        console.log('\nPASS preview editor by keyboard');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
