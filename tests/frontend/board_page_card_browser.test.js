// The Message Board page (#6279): with a board open its own name is the heading and "Message Board" the small quiet label above it; the message box is the chat's card (one rounded box that grows
// with its text, a round Send inside it, the accent on the card while the text has focus, 44 px on touch). Desktop and a 390 px phone, light and dark. Real isolated server; the board is made here and taken away.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const board = `page-card-${Date.now()}`;
    const S = `(window.__coralStatic || '/static/')`;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile, theme] of [[1280, 800, false, 'dark'], [1280, 800, false, 'light'], [390, 844, true, 'dark']]) {
            const at = `${w}px ${theme}`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`localStorage.setItem('coral-theme', '${theme}'); document.documentElement.setAttribute('data-theme', '${theme}'); true`);
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`fetch('/api/board/${board}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'qa', as: 'QA', content: 'hello' }) }).then(r => r.status)`);
            await ev(`(async () => { const m = await import(${S} + 'message_board.js'); const { showView } = await import(${S} + 'utils.js'); showView('messageboard-view'); m.selectBoardProject(${JSON.stringify(board)}); return true; })()`);
            await waitFor(`document.getElementById('messageboard-project-badge').textContent.length > 0 && document.getElementById('mb-post-input').getClientRects().length > 0`, `${at}: the board`, 10000);
            const m = await ev(`(() => { const cs = e => getComputedStyle(e), r = e => e.getBoundingClientRect();
                const title = document.getElementById('messageboard-title'), name = document.getElementById('messageboard-project-badge'), card = document.querySelector('.mb-post-card'), ta = document.getElementById('mb-post-input'), send = document.querySelector('.mb-post-send-btn');
                return { titleText: title.textContent, nameSize: parseFloat(cs(name).fontSize), titleSize: parseFloat(cs(title).fontSize), nameAbove: r(name).top >= r(title).bottom - 1, nameTransform: cs(name).textTransform, nameBg: cs(name).backgroundColor,
                    radius: parseFloat(cs(card).borderTopLeftRadius), taBorder: cs(ta).borderTopWidth, taBg: cs(ta).backgroundColor, placeholder: ta.placeholder, sendRadius: cs(send).borderTopLeftRadius, sendW: r(send).width, sendH: r(send).height, sendName: send.getAttribute('aria-label'),
                    sendInside: r(send).right <= r(card).right && r(send).bottom <= r(card).bottom + 1, taH: r(ta).height, hscroll: document.documentElement.scrollWidth > innerWidth + 1 }; })()`);
            assert.equal(m.titleText, 'Message Board', `${at}: the label is still Message Board`);
            assert.ok(m.nameSize > m.titleSize && m.nameAbove, `${at}: the board's name is the heading (${m.nameSize}px) over the small label (${m.titleSize}px)`);
            assert.ok(m.nameTransform === 'none' && m.nameBg === 'rgba(0, 0, 0, 0)', `${at}: the name is plain text, not an uppercase pill (${m.nameTransform}, ${m.nameBg})`);
            assert.ok(m.radius >= 16 && m.taBorder === '0px' && m.taBg === 'rgba(0, 0, 0, 0)', `${at}: one rounded box, the text sits on the card (radius ${m.radius})`);
            assert.equal(m.placeholder, 'Message the team...', `${at}: placeholder`);
            assert.ok(m.sendInside && m.sendName === 'Send message' && parseFloat(m.sendRadius) >= m.sendW / 2 - 1, `${at}: a round Send inside the card, named (${m.sendW}x${m.sendH}, ${m.sendRadius})`);
            if (mobile) assert.ok(m.sendW >= 44 && m.sendH >= 44 && m.taH >= 44, `${at}: 44 px targets on a phone (${m.sendW}x${m.sendH}, box ${m.taH})`);
            // grows with its text, and the accent is on the card while the text has focus
            await ev(`document.getElementById('mb-post-input').focus(); document.getElementById('mb-post-input').value = 'one\\ntwo\\nthree\\nfour'; document.getElementById('mb-post-input').dispatchEvent(new Event('input', { bubbles: true })); true`);
            const g = await ev(`(() => { const ta = document.getElementById('mb-post-input'), card = document.querySelector('.mb-post-card'); return { h: ta.getBoundingClientRect().height, focus: getComputedStyle(card).borderTopColor, accent: getComputedStyle(document.documentElement).getPropertyValue('--accent').trim() }; })()`);
            assert.ok(g.h > m.taH + 30, `${at}: four lines grow the box (${m.taH} to ${g.h} px)`);
            assert.ok(g.focus !== 'rgba(0, 0, 0, 0)', `${at}: the card has a border while focused (${g.focus})`);
            assert.ok(!(await ev(`document.documentElement.scrollWidth > innerWidth + 1`)), `${at}: nothing scrolls sideways`);
        }
        console.log('PASS Message Board page: the name is the heading and the message box is the chat card');
    } finally { await removeBoards(ev, [board]); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
