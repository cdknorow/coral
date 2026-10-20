// #5503: files that came with a cross-team message. On the receiving board the message's external.attachments show under its text as an image thumbnail or a file chip that opens in the
// artifact viewer, and a coral://artifacts link in that text whose file is not in the list shows as muted text saying "not on this machine" (a link that opens nothing is worse than no link).
// Messages that are not from another Coral (the sender's "to <board>" mirror, an ordinary post) keep their links. A name from the envelope is text, never markup; an entry without a real
// 64-hex id is dropped. Desktop and phone, both themes, the real page code, the messages served by a stub of the one list route.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

const H1 = '1'.repeat(64), H2 = '2'.repeat(64), H3 = '3'.repeat(64);

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const S = `(window.__coralStatic || '/static/')`;
    const board = `qa-attach-5503-${Date.now()}`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const msg = (id, extra) => ({ id, subscriber_id: 'x', job_title: 'Orchestrator', created_at: '2026-10-20T20:00:0' + id + 'Z', icon: '', ...extra });
    const MESSAGES = [
        msg(1, { content: `Report: [the report](coral://artifacts/${H1}) and the shot ![shot](coral://artifacts/${H2}) and [a file that did not come](coral://artifacts/${H3}).`,
            external: { direction: 'inbound', server_id: 'srv', board: 'other-team', sender: 'Orchestrator', attachments: [
                { id: H1, name: 'report.md', media_type: 'text/markdown', size: 2048 },
                { id: H2, name: 'shot.png', media_type: 'image/png', size: 70 },
                { id: 'zz', name: 'no real id', media_type: 'text/plain', size: 1 },
                { id: H3.slice(1), name: 'short id', media_type: 'text/plain', size: 1 },
                { id: 'a'.repeat(64), name: '<img src=x onerror="window.__pwn=1">.txt', media_type: 'text/plain', size: 3 * 1024 * 1024 }] } }),
        msg(2, { content: `My own copy: [it](coral://artifacts/${H3})`, external: { direction: 'outbound', server_id: 'srv', board: 'me', target_board: 'other-team', target_server_id: 'srv' } }),
        msg(3, { content: `An ordinary post: [it](coral://artifacts/${H3})` }),
        msg(4, { content: 'From another team, no files and no links', external: { direction: 'inbound', server_id: 'srv', board: 'other-team', sender: 'Orchestrator' } }),
    ];
    const snapshot = () => ev(`(() => { const by = id => [...document.querySelectorAll('#agentic-panel-board .mb-message')][id];
        const m = [0, 1, 2, 3].map(by);
        const r = e => { const b = e.getBoundingClientRect(); return { w: Math.round(b.width), h: Math.round(b.height) }; };
        const chips = [...m[0].querySelectorAll('.mb-attachment')];
        return { n: document.querySelectorAll('#agentic-panel-board .mb-message').length,
            chips: chips.map(c => ({ name: c.querySelector('.mb-attachment-name').textContent, label: c.getAttribute('aria-label'), image: c.classList.contains('is-image'), icon: (c.querySelector('.mb-attachment-icon') || {}).textContent || '', size: r(c), thumb: (c.querySelector('img') || {}).src || '', href: c.getAttribute('href') })),
            notHere: [...m[0].querySelectorAll('.mb-not-here')].map(e => e.textContent), links0: [...m[0].querySelectorAll('.mb-message-body a')].map(a => a.getAttribute('data-artifact')),
            imgs0: [...m[0].querySelectorAll('.mb-message-body img')].length,
            links1: [...m[1].querySelectorAll('.mb-message-body a')].length, notHere1: m[1].querySelectorAll('.mb-not-here').length, chips1: m[1].querySelectorAll('.mb-attachment').length,
            links2: [...m[2].querySelectorAll('.mb-message-body a')].length, notHere2: m[2].querySelectorAll('.mb-not-here').length,
            chips3: m[3].querySelectorAll('.mb-attachment').length, notHere3: m[3].querySelectorAll('.mb-not-here').length,
            pwn: !!window.__pwn, imgOnerror: document.querySelectorAll('#agentic-panel-board img[onerror]').length,
            overflow: Math.max(0, document.documentElement.scrollWidth - innerWidth) }; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [view, theme] of [[{ w: 1280, h: 900, phone: false }, 'light'], [{ w: 1280, h: 900, phone: false }, 'dark'], [{ w: 390, h: 844, phone: true }, 'light'], [{ w: 390, h: 844, phone: true }, 'dark']]) {
            const tag = `${view.w}px ${theme}`;
            await Emulation.setDeviceMetricsOverride({ width: view.w, height: view.h, deviceScaleFactor: 1, mobile: view.phone });
            await Emulation.setTouchEmulationEnabled({ enabled: view.phone });
            await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: theme }] });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`); await installAudit(ev); await setTheme(ev, theme);
            await ev(`(() => { const R = window.fetch; window.fetch = (u, o = {}) => { const p = new URL(String(u && u.url || u), location.origin);
                if (p.pathname === '/api/board/${board}/messages/all') return Promise.resolve(new Response(JSON.stringify({ messages: ${JSON.stringify(MESSAGES)}, total: 4, before_count: 0 }), { status: 200, headers: { 'content-type': 'application/json' } }));
                return R(u, o); }; return true; })()`);
            await ev(`window.__m.add('att-bot', 'A', 4, { sleeping: false, board_project: ${JSON.stringify(board)} }); true`); await new Promise(r => setTimeout(r, 600)); // sleep-ok: the agent list draws over a few frames
            await ev(`window.selectLiveSession('att-bot', 'claude', 'att-bot-id', 'local'); true`); await new Promise(r => setTimeout(r, 900)); // sleep-ok: the agent's tabs draw over a few frames
            await ev(`window.switchAgenticTab('board', 'top'); true`);
            if (view.phone) await ev(`(() => { const side = document.getElementById('agentic-state'); if (side && side.getBoundingClientRect().width === 0) window.toggleAgenticPanel(true); return true; })()`);
            await waitFor(`document.querySelectorAll('#agentic-panel-board .mb-message').length === 4`, `${tag}: the four messages`, 8000);
            const s = await snapshot();
            check(`${tag}: the inbound message shows its two real files as chips, in order, and drops the entries without a real id (${s.chips.map(c => c.name).join(' | ')})`, s.chips.length === 3 && s.chips[0].name === 'report.md' && s.chips[1].name === 'shot.png' && s.chips[2].name === '<img src=x onerror="window.__pwn=1">.txt', JSON.stringify(s.chips.map(c => c.name)));
            check(`${tag}: an image is a thumbnail of the artifact (or, when it cannot be drawn, a file chip with an image icon), a file a chip with name and size (${JSON.stringify([s.chips[0].label, s.chips[1].label, s.chips[2].label])})`, (s.chips[1].image ? /\/api\/artifacts\/2{64}/.test(s.chips[1].thumb) : s.chips[1].icon === 'image') && !s.chips[0].image && s.chips[0].label === 'Open report.md, 2.0 KB' && /3\.0 MB/.test(s.chips[2].label) && /\/api\/artifacts\/1{64}/.test(s.chips[0].href));
            check(`${tag}: the link to a file that did not come says "not on this machine" and is not a link (${JSON.stringify(s.notHere)}; links left ${JSON.stringify(s.links0)})`, s.notHere.length === 1 && /a file that did not come \(not on this machine\)/.test(s.notHere[0]) && s.links0.length === 1 && s.links0[0] === H1 && s.imgs0 >= 1);
            check(`${tag}: the sender's own mirror and an ordinary post keep their links and show no chips`, s.links1 === 1 && s.notHere1 === 0 && s.chips1 === 0 && s.links2 === 1 && s.notHere2 === 0);
            check(`${tag}: a message from another Coral with no files and no links shows nothing extra`, s.chips3 === 0 && s.notHere3 === 0);
            check(`${tag}: a name from the envelope is text, never markup`, !s.pwn && s.imgOnerror === 0);
            check(`${tag}: nothing makes the page wider (${s.overflow})`, s.overflow === 0);
            if (view.phone) check(`${tag}: a file chip is 44 px tall (${JSON.stringify(s.chips.map(c => c.size.h))})`, s.chips[0].size.h >= 44 && s.chips[2].size.h >= 44);
            if (process.env.CORAL_SCREENSHOT_DIR) require('node:fs').writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/board-attachments-${view.w}-${theme}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
            const au = await auditRegion(ev, ['#agentic-panel-board .mb-message'], { focus: ['#agentic-panel-board .mb-attachment'] });
            check(`${tag}: readable (4.5:1) with a focus ring (${au.checked} checked)`, au.fails.length === 0, JSON.stringify(au.fails.slice(0, 2)));
            // a plain click on a chip opens the artifact viewer
            await ev(`document.querySelector('#agentic-panel-board .mb-attachment').click(); true`);
            await waitFor(`(() => { const v = document.querySelector('.artifact-viewer'); return !!v && !v.hidden; })()`, `${tag}: the viewer`, 8000);
            check(`${tag}: a click on a chip opens the artifact viewer`, true);
        }
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed}/${results.length} checks passed`);
        if (failed) process.exitCode = 1;
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
