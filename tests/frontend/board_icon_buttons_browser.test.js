// The board panel's Pause and Export are icon buttons (#5491): each has an accessible name and a tooltip and no visible text; the pause button's icon, name and tooltip say what it will do
// (Pause while reads are on, Resume while the board is paused) and the paused board's banner shows; the export button's name is Export messages and it is pressed while select mode is on;
// on a phone each is 44 px; both themes read at 4.5:1 with a focus ring. Against a real board on the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const board = `qa-icons-5491-${Date.now()}`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const state = () => ev(`(() => { const p = document.getElementById('board-chat-pause-btn'), x = document.querySelector('#agentic-panel-board .board-select-btn'); const r = e => { const b = e.getBoundingClientRect(); return { w: Math.round(b.width), h: Math.round(b.height) }; };
        return { pause: { label: p.getAttribute('aria-label'), title: p.title, icon: p.querySelector('.material-icons').textContent, text: [...p.childNodes].filter(n => n.nodeType === 3).map(n => n.textContent.trim()).join(''), size: r(p), paused: p.classList.contains('mb-action-danger') },
            exp: { label: x.getAttribute('aria-label'), title: x.title, icon: x.querySelector('.material-icons').textContent, text: [...x.childNodes].filter(n => n.nodeType === 3).map(n => n.textContent.trim()).join(''), size: r(x), pressed: x.getAttribute('aria-pressed') },
            banner: getComputedStyle(document.getElementById('board-chat-paused-banner')).display !== 'none' }; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [view, theme] of [[{ w: 1280, h: 900, phone: false }, 'light'], [{ w: 1280, h: 900, phone: false }, 'dark'], [{ w: 390, h: 844, phone: true }, 'light'], [{ w: 390, h: 844, phone: true }, 'dark']]) {
            const tag = `${view.w}px ${theme}`;
            await Emulation.setDeviceMetricsOverride({ width: view.w, height: view.h, deviceScaleFactor: 1, mobile: view.phone });
            await Emulation.setTouchEmulationEnabled({ enabled: view.phone });
            await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: theme }] });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`); await installAudit(ev); await setTheme(ev, theme);
            await ev(`fetch('/api/board/${board}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'qa-5491', as: 'QA', content: 'hello' }) }).then(r => r.status)`);
            await ev(`fetch('/api/board/${board}/resume', { method: 'POST' }).then(r => r.status)`);
            await ev(`window.__m.add('icons-bot', 'A', 4, { sleeping: false, board_project: ${JSON.stringify(board)} }); true`); await new Promise(r => setTimeout(r, 600)); // sleep-ok: the agent list draws over a few frames
            await ev(`window.selectLiveSession('icons-bot', 'claude', 'icons-bot-id', 'local'); true`); await new Promise(r => setTimeout(r, 900)); // sleep-ok: the agent's tabs draw over a few frames
            await ev(`window.switchAgenticTab('board', 'top'); true`);
            if (view.phone) await ev(`(() => { const side = document.getElementById('agentic-state'); if (side && side.getBoundingClientRect().width === 0) window.toggleAgenticPanel(true); return true; })()`); // a phone shows the side panel as a drawer
            await waitFor(`!!document.getElementById('board-chat-pause-btn') && document.getElementById('board-chat-pause-btn').getClientRects().length > 0`, `${tag}: the board panel`, 8000);
            await waitFor(`document.querySelector('#board-chat-pause-btn .material-icons').textContent === 'pause'`, `${tag}: the pause state read`, 5000);
            let s = await state();
            check(`${tag}: Pause is an icon button with a name and a tooltip and no visible text (${JSON.stringify(s.pause)})`, s.pause.label === 'Pause board' && /^Pause board/.test(s.pause.title) && s.pause.icon === 'pause' && s.pause.text === '' && !s.pause.paused && !s.banner);
            check(`${tag}: Export is an icon button too (${JSON.stringify(s.exp)})`, s.exp.label === 'Export messages' && /^Export/.test(s.exp.title) && s.exp.icon === 'download' && s.exp.text === '' && s.exp.pressed === 'false');
            if (view.phone) check(`${tag}: both are 44 px (${JSON.stringify([s.pause.size, s.exp.size])})`, s.pause.size.w >= 44 && s.pause.size.h >= 44 && s.exp.size.w >= 44 && s.exp.size.h >= 44);
            await ev(`document.getElementById('board-chat-pause-btn').click(); true`);
            await waitFor(`document.querySelector('#board-chat-pause-btn .material-icons').textContent === 'play_arrow'`, `${tag}: paused`, 6000);
            s = await state();
            check(`${tag}: paused: the icon, name and tooltip say Resume, the banner shows (${s.pause.label} / ${s.pause.title})`, s.pause.label === 'Resume board' && /^Resume board/.test(s.pause.title) && s.pause.paused && s.banner);
            if (process.env.CORAL_SCREENSHOT_DIR) require('node:fs').writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/board-icons-${view.w}-${theme}-paused.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
            const au = await auditRegion(ev, ['#agentic-panel-board .board-chat-header'], { focus: ['#board-chat-pause-btn', '#agentic-panel-board .board-select-btn'] });
            check(`${tag}: readable (4.5:1) with a focus ring, paused too (${au.checked} checked)`, au.fails.length === 0, JSON.stringify(au.fails.slice(0, 2)));
            await ev(`document.getElementById('board-chat-pause-btn').click(); true`);
            await waitFor(`document.querySelector('#board-chat-pause-btn .material-icons').textContent === 'pause'`, `${tag}: resumed`, 6000);
            check(`${tag}: resumed: back to Pause, no banner`, (await state()).pause.label === 'Pause board' && !(await state()).banner);
            await ev(`document.querySelector('#agentic-panel-board .board-select-btn').click(); true`);
            check(`${tag}: select mode presses Export`, (await state()).exp.pressed === 'true');
            await ev(`document.querySelector('#agentic-panel-board .board-select-btn').click(); true`);
            check(`${tag}: and leaving it lets go`, (await state()).exp.pressed === 'false');
            // the Board page writes this button too (loading a board, pausing it there): it must not put the text back or leave the name stale
            await ev(`import(${S} + 'message_board.js').then(m => m.selectBoardProject(${JSON.stringify(board)})).then(() => true)`);
            s = await state();
            check(`${tag}: the Board page loading the board leaves the icon, name and tooltip (${JSON.stringify(s.pause)})`, s.pause.icon === 'pause' && s.pause.text === '' && s.pause.label === 'Pause board' && /^Pause board/.test(s.pause.title));
            await ev(`import(${S} + 'message_board.js').then(m => m.toggleBoardPause()).then(() => true)`);
            await waitFor(`document.querySelector('#board-chat-pause-btn .material-icons').textContent === 'play_arrow'`, `${tag}: paused from the page`, 6000);
            s = await state();
            check(`${tag}: pausing from the Board page: the panel's button is the play icon named Resume board, no text (${JSON.stringify(s.pause)})`, s.pause.icon === 'play_arrow' && s.pause.text === '' && s.pause.label === 'Resume board' && /^Resume board/.test(s.pause.title));
            await ev(`import(${S} + 'message_board.js').then(m => m.toggleBoardPause()).then(() => true)`);
            await waitFor(`document.querySelector('#board-chat-pause-btn .material-icons').textContent === 'pause'`, `${tag}: resumed from the page`, 6000);
            s = await state();
            check(`${tag}: resuming from the Board page: back to the pause icon named Pause board, no text`, s.pause.icon === 'pause' && s.pause.text === '' && s.pause.label === 'Pause board');
        }
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        await ev(`fetch('/api/board/${board}', { method: 'DELETE' }).then(r => r.status)`).catch(() => {});
        await client.close(); process.exit(failed ? 1 : 0);
    } catch (e) { console.error('FAIL', e && e.stack || e); try { await ev(`fetch('/api/board/${board}', { method: 'DELETE' }).then(r => r.status)`); } catch (_) {} await client.close().catch(() => {}); process.exit(1); }
})().catch(e => { console.error(e); process.exitCode = 1; });
