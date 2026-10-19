// The agent header (#5142, the operator): no model in it (the input toolbar's chip and the row's tooltip carry it), and the git branch is capped with an ellipsis, in the live header and the
// history header. The whole name shows in place on hover, on keyboard focus and on a tap, without moving the header's controls; a 70-character worktree branch must not push them off the
// screen at 320, 390, 1024 or 1280 px, in both themes. Real isolated server, a mock agent; drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const LONG = 'worktree-agent-a1b2c3d4e5f60718/feature/calm-the-dashboard-remove-dot-rings-and-honour-reduced-motion';
const SHORT = 'fix/header';

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a layout settle after a resize or a theme change
    const theme = name => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars && m.clearCustomThemeVars(); m.applyTheme(${JSON.stringify(name)}); return true; })()`);
    const move = async (x, y) => { await Input.dispatchMouseEvent({ type: 'mouseMoved', x, y }); await sleep(120); }; // sleep-ok: the :hover state after a pointer move
    const key = async (k, code) => { await Input.dispatchKeyEvent({ type: 'keyDown', key: k, code: k, windowsVirtualKeyCode: code }); await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code: k, windowsVirtualKeyCode: code }); };
    const open = async (w, mobile) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const entry = window.__m.entry.bind(window.__m); window.__m.entry = a => ({ ...entry(a), model: 'claude-opus-4-1-20250805', branch: ${JSON.stringify(LONG)}, repo_name: '' });
            window.__m.add('hb-agent', 'A', 4, { sleeping: false, session_id: 'hb-sid' }); return true; })()`);
        await waitFor(`(window.__qaState.liveSessions || []).some(s => s.session_id === 'hb-sid')`, 'the agent', 8000);
        await ev(`window.selectLiveSession('hb-agent', 'claude', 'hb-sid', 'local'); true`);
        await waitFor(`!!document.getElementById('terminal-header-label') && document.getElementById('terminal-header-label').textContent.length > 0`, 'the header', 8000);
        await ev(`T.render.updateSessionBranch(${JSON.stringify(LONG)}, '', {}); true`);
        await sleep(400); // sleep-ok: a layout or colour transition settling; there is no event for it
    };
    const geo = () => ev(`(() => { const h = document.querySelector('#live-session-view .terminal-header'); const hr = h.getBoundingClientRect(); const t = document.querySelector('#terminal-header-branch .terminal-branch-text');
        const vis = e => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && getComputedStyle(e).visibility !== 'hidden'; };
        const btns = [...h.querySelectorAll('button')].filter(vis).map(b => { const r = b.getBoundingClientRect(); return [Math.round(r.left), Math.round(r.right)]; });
        const br = document.getElementById('terminal-header-branch'); const brShown = vis(br);
        const tr = t.getBoundingClientRect();
        return { model: !!document.getElementById('terminal-header-model'), headerText: h.textContent.replace(/\\s+/g, ' '), over: document.documentElement.scrollWidth - innerWidth, btnOut: btns.filter(b => b[0] < 0 || b[1] > innerWidth + 0.5).length, btns: btns.length, brShown,
            capped: brShown && t.scrollWidth > t.clientWidth + 1, textW: Math.round(tr.width), fullName: t.textContent, chipFull: br.dataset.full, focusable: t.tabIndex === 0, role: t.getAttribute('role'), label: t.getAttribute('aria-label'),
            headerH: Math.round(hr.height), x: tr.left + tr.width / 2, y: tr.top + tr.height / 2 }; })()`);
    const pop = () => ev(`(() => { const br = document.getElementById('terminal-header-branch'); const c = getComputedStyle(br, '::after'); return { content: c.content, position: c.position, width: parseFloat(c.width), left: c.left, right: c.right,
        hdrLeft: document.querySelector('#live-session-view .terminal-header').getBoundingClientRect().left, hdrRight: document.querySelector('#live-session-view .terminal-header').getBoundingClientRect().right, top: c.top, color: c.color, bg: c.backgroundColor }; })()`);

    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [dev, w, mobile] of [['desktop', 1280, false], ['narrow desktop', 1024, false], ['phone', 390, true], ['small phone', 320, true]]) {
            console.log(dev);
            await open(w, mobile);
            let g = await geo();
            check(`${dev}: no model in the agent header (no #terminal-header-model, its text is not in the header)`, !g.model && !/Opus|Claude · /.test(g.headerText), JSON.stringify([g.model, g.headerText.slice(0, 120)]));
            check(`${dev}: the 70-character branch pushes nothing off screen (no sideways scroll, every header button inside the window)`, g.over <= 0 && g.btnOut === 0 && g.btns > 0, JSON.stringify([g.over, g.btnOut, g.btns]));
            const narrowHdr = await ev(`document.querySelector('#live-session-view .terminal-header').hasAttribute('data-narrow')`);
            if (g.brShown && narrowHdr) { check(`${dev}: a header too narrow for a branch squeezes it as before and takes it out of the Tab order (nothing to focus)`, (await ev(`document.querySelector('#terminal-header-branch .terminal-branch-text').tabIndex`)) === -1 && g.over <= 0, JSON.stringify([g.textW, g.over])); await shot(`header-branch-${dev.replace(' ', '-')}-closed`); continue; }
            if (!g.brShown) { const hw = await ev(`document.querySelector('#live-session-view .terminal-header').clientWidth`); check(`${dev}: the header is too narrow for a branch (${hw} px, under 22.25 rem), so it is not shown, and the buttons are all there`, hw < 356 + 12, String(hw)); await shot(`header-branch-${dev.replace(' ', '-')}-closed`); continue; }
            check(`${dev}: the branch is capped with an ellipsis (${g.textW} px wide, the name is longer), named in full for a screen reader`, g.capped && g.fullName === LONG && g.label === 'Branch ' + LONG && g.chipFull === LONG, JSON.stringify([g.capped, g.textW, g.label]));
            check(`${dev}: the capped text can be focused and says it is a button`, g.focusable && g.role === 'button');
            await shot(`header-branch-${dev.replace(' ', '-')}-closed`);
            // hover
            if (!mobile) {
                await move(g.x, g.y);
                let p = await pop();
                check(`${dev}: hover shows the whole name in place (the chip's ::after), inside the window, over the page`, p.content === JSON.stringify(LONG) && p.position === 'absolute' && p.hdrRight - 6 - p.width >= p.hdrLeft - 0.5, JSON.stringify([p.content.slice(0, 40), p.position, p.width, p.hdrLeft, p.hdrRight]));
                const g2 = await geo();
                check(`${dev}: and nothing in the header moved while it shows`, g2.headerH === g.headerH && g2.textW === g.textW, JSON.stringify([g.headerH, g2.headerH, g.textW, g2.textW]));
                for (const th of ['dark', 'light']) {
                    await theme(th); await sleep(350); // sleep-ok: a layout or colour transition settling; there is no event for it
                    p = await pop();
                    const ratio = await ev(`(() => { const px = c => { const sr = /color\\(srgb ([^)]+)\\)/.exec(c); const m = sr || /rgba?\\(([^)]+)\\)/.exec(c); const q = m[1].split(/[ ,\\/]+/).filter(Boolean).map(Number); const k = sr ? 255 : 1; return { r: q[0] * k, g: q[1] * k, b: q[2] * k }; };
                        const f = v => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }; const L = c => 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b); const c = getComputedStyle(document.getElementById('terminal-header-branch'), '::after');
                        const [a, b] = [L(px(c.color)), L(px(c.backgroundColor))].sort((x, y) => y - x); return (a + 0.05) / (b + 0.05); })()`);
                    check(`${dev}, ${th}: the shown name reads at 4.5:1`, ratio >= 4.5, ratio.toFixed(1));
                    await shot(`header-branch-${dev.replace(' ', '-')}-hover-${th}`);
                }
                await theme('dark');
                await move(5, 5);
                check(`${dev}: moving away closes it`, (await pop()).content === 'none');
                // keyboard
                await ev(`document.activeElement && document.activeElement.blur(); true`);
                let focused = false;
                for (let i = 0; i < 60 && !focused; i++) { await key('Tab', 9); focused = await ev(`document.activeElement && document.activeElement.classList.contains('terminal-branch-text')`); }
                check(`${dev}: Tab reaches the branch`, focused);
                if (focused) {
                    check(`${dev}: keyboard focus shows the whole name`, (await pop()).content === JSON.stringify(LONG));
                    await key('Tab', 9);
                    check(`${dev}: Tab away closes it`, (await pop()).content === 'none');
                }
            }
            // tap / Enter toggles (the tap is a click)
            await ev(`document.querySelector('#terminal-header-branch .terminal-branch-text').click(); true`);
            let p = await pop();
            check(`${dev}: a tap shows the whole name inside the header`, p.content === JSON.stringify(LONG) && (p.position === 'absolute' && p.hdrRight - 6 - p.width >= p.hdrLeft - 0.5), JSON.stringify([p.content.slice(0, 30), p.position, p.width, w]));
            check(`${dev}: and says it is expanded`, (await ev(`document.querySelector('#terminal-header-branch .terminal-branch-text').getAttribute('aria-expanded')`)) === 'true');
            await shot(`header-branch-${dev.replace(' ', '-')}-open`);
            await ev(`document.querySelector('#terminal-header-branch .terminal-branch-text').click(); true`);
            check(`${dev}: a second tap closes it`, (await pop()).content === 'none');
            await ev(`document.querySelector('#terminal-header-branch .terminal-branch-text').click(); true`);
            await ev(`document.body.click(); true`);
            check(`${dev}: a tap elsewhere closes it`, (await pop()).content === 'none');
            if (mobile) {
                // real touch taps (not .click()): a tap leaves :hover on the text, which used to keep the box open when the second tap closed it (#5154)
                const tap = async (x, y) => { await Input.dispatchTouchEvent({ type: 'touchStart', touchPoints: [{ x, y }] }); await Input.dispatchTouchEvent({ type: 'touchEnd', touchPoints: [] }); await sleep(250); }; // sleep-ok: the synthesized mouse and click events after a touch
                const at = await geo();
                const hoverable = await ev(`matchMedia('(hover: hover)').matches`);
                await tap(at.x, at.y);
                check(`${dev}: a touch tap opens the whole name (this window reports hover: ${hoverable})`, (await pop()).content === JSON.stringify(LONG), (await pop()).content.slice(0, 20));
                await tap(at.x, at.y);
                check(`${dev}: a second touch tap on the name closes it, though the tap left :hover on it`, (await pop()).content === 'none', (await pop()).content.slice(0, 20));
                await tap(at.x, at.y);
                await tap(w / 2, 600);
                check(`${dev}: a touch tap elsewhere closes it`, (await pop()).content === 'none', (await pop()).content.slice(0, 20));
            }
            // a short branch is not cut
            if (!mobile) {
                await ev(`T.render.updateSessionBranch(${JSON.stringify(SHORT)}, '', {}); true`); await sleep(200); // sleep-ok: a layout or colour transition settling; there is no event for it
                g = await geo();
                check(`${dev}: a short branch is whole`, !g.capped && g.fullName === SHORT, JSON.stringify([g.capped, g.fullName]));
            }
        }

        // the history header
        console.log('history');
        for (const [w, mobile] of [[1280, false], [320, true]]) {
            await open(w, mobile);
            await ev(`(async () => { const { state } = await import((window.__coralStatic || '/static/') + 'state.js'); state.historySessionsList = [{ session_id: 'hist-5142', branch: ${JSON.stringify(LONG)}, source_type: 'claude' }]; const m = await import((window.__coralStatic || '/static/') + 'sessions.js'); m.selectHistorySession('hist-5142'); return true; })()`);
            await waitFor(`getComputedStyle(document.getElementById('history-session-branch')).display !== 'none'`, 'the history branch', 8000);
            await sleep(300); // sleep-ok: a layout or colour transition settling; there is no event for it
            const h = await ev(`(() => { const c = document.getElementById('history-session-branch'); const t = c.querySelector('.branch-text'); const r = t.getBoundingClientRect(); const cs = getComputedStyle(c, '::after');
                return { capped: t.scrollWidth > t.clientWidth + 1, w: Math.round(r.width), full: t.textContent, chipFull: c.dataset.full, over: document.documentElement.scrollWidth - innerWidth, model: !!document.getElementById('terminal-header-model'), x: r.left + r.width / 2, y: r.top + r.height / 2, content: cs.content }; })()`);
            check(`history ${w}: the branch is capped (${h.w} px), no model in the header, nothing overflows sideways`, h.capped && h.chipFull === LONG && !h.model && h.over <= 0, JSON.stringify(h));
            await ev(`document.querySelector('#history-session-branch .branch-text').click(); true`);
            const o = await ev(`(() => { const c = document.getElementById('history-session-branch'); const cs = getComputedStyle(c, '::after'); const r = c.getBoundingClientRect(); return { content: cs.content, w: parseFloat(cs.width), left: r.left }; })()`);
            check(`history ${w}: a tap shows the whole name, inside the window`, o.content === JSON.stringify(LONG) && o.left + o.w <= w + 1, JSON.stringify(o));
            await shot(`header-branch-history-${w}-open`);
            await ev(`document.body.click(); true`);
        }
    } catch (e) {
        console.error(e && e.stack || e);
        results.push(false);
    } finally { try { await client.close(); } catch (_) {} }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS header branch cap: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
