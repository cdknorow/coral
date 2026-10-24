// A board message's Save-for-later icon sits on its sender's name row (#6995, the icon census's red on the 165 third head: "button.mb-save-btn ... -3 px from span.mb-agent-name"). The message header aligned its items on
// the text baseline on a mouse device, which puts a 16 px icon button 2 to 3 px off the name's line on some fonts (Linux, where the gate runs); it centres them, as it already did on touch. The real classes and the real
// Save button markup, on a desktop window at 100% and at 200% text, and 390 px with touch (at 200% text there the header wraps onto lines of its own, which the census skips): the icon's centre is within 1 px of the name's line centre; the time and Delete stay on the row. Drives Chrome over CDP
// through agent_view_harness.js against the isolated dev server.
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile, font] of [[1280, 800, false, ''], [1280, 800, false, '32px'], [390, 844, true, '']]) {
            const tag = `${w} px${mobile ? ' touch' : ''}${font ? ', 200% text' : ''}`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled(mobile ? { enabled: true, maxTouchPoints: 5 } : { enabled: false });
            await Emulation.setEmulatedMedia({ features: mobile ? [{ name: 'pointer', value: 'coarse' }, { name: 'hover', value: 'none' }] : [] });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            const r = await ev(`(async () => { ${font ? `document.documentElement.style.fontSize = '${font}';` : ''}
                const m = await import(${S} + 'saved_marks.js'); const host = document.createElement('div'); host.id = 'align-probe'; host.style.cssText = 'position:fixed;left:20px;top:80px;width:' + Math.min(innerWidth - 40, 700) + 'px;z-index:99999;background:var(--bg-primary)';
                host.innerHTML = '<div class="mb-message mb-message-first board-msg-left" role="listitem"><div class="mb-message-header"><span class="mb-agent-name">Frontend Dev</span><span class="message-time">12:34</span>' + m.BOARD_SAVE_BUTTON('team-a', 'team-a', '2026-10-24T12:34:00Z', 'Frontend Dev') + '<button class="mb-delete-msg-btn" aria-label="Delete the message">&times;</button></div><div class="mb-message-body">Hello</div></div>';
                document.body.appendChild(host);
                const name = host.querySelector('.mb-agent-name'), save = host.querySelector('.mb-save-btn'), svg = save.querySelector('svg'), time = host.querySelector('.message-time'), del = host.querySelector('.mb-delete-msg-btn');
                const range = document.createRange(); range.selectNodeContents(name); const nr = range.getClientRects()[0], g = svg.getBoundingClientRect();
                const row = host.querySelector('.mb-message-header').getBoundingClientRect(), dr = del.getBoundingClientRect();
                return { off: Math.round(((g.top + g.height / 2) - (nr.top + nr.height / 2)) * 10) / 10, ai: getComputedStyle(host.querySelector('.mb-message-header')).alignItems, sameRow: Math.abs(dr.top + dr.height / 2 - (row.top + row.height / 2)) < 20 && time.getBoundingClientRect().top < row.bottom }; })()`);
            check(`${tag}: the Save icon's centre is within 1 px of the sender name's line (${r.off} px)`, Math.abs(r.off) <= 1 && r.ai === 'center' && r.sameRow, JSON.stringify(r));
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} board save alignment checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
