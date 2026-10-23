// The team board's message box is the chat composer's card (#6239): one rounded box, the UI font, a box that grows with its
// text, @all as a chip in the bottom row and the round Send at its right, the chat's focus ring. Its keys are the chat's
// (Enter sends, Shift+Enter adds a line; the keyboard suite covers them). Desktop and a 390 px phone, light and dark.
// SHOT_DIR=<dir> also saves a screenshot of each state.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const board = `card-board-${Date.now()}`;
    const S = `(window.__coralStatic || '/static/')`;
    const shot = async name => {
        if (!process.env.SHOT_DIR) return;
        await new Promise(r => setTimeout(r, 300)); // sleep-ok: let the focus ring's transition end before the capture
        fs.writeFileSync(`${process.env.SHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
    };
    const open = async (width, height, mobile, theme) => {
        await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: 2, mobile });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`localStorage.setItem('coral-theme', '${theme}'); document.documentElement.setAttribute('data-theme', '${theme}'); true`);
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(async () => { for (const c of ['Plan for the release', 'Looks good, shipping it']) await fetch('/api/board/${board}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'card', as: 'QA', content: c }) }); return true; })()`);
        await ev(`window.__m.add('card-agent', 'A', 3, { sleeping: false, board_project: ${JSON.stringify(board)} }); true`);
        await new Promise(r => setTimeout(r, 600)); // sleep-ok: the first live list renders from several answers; no single completion event
        await ev(`window.selectLiveSession('card-agent', 'claude', 'card-agent-id', 'local'); true`);
        await waitFor(`!!document.getElementById('agentic-panel-board')`, 'the panel', 8000);
        if (mobile) await ev(`(document.getElementById('popout-panel-toggle-btn') || document.getElementById('panel-toggle-btn')).click(); true`); // the phone shows the side panel as an overlay
        await ev(`(async () => { const m = await import(${S} + 'render.js'); m.showBoardChatTab(${JSON.stringify(board)}); window.switchAgenticTab('board', 'top'); return true; })()`);
        await waitFor(`document.querySelectorAll('#board-panel-msgs .mb-message').length >= 2 && !!document.getElementById('board-panel-input').offsetParent`, 'the messages and the box', 10000);
    };
    const measure = () => ev(`(() => {
        const r = e => { const b = e.getBoundingClientRect(); return { l: b.left, r: b.right, t: b.top, b: b.bottom, w: b.width, h: b.height }; };
        const pane = document.getElementById('board-chat-input-pane'), ta = document.getElementById('board-panel-input');
        const chip = pane.querySelector('.board-mention-chip'), send = pane.querySelector('.btn-send');
        const cs = e => getComputedStyle(e);
        return {
            pane: r(pane), ta: r(ta), chip: r(chip), send: r(send),
            radius: parseFloat(cs(pane).borderTopLeftRadius), paneBorder: cs(pane).borderTopColor,
            taBorder: cs(ta).borderTopWidth, taBg: cs(ta).backgroundColor, taFont: cs(ta).fontFamily, uiFont: cs(document.body).fontFamily,
            placeholder: ta.placeholder, sendRadius: cs(send).borderTopLeftRadius, chipText: chip.textContent.trim(), chipLabel: chip.getAttribute('aria-label') || chip.textContent.trim(),
            sendName: send.getAttribute('aria-label'), handle: !!document.querySelector('.board-chat-resize-handle'),
            focusBorder: cs(pane).borderTopColor, accent: cs(document.documentElement).getPropertyValue('--accent').trim(),
            hscroll: document.documentElement.scrollWidth > innerWidth + 1, winH: innerHeight,
            order: [...pane.querySelectorAll('textarea, .board-mention-chip, .btn-send')].map(e => e.id || e.className.split(' ').pop()),
        };
    })()`);
    const check = async where => {
        const m = await measure();
        assert.equal(m.placeholder, 'Message the team... (@ to mention)', `${where}: placeholder`);
        assert.ok(m.radius >= 12, `${where}: one rounded box (radius ${m.radius})`);
        assert.equal(m.taBorder, '0px', `${where}: the text has no box of its own inside the card`);
        assert.equal(m.taBg, 'rgba(0, 0, 0, 0)', `${where}: the text sits on the card`);
        assert.ok(!/mono|courier|consolas|menlo/i.test(m.taFont), `${where}: the UI font, not monospace (${m.taFont})`);
        assert.ok(m.ta.b <= m.chip.t + 1 && m.ta.b <= m.send.t + 1, `${where}: chip and Send are in a row below the text`);
        assert.ok(m.chip.l < m.send.l && m.chip.l - m.pane.l < 40, `${where}: @all at the left, Send at the right`);
        assert.ok(m.pane.r - m.send.r < 24 && m.send.b <= m.pane.b, `${where}: Send is at the bottom right of the card`);
        assert.ok(Math.abs(m.send.w - m.send.h) < 1 && parseFloat(m.sendRadius) >= m.send.w / 2 - 1, `${where}: Send is round (${m.send.w}x${m.send.h})`);
        assert.equal(m.chipText, '@all', `${where}: the @all chip`);
        assert.equal(m.sendName, 'Send message', `${where}: Send keeps its name`);
        assert.equal(m.handle, false, `${where}: no fixed-height resize handle`);
        assert.ok(m.pane.h < m.winH * 0.3, `${where}: an empty box is short, not a tall square (${Math.round(m.pane.h)}px)`);
        assert.ok(!m.hscroll, `${where}: no horizontal scroll`);
        assert.deepEqual(m.order, ['board-panel-input', 'board-mention-chip', 'btn-send'], `${where}: Tab order: text, @all, Send`);
        return m;
    };
    const grows = async where => {
        const h0 = (await measure()).ta.h;
        await ev(`document.getElementById('board-panel-input').focus(); true`);
        await Input.insertText({ text: 'line one' });
        for (const l of ['line two', 'line three', 'line four']) { await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, modifiers: 8, text: '\r' }); await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, modifiers: 8 }); await Input.insertText({ text: l }); }
        const m = await measure();
        assert.ok(m.ta.h > h0 + 30, `${where}: the box grows with its lines (${h0} -> ${m.ta.h})`);
        assert.ok(m.ta.h <= m.winH * 0.4 + 1, `${where}: and stops at 40% of the window`);
        await shot(where.replace(/\W+/g, '_') + '_typed');
        await ev(`document.getElementById('board-panel-input').value = ''; true`);
        assert.ok((await measure()).ta.h <= h0 + 1, `${where}: clearing it (a send) shrinks the box back`);
    };

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [label, w, h, mobile] of [['desktop', 1280, 900, false], ['phone', 390, 844, true]]) {
            for (const theme of ['dark', 'light']) {
                const where = `${label} ${theme}`;
                console.log(`${where}`);
                await open(w, h, mobile, theme);
                await shot(where.replace(/\W+/g, '_') + '_empty');
                await check(where);
                await grows(where);
                // The focus ring is the chat's: the card's border turns to the accent while the box has focus.
                await ev(`document.getElementById('board-panel-input').focus(); true`);
                await new Promise(r => setTimeout(r, 300)); // sleep-ok: the border's colour transition (0.15 s) has to end
                const ring = await ev(`(() => { const p = document.getElementById('board-chat-input-pane'); const c = document.createElement('i'); c.style.color = getComputedStyle(document.documentElement).getPropertyValue('--accent'); document.body.appendChild(c); const a = getComputedStyle(c).color; c.remove(); return { border: getComputedStyle(p).borderTopColor, accent: a }; })()`);
                assert.equal(ring.border, ring.accent, `${where}: the card's border is the accent while the box has focus`);
            }
        }
        console.log('\nAll board composer card checks passed');
    } finally { await removeBoards(ev, [board]); await client.close(); }
})().catch(err => { console.error(err); process.exit(1); });
