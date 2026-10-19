// The team health monitor is off by default and easy to see (#4962): an icon beside the team's info button in the agent list, dimmed while the
// check is off and accent while it is on, named "Team health monitor: on/off"; a click opens the team's settings on the Health check tab, which
// (against the real server) shows a fresh team's check OFF and says what it costs; no icon where the live list carries no state (Coral Hub); the
// Agent Team dialog has the option, unchecked, with the same sentence, and sends it only when it is checked. 44 px on a phone. Run against an
// isolated dev server; set HEALTH_SHOT_DIR to save screenshots.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { setTheme } = require('./helpers/contrast');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const MOD = `(window.__coralStatic || '/static/')`;
    const TEAM = 'hl-' + Date.now().toString(36);
    const shot = async name => {
        if (!process.env.HEALTH_SHOT_DIR) return;
        const { data } = await Page.captureScreenshot({ format: 'png' });
        require('node:fs').writeFileSync(`${process.env.HEALTH_SHOT_DIR}/${name}.png`, Buffer.from(data, 'base64'));
    };
    const LONG = 'a-team-with-a-name-that-is-far-too-long-to-fit-in-the-sidebar-' + TEAM;
    const rows = (on, board = TEAM) => [1, 2].map(i => ({ session_id: `hl-${i}`, name: `hl-agent-${i}`, display_name: `Agent ${i}`, agent_type: 'claude', working_directory: '/tmp/hl', board_project: board, status: 'idle',
        ...(on === undefined ? {} : { team_health_on: on }) }));
    const render = (on, board) => ev(`import(${MOD} + 'render.js').then(async r => { const { state } = await import(${MOD} + 'state.js');
        state.liveSessions = ${JSON.stringify(rows(on, board))}; r.flushLiveSessionsRender(); r.renderLiveSessions(state.liveSessions); return true; })`);
    const icon = () => ev(`(() => { const b = document.querySelector('.team-health-btn'); if (!b) return null; const r = b.getBoundingClientRect(), cs = getComputedStyle(b);
        const open = document.querySelector('.team-open-btn').getBoundingClientRect();
        return { label: b.getAttribute('aria-label'), title: b.getAttribute('title'), on: b.classList.contains('is-on'), w: Math.round(r.width), h: Math.round(r.height), color: cs.color, opacity: cs.opacity,
            beside: Math.abs(r.top - open.top) < 6 && r.right <= open.left + 1, board: b.dataset.board, bg: cs.backgroundColor }; })()`);
    // The icon's contrast against what is behind it (the pill's tint composited over the panel), 3:1 for a graphic (WCAG 1.4.11).
    const contrast = () => ev(`(() => { const b = document.querySelector('.team-health-btn'); const cv = document.createElement('canvas'); cv.width = cv.height = 1; const cx = cv.getContext('2d', { willReadFrequently: true });
        const rgba = c => { cx.clearRect(0, 0, 1, 1); cx.fillStyle = '#000'; cx.fillStyle = c; cx.fillRect(0, 0, 1, 1); const d = cx.getImageData(0, 0, 1, 1).data; return [d[0], d[1], d[2], d[3] / 255]; };
        const over = (f, g) => [f[0] * f[3] + g[0] * (1 - f[3]), f[1] * f[3] + g[1] * (1 - f[3]), f[2] * f[3] + g[2] * (1 - f[3]), 1];
        const lum = c => { const t = c.slice(0, 3).map(v => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }); return 0.2126 * t[0] + 0.7152 * t[1] + 0.0722 * t[2]; };
        let bg = [255, 255, 255, 1]; const layers = []; for (let e = b; e; e = e.parentElement) { const c = rgba(getComputedStyle(e).backgroundColor); if (c[3] > 0) { layers.push(c); if (c[3] === 1) break; } } for (const l of layers.reverse()) bg = over(l, bg);
        const fg = over(rgba(getComputedStyle(b).color), bg); const a = lum(fg), z = lum(bg); return (Math.max(a, z) + 0.05) / (Math.min(a, z) + 0.05); })()`);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile, dev] of [[1280, 800, false, 'desktop'], [390, 844, true, 'phone']]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Emulation.setTouchEmulationEnabled({ enabled: mobile, maxTouchPoints: 5 }); // a phone is a coarse pointer: the 44 px rule
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);

          for (const theme of ['dark', 'light']) {
            await setTheme(ev, theme);
            const tag = `${dev}-${theme}`;
            // Off: the plain muted outline, named off.
            await render(false);
            const off = await icon();
            assert.ok(off, `${dev}: the team header has the health icon`);
            assert.equal(off.label, 'Team health monitor: off'); assert.equal(off.title, 'Team health monitor: off');
            assert.equal(off.on, false);
            assert.equal(off.bg, 'rgba(0, 0, 0, 0)', `${tag}: off is a plain outline, no pill`);
            assert.ok(off.beside, `${dev}: it sits beside the team's info button ${JSON.stringify(off)}`);
            if (mobile) assert.ok(off.w >= 44 && off.h >= 44, `${dev}: 44 px target (${off.w}x${off.h})`);
            const offContrast = await contrast();
            assert.ok(offContrast >= 3, `${tag}: the off icon is 3:1 against its panel (${offContrast.toFixed(2)})`);
            await shot(`${tag}-off`);

            // On: accent, full strength, named on.
            await render(true);
            const on = await icon();
            assert.equal(on.label, 'Team health monitor: on'); assert.equal(on.title, 'Team health monitor: on'); assert.equal(on.on, true);
            assert.notEqual(on.color, off.color, `${dev}: on and off differ in colour`);
            const accent = await ev(`getComputedStyle(document.documentElement).getPropertyValue('--accent').trim()`);
            const same = await ev(`(() => { const c = document.createElement('i'); c.style.color = ${JSON.stringify(accent)}; document.body.appendChild(c); const v = getComputedStyle(c).color; c.remove(); return v; })()`);
            assert.equal(on.color, same, `${dev}: accent coloured while on`);
            assert.notEqual(on.bg, 'rgba(0, 0, 0, 0)', `${tag}: on is an accent-tinted pill (${on.bg})`);
            const onContrast = await contrast();
            assert.ok(onContrast >= 3, `${tag}: the accent icon is 3:1 on its pill (${onContrast.toFixed(2)})`);
            await shot(`${tag}-on`);
          }
            await setTheme(ev, 'dark');

            // A long team name ends in an ellipsis, with the whole name as its title, and nothing in the header wraps or leaves it.
            await render(true, LONG);
            const long = await ev(`(() => { const t = document.querySelector('.group-name-text'); const hdr = document.querySelector('.session-team-group .board-card-header'); const hb = hdr.getBoundingClientRect(); const btn = document.querySelector('.team-health-btn').getBoundingClientRect();
                const cs = getComputedStyle(t); return { title: t.getAttribute('title'), truncated: t.scrollWidth > t.clientWidth, ellipsis: cs.textOverflow, nowrap: cs.whiteSpace, oneLine: t.getBoundingClientRect().height < 28, hdrH: Math.round(hb.height), btnInside: btn.right <= hb.right + 1 && btn.left >= hb.left }; })()`);
            assert.equal(long.title, LONG, `${dev}: the whole name is the title`);
            assert.ok(long.truncated && long.ellipsis === 'ellipsis' && long.nowrap === 'nowrap' && long.oneLine, `${dev}: the long name is cut with an ellipsis on one line ${JSON.stringify(long)}`);
            assert.ok(long.btnInside, `${dev}: the buttons stay inside the header ${JSON.stringify(long)}`);
            await shot(`${dev}-long-name`);

            // Coral Hub: the list carries no state, so no icon.
            await render(undefined);
            assert.equal(await icon(), null, `${dev}: no icon where the list gives no state`);
        }

        // A click opens the team's settings on the Health check tab, which (real server) shows a new team's check off and what it costs.
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        const sub = await ev(`fetch('/api/board/${TEAM}/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'hl-boss' }) }).then(r => r.status)`);
        assert.ok(sub < 300, 'subscribe');
        await render(false);
        await ev(`document.querySelector('.team-health-btn').click(); true`);
        await waitFor(`document.getElementById('team-settings-tab-health')?.getAttribute('aria-selected') === 'true'`, 'the Health check tab selected', 15000);
        const panel = await ev(`(() => { const f = document.querySelector('#team-settings-health form'); return { on: f.elements.on.checked, text: document.getElementById('hc-on-w').textContent, shown: !document.getElementById('team-settings-health').hidden, interval: f.elements.interval_min.value }; })()`);
        assert.equal(panel.on, false, 'a new team has the check off');
        assert.ok(panel.shown && /tokens/.test(panel.text) && /stuck or idle/.test(panel.text), `the setting says what it does and costs: ${panel.text}`);
        assert.equal(panel.interval, '10', 'and shows its interval');
        assert.deepEqual(await ev(`[...document.querySelectorAll('#team-settings-health fieldset')].map(f => [f.querySelector('legend').textContent, !!f.querySelector('[name=auto_delegate_on]')])`), [['The check', false], ['Reviews', true], ['Automatic rules', false]], 'handing reviews to the default reviewers is its own section, apart from the check and its rules');
        assert.ok(/whether or not the health check/.test(await ev(`document.querySelector('#team-settings-health [data-rule=auto_delegate]').closest('fieldset').textContent`)), 'and says it works with the check off');
        await shot('settings-health');

        // The Agent Team dialog: the option is there, unchecked, with the sentence; it is sent only when checked.
        await ev(`(async () => { window.__launches = []; const real = window.fetch; const json = (b) => Promise.resolve(new Response(JSON.stringify(b), { status: 200, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href); if (p.pathname === '/api/sessions/launch-team' && (o.method || 'GET') === 'POST') { window.__launches.push(JSON.parse(o.body)); return json({ agents: [{ name: 'A', session_id: 's' }] }); } return real(u, o); };
            await window.showLaunchModal(); window.hideLaunchModal(); // the dialog's markup loads on first use
            return true; })()`);
        const launch = async checked => {
            await ev(`window.showLaunchModal(); window._selectLaunchType('team'); true`);
            await waitFor(`getComputedStyle(document.getElementById('launch-step-team')).display !== 'none'`, 'the team step');
            await waitFor(`!document.querySelector('#launch-step-team .btn-primary').disabled`, 'the Launch button to be enabled (the last launch has finished)');
            const opt = await ev(`(() => { const c = document.getElementById('launch-team-health'); return { there: !!c, checked: c && c.checked, note: document.getElementById('team-health-option').textContent.replace(/\\s+/g, ' ').trim() }; })()`);
            assert.ok(opt.there && opt.checked === false, `the option starts unchecked ${JSON.stringify(opt)}`);
            assert.ok(/stuck or idle/.test(opt.note) && /tokens/.test(opt.note) && /Off by default/.test(opt.note), opt.note);
            // The option is in the Board & workspace section of the sectioned dialog (#4950).
            await ev(`document.querySelector('#launch-step-team [role=tab][data-section=board]').click(); true`);
            assert.ok(await ev(`document.getElementById('team-health-option').getClientRects().length > 0`), 'the option shows in the Board & workspace section');
            if (!checked) await shot('dialog');
            await ev(`document.getElementById('team-board-name').value = 'hl-launch'; document.getElementById('launch-team-dir').value = '/work/project'; document.getElementById('launch-team-health').checked = ${checked}; window._validateTeamName(); true`);
            await waitFor(`!document.getElementById('team-board-name-hint').classList.contains('error')`, 'the team name to be accepted');
            await ev(`window.launchTeam(); true`);
            await waitFor(`window.__launches.length === ${checked ? 2 : 1}`, 'the launch request');
            await waitFor(`getComputedStyle(document.getElementById('launch-modal')).display === 'none'`, 'the dialog closed');
        };
        await launch(false);
        assert.equal('health_monitor' in await ev(`window.__launches[0]`), false, 'unchecked: not sent, the team gets the default (off)');
        await launch(true);
        assert.equal(await ev(`window.__launches[1].health_monitor`), true, 'checked: sent');
        await ev(`window.showLaunchModal(); window._selectLaunchType('team'); true`);
        assert.equal(await ev(`document.getElementById('launch-team-health').checked`), false, 'a new dialog starts unchecked again');
        console.log('PASS team health monitor: off by default, the icon (dimmed off, accent on, 44 px on a phone, none through the Hub), it opens the setting, the dialog option');
    } finally { await ev(`fetch('/api/board/${TEAM}', { method: 'DELETE' }).then(r => r.status)`).catch(() => {}); await client.close(); } // the suite's board goes (the runner's end-check names one left)
})().catch(e => { console.error(e); process.exitCode = 1; });
