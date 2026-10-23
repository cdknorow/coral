// The board chat panel by keyboard (#4026 follow-up), against a real board on the test server:
//   - the message box has a name and is a combobox for the @mention list;
//   - typing @ opens the list; ArrowDown/ArrowUp move the highlight (aria-activedescendant follows), Enter and Tab pick the
//     highlighted name, Escape closes the list without sending or leaving the box (the list was reachable by mouse only);
//   - Enter sends the message (Ctrl+Enter and Cmd+Enter too); Shift+Enter with the list closed adds a line (#6239: as the chat).
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const press = async (k, code, vk, modifiers = 0) => { const base = { key: k, code, windowsVirtualKeyCode: vk, modifiers }; await Input.dispatchKeyEvent({ type: 'keyDown', ...base, ...(k === 'Enter' ? { text: '\r' } : {}) }); await Input.dispatchKeyEvent({ type: 'keyUp', ...base }); };
    const board = `kb-board-${Date.now()}`;
    const S = `(window.__coralStatic || '/static/')`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(async () => { for (const c of ['hello team', 'second message']) await fetch('/api/board/${board}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'kb', as: 'QA', content: c }) }); return true; })()`);
        // The board's subscribers for the @ list
        await ev(`(() => { const orig = window.fetch; window.__sent = []; window.fetch = (url, opts = {}) => { const u = String(url);
            if (/\\/subscribers(\\?|$)/.test(u)) return Promise.resolve(new Response(JSON.stringify([{ job_title: 'Backend Dev' }, { job_title: 'Back Office' }, { job_title: 'QA' }]), { headers: { 'content-type': 'application/json' } }));
            return orig(url, opts); }; return true; })()`);
        await ev(`window.__m.add('kb-agent', 'A', 3, { sleeping: false, board_project: ${JSON.stringify(board)} }); true`);
        await new Promise(r => setTimeout(r, 600)); // sleep-ok: the first live list renders from several answers; no single completion event
        await ev(`window.selectLiveSession('kb-agent', 'claude', 'kb-agent-id', 'local'); true`);
        await waitFor(`!!document.getElementById('agentic-panel-board')`, 'the panel', 8000);
        await ev(`(async () => { const m = await import(${S} + 'render.js'); m.showBoardChatTab(${JSON.stringify(board)}); window.switchAgenticTab('board', 'top'); return true; })()`);
        await waitFor(`document.querySelectorAll('#board-panel-msgs .mb-message').length === 2`, 'the messages', 10000);
        const box = `document.getElementById('board-panel-input')`;
        assert.equal(await ev(`${box}.getAttribute('aria-label')`), 'Message the team', 'the message box is named');
        assert.equal(await ev(`${box}.getAttribute('role')`), 'combobox');
        assert.equal(await ev(`${box}.getAttribute('aria-expanded')`), 'false');

        console.log('1. @ mention list by keyboard');
        await ev(`${box}.focus(); true`);
        await Input.insertText({ text: 'hi @ba' });
        await waitFor(`document.querySelectorAll('#board-mention-dropdown .board-mention-item').length === 2`, 'two matches for "ba" ' + JSON.stringify(await ev(`({ v: ${box}.value, html: document.getElementById('board-mention-dropdown').outerHTML.slice(0, 200), sel: ${box}.selectionStart })`)), 5000);
        assert.equal(await ev(`${box}.getAttribute('aria-expanded')`), 'true');
        const act = () => ev(`(() => { const a = document.querySelector('#board-mention-dropdown .board-mention-item.active'); return a ? { text: a.textContent, id: a.id, aria: ${box}.getAttribute('aria-activedescendant') } : null; })()`);
        let a = await act();
        assert.equal(a.text, 'Backend Dev', 'the first match is highlighted'); assert.equal(a.aria, a.id, 'aria-activedescendant names it');
        await press('ArrowDown', 'ArrowDown', 40);
        a = await act(); assert.equal(a.text, 'Back Office', 'ArrowDown moves to the next'); assert.equal(a.aria, a.id);
        await press('ArrowDown', 'ArrowDown', 40);
        a = await act(); assert.equal(a.text, 'Backend Dev', 'ArrowDown wraps');
        await press('ArrowUp', 'ArrowUp', 38);
        a = await act(); assert.equal(a.text, 'Back Office', 'ArrowUp wraps back');
        await press('Enter', 'Enter', 13);
        assert.equal(await ev(`${box}.value`), 'hi @Back Office ', 'Enter picks the highlighted name');
        assert.equal(await ev(`document.getElementById('board-mention-dropdown').style.display`), 'none', 'the list closes');
        assert.equal(await ev(`document.activeElement === ${box}`), true, 'focus stays in the box');
        assert.equal(await ev(`${box}.getAttribute('aria-expanded')`), 'false');
        await Input.insertText({ text: '@q' });
        await waitFor(`document.querySelectorAll('#board-mention-dropdown .board-mention-item').length === 1`, 'one match for "q"', 5000);
        await press('Tab', 'Tab', 9);
        assert.equal(await ev(`${box}.value`), 'hi @Back Office @QA ', 'Tab picks the highlighted name');
        assert.equal(await ev(`document.activeElement === ${box}`), true, 'Tab with the list open stays in the box');
        await Input.insertText({ text: '@b' });
        await waitFor(`document.querySelectorAll('#board-mention-dropdown .board-mention-item').length === 2`, 'the list again', 5000);
        await press('Escape', 'Escape', 27);
        assert.equal(await ev(`document.getElementById('board-mention-dropdown').style.display`), 'none', 'Escape closes the list');
        assert.equal(await ev(`${box}.value`), 'hi @Back Office @QA @b', 'Escape leaves the text alone');
        console.log('   ✓ arrows move, Enter and Tab pick, Escape closes; focus stays in the box');

        console.log('2. Enter sends, Shift+Enter adds a line (as in the chat, #6239); Ctrl+Enter and Cmd+Enter send too');
        await ev(`${box}.value = 'first'; ${box}.setSelectionRange(5, 5); true`);
        await press('Enter', 'Enter', 13, 8);
        assert.equal(await ev(`${box}.value`), 'first\n', 'Shift+Enter adds a line and does not send');
        await ev(`${box}.value = ''; true`);
        await press('Enter', 'Enter', 13);
        assert.equal(await ev(`${box}.value`), '', 'an Enter on an empty box sends nothing and adds no line');
        for (const [name, mod, text] of [['Enter', 0, 'sent by enter'], ['Ctrl+Enter', 2, 'sent by ctrl'], ['Cmd+Enter', 4, 'sent by cmd']]) {
            await ev(`${box}.value = ${JSON.stringify(text)}; true`);
            await press('Enter', 'Enter', 13, mod);
            await waitFor(`[...document.querySelectorAll('#board-panel-msgs .mb-message')].some(m => m.textContent.includes(${JSON.stringify(text)}))`, `${name} sends`, 8000);
            assert.equal(await ev(`${box}.value`), '', `${name} clears the box`);
        }
        console.log('   ✓ Enter, Ctrl+Enter and Cmd+Enter send; Shift+Enter adds a line');
        console.log('\nPASS board panel by keyboard');
    } finally { await removeBoards(ev, [board]); await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
