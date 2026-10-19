// The menu button (the three dots) of an agent row and of a group header blends with the row it sits on (#5121). It had a fixed grey (--bg-hover) hover
// fill, which drew an opaque box over the selected row's blue tile. On the selected row, the selected row with its attention, stuck, stale and
// sleeping marks, and a group header, with the pointer over the button (a real mouse move, so :hover applies): no fill at rest, and a hover fill that is a faint
// tint (alpha 0.25 or less) so the row's own fill shows through; the dots are readable (3:1 against the row). Both themes, desktop and a 390 px phone.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const { Input } = client;
    const shot = async (name, clip) => {
        if (!process.env.CORAL_SCREENSHOT_DIR) return;
        fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
        const r = await Page.captureScreenshot({ format: 'png', ...(clip ? { clip: { ...clip, scale: 2 } } : {}) });
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from(r.data, 'base64'));
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.clear(); true`);
        await ev(`(() => { for (const n of ['kb-a', 'kb-b']) window.__m.add(n, n.slice(-1).toUpperCase(), 3, { sleeping: false, agent_type: 'claude' }); T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
        await select('kb-a');
        await waitFor(`!!document.querySelector('#live-sessions-list li.session-group-item.active .sidebar-kebab-btn')`, 'the selected row', 8000, `({ lis: [...document.querySelectorAll('#live-sessions-list li')].map(l => l.className).slice(0, 8), kebabs: document.querySelectorAll('.sidebar-kebab-btn').length })`);
        const theme = name => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars && m.clearCustomThemeVars(); m.applyTheme(${JSON.stringify(name)}); return true; })()`);
        // The button's centre (scrolled into view); move the pointer off, or onto it.
        const centre = sel => ev(`(() => { const b = document.querySelector(${JSON.stringify(sel)}); b.scrollIntoView({ block: 'center' }); const r = b.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);
        const move = async (x, y) => { await Input.dispatchMouseEvent({ type: 'mouseMoved', x, y }); await new Promise(r => setTimeout(r, 160)); }; // sleep-ok: the 0.1s opacity transition and a frame after the move
        const read = sel => ev(`(() => { const b = document.querySelector(${JSON.stringify(sel)}); const cs = getComputedStyle(b); const row = b.closest('li') || b.closest('.session-group-header') || b.parentElement;
            const px = c => { const sr = /color\\(srgb ([^)]+)\\)/.exec(c); const m = sr || /rgba?\\(([^)]+)\\)/.exec(c); if (!m) return null; const p = m[1].split(/[ ,\\/]+/).filter(Boolean).map(Number); const k = sr ? 255 : 1; return { r: p[0] * k, g: p[1] * k, b: p[2] * k, a: p.length > 3 ? p[3] : 1 }; }; // color-mix() computes to color(srgb 0-1 ...)
            return { bg: px(cs.backgroundColor), color: px(cs.color), opacity: Number(cs.opacity), rowBg: px(getComputedStyle(row).backgroundColor), hover: b.matches(':hover') }; })()`);
        const lum = c => { const f = v => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b); };
        const over = (top, base) => ({ r: top.r * top.a + base.r * (1 - top.a), g: top.g * top.a + base.g * (1 - top.a), b: top.b * top.a + base.b * (1 - top.a), a: 1 });
        const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };
        // The page behind a row that has no fill of its own.
        const pageBg = () => ev(`(() => { const c = getComputedStyle(document.querySelector('.sidebar, #live-sessions-list') || document.body).backgroundColor; const m = /rgba?\\(([^)]+)\\)/.exec(c); const p = m[1].split(/[ ,\\/]+/).filter(Boolean).map(Number); return { r: p[0], g: p[1], b: p[2], a: p.length > 3 ? p[3] : 1 }; })()`);

        let LIST = '#live-sessions-list'; // the phone draws its own list
        const marks = [['selected', ''], ['needs attention', 'needs-attention'], ['stuck', 'stuck'], ['stale', 'stale'], ['sleeping', 'sleeping']];
        const run = async (label) => {
            const SEL = `${LIST} li.session-group-item.active .sidebar-kebab-btn`, GROUP = `${LIST} .session-group-header .group-kebab .sidebar-kebab-btn`, ROW = `${LIST} li.session-group-item.active`;
            for (const th of ['dark', 'light']) {
                await theme(th); await new Promise(r => setTimeout(r, 300)); // sleep-ok: the theme's colour transitions
                const page = await pageBg();
                for (const [what, cls] of marks) {
                    await ev(`(() => { const li = document.querySelector(${JSON.stringify(ROW)}); li.classList.remove('needs-attention', 'stuck', 'stale', 'sleeping'); ${cls ? `li.classList.add(${JSON.stringify(cls)});` : ''} return true; })()`);
                    const at = `${label}, ${th}, ${what}`;
                    await move(2, 2);
                    const rest = await read(SEL);
                    assert.ok(!rest.bg || rest.bg.a === 0, `${at}: no fill at rest: ` + JSON.stringify(rest.bg));
                    const c = await centre(SEL); await move(c.x, c.y);
                    const hov = await read(SEL);
                    if (what === 'selected') await shot(`row-kebab-${label.split(' ')[0].toLowerCase()}-${th}`, await ev(`(() => { const r = document.querySelector(${JSON.stringify(ROW)}).getBoundingClientRect(); return { x: Math.max(0, r.left - 6), y: Math.max(0, r.top - 6), width: Math.min(r.width + 12, innerWidth), height: r.height + 12 }; })()`));
                    assert.equal(hov.hover, true, `${at}: the pointer is over the button: ` + JSON.stringify({ c, top: await ev(`(() => { const e = document.elementFromPoint(${c.x}, ${c.y}); return e && (e.tagName + '.' + e.className + ' in ' + (e.closest('[id]') || {}).id); })()`), vp: await ev(`[innerWidth, innerHeight, document.querySelector(${JSON.stringify(SEL)}).getBoundingClientRect().toJSON()]`) }));
                    assert.ok(hov.bg && hov.bg.a > 0 && hov.bg.a <= 0.25, `${at}: the hover fill is a faint tint, so the row's fill shows through: ` + JSON.stringify(hov.bg));
                    const rowFill = hov.rowBg && hov.rowBg.a > 0 ? over(hov.rowBg, page) : page;
                    const dots = over({ ...hov.color, a: hov.color.a * hov.opacity }, over(hov.bg, rowFill));
                    assert.ok(ratio(dots, over(hov.bg, rowFill)) >= 3, `${at}: the dots read on the tint (3:1): ` + ratio(dots, over(hov.bg, rowFill)).toFixed(2));
                }
                // The group header's button (the folder's, and a team's, which share the class).
                if (await ev(`!!document.querySelector(${JSON.stringify(GROUP)})`)) {
                    await move(2, 2);
                    const rest = await read(GROUP); assert.ok(!rest.bg || rest.bg.a === 0, `${label}, ${th}, group header: no fill at rest`);
                    const c = await centre(GROUP); await move(c.x, c.y);
                    const hov = await read(GROUP);
                    assert.equal(hov.hover, true);
                    assert.ok(hov.bg && hov.bg.a > 0 && hov.bg.a <= 0.25, `${label}, ${th}, group header: the hover fill is a faint tint: ` + JSON.stringify(hov.bg));
                }
                await move(2, 2);
            }
            console.log(`   ✓ ${label}: no fill at rest, a faint tint on hover, readable dots, on every row state, both themes, and the group header`);
        };
        await run('Desktop');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: false }); // the phone's layout; a mouse, so :hover applies (a touch device has none)
        // On a phone the list is its own page, hidden while an agent is open: show the list and give the first row the selected state by class.
        await ev(`(() => { window.history.pushState({}, '', '/'); window.switchNavTab && window.switchNavTab('agents'); return true; })()`);
        LIST = '#mobile-session-list';
        await waitFor(`!!document.querySelector('#mobile-session-list li.session-group-item .sidebar-kebab-btn')`, 'the phone list', 8000, `({ lis: [...document.querySelectorAll('li')].map(l => l.className).slice(0, 8), id: !!document.getElementById('mobile-session-list') })`);
        await ev(`document.querySelectorAll('#mobile-session-list li.session-group-item').forEach((l, i) => l.classList.toggle('active', i === 0)); true`);
        await run('Phone 390x844');
        await Emulation.clearDeviceMetricsOverride();
        await theme('dark');
        console.log('\nPASS row kebab blend');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e); process.exitCode = 1; });
