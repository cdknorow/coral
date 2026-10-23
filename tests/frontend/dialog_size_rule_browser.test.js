// One size rule for every dialog (UI audit #1): the Launch dialog (agent, team, terminal), Create Agent, Settings and Team
// settings are as high as their content up to 90% of the window (never a fixed 576 or 736 px that cuts content off while the
// screen is empty), as wide as clamp(360px, 92vw, 960px), scroll in ONE place (the pane) with the footer always in view.
// Measured at 1280x800, 1440x900, 1920x1080, 2560x1440 and 390x844. CORAL_MEASURE_OUT=<file> also writes every number as JSON
// (the before/after table of the task). Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;

const SIZES = [[1280, 800, false], [1440, 900, false], [1920, 1080, false], [2560, 1440, false], [390, 844, true]];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const TEAM = 'dialog-size-' + Date.now();
    const out = [];
    // The box, its scrollers that really scroll, and where its footer sits, as the person sees them.
    const MEASURE = `(box, footerSel) => {
        const r = box.getBoundingClientRect();
        const scrollers = [...box.querySelectorAll('*'), box].filter(e => { const s = getComputedStyle(e); return /(auto|scroll)/.test(s.overflowY) && e.scrollHeight > e.clientHeight + 1 && e.clientHeight > 0 && s.display !== 'none' && e.getBoundingClientRect().height > 0; })
            .map(e => ({ tag: e.tagName.toLowerCase() + (e.id ? '#' + e.id : '') + (e.className && typeof e.className === 'string' ? '.' + e.className.trim().split(/\\s+/)[0] : ''), visible: Math.round(e.clientHeight), total: e.scrollHeight }));
        const f = footerSel && [...box.querySelectorAll(footerSel)].filter(e => e.getBoundingClientRect().height > 0).pop();
        const fr = f && f.getBoundingClientRect();
        return { w: Math.round(r.width), h: Math.round(r.height), vw: innerWidth, vh: innerHeight, scrollers,
            footer: fr ? { bottom: Math.round(fr.bottom), inBox: fr.bottom <= r.bottom + 1, inView: fr.bottom <= innerHeight + 1 && fr.top >= 0 } : null,
            pageX: document.documentElement.scrollWidth - innerWidth };
    }`;
    const measure = (boxExpr, footerSel) => ev(`(${MEASURE})(${boxExpr}, ${JSON.stringify(footerSel || '')})`);
    const rule = (label, m, small) => {
        const wide = m.vw >= 768;
        const want = Math.min(960, Math.max(360, 0.92 * m.vw));
        if (wide && small) assert.ok(m.w <= want + 1.5, `${label}: a small form is no wider than the rule: ${m.w}`);
        else if (wide) assert.ok(Math.abs(m.w - want) <= 1.5, `${label}: width ${m.w} is clamp(360px, 92vw, 960px) = ${Math.round(want)} at ${m.vw}`);
        else assert.ok(m.w <= m.vw && m.w >= 0.9 * m.vw - 2, `${label}: a phone dialog fills the window: ${m.w} of ${m.vw}`);
        assert.ok(m.h <= 0.9 * m.vh + 1.5, `${label}: height ${m.h} is at most 90% of ${m.vh}`);
        // The terminal form (a small form) scrolls its step by the 24px its sticky header and footer overhang; that is padding, not content.
        if (small) m.scrollers = m.scrollers.filter(x => x.total - x.visible > 30);
        if (m.scrollers.length) assert.ok(m.h >= 0.9 * m.vh - 3, `${label}: it scrolls, so it must first use its room: ${m.h} of ${m.vh} (${JSON.stringify(m.scrollers)})`);
        assert.ok(m.scrollers.length <= 1, `${label}: ONE scroll area, found ${JSON.stringify(m.scrollers)}`);
        if (m.footer) assert.ok((m.footer.inBox || small) && m.footer.inView, `${label}: the footer stays in view ${JSON.stringify(m.footer)}`);
        assert.ok(m.pageX <= 1, `${label}: no page-wide horizontal scroll`);
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        const sub = await ev(`fetch('/api/board/${TEAM}/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session' }) }).then(r => r.status)`);
        assert.ok(sub < 300, 'a team to open the settings of: ' + sub);

        const screens = {
            // New Agent opens on the folder alone (a small form, like the terminal); "More options" opens its sections: the sectioned dialog the rule is about.
            'Launch agent (closed)': { small: true, open: `(async () => { await window.showLaunchModal(); window._selectLaunchType('agent'); return true; })()`, ready: `getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none'`, box: `document.querySelector('#launch-modal .modal-content')`, footer: '.modal-actions', close: `window.hideLaunchModal(); true` },
            'Launch agent': { open: `(async () => { await window.showLaunchModal(); window._selectLaunchType('agent'); document.getElementById('launch-agent-more').dispatchEvent(new CustomEvent('sd-more-open', { bubbles: true })); return true; })()`, ready: `getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none'`, box: `document.querySelector('#launch-modal .modal-content')`, footer: '.modal-actions', close: `window.hideLaunchModal(); true` },
            'Launch team': { open: `(async () => { await window.showLaunchModal(); window._selectLaunchType('team'); return true; })()`, ready: `getComputedStyle(document.getElementById('launch-step-team')).display !== 'none'`, box: `document.querySelector('#launch-modal .modal-content')`, footer: '.modal-actions', close: `window.hideLaunchModal(); true` },
            'Launch terminal': { open: `(async () => { await window.showLaunchModal(); window._selectLaunchType('terminal'); return true; })()`, ready: `getComputedStyle(document.getElementById('launch-step-terminal')).display !== 'none'`, box: `document.querySelector('#launch-modal .modal-content')`, footer: '.modal-actions', close: `window.hideLaunchModal(); true` },
            'Settings': { open: `(async () => { await window.showSettingsModal(); return true; })()`, ready: `getComputedStyle(document.getElementById('settings-modal')).display !== 'none' && !!document.querySelector('#settings-modal .settings-pane:not([hidden])')`, box: `document.querySelector('#settings-modal .modal-content')`, footer: '.modal-footer', close: `window.hideSettingsModal(); true` },
            'Team settings': { open: `document.getElementById('team-settings-workspace')?.remove(); document.getElementById('team-working-mode-dialog')?.remove(); import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`, ready: `!!document.querySelector('form.working-mode-form') && document.querySelector('form.working-mode-form').dataset.initialState !== undefined`, box: `document.getElementById('team-working-mode-dialog')`, footer: '.working-mode-actions, .working-mode-footer, .team-settings-actions', close: `document.getElementById('team-working-mode-dialog')?.close(); document.getElementById('team-working-mode-dialog')?.remove(); true` },
        };
        for (const [w, h, mobile] of SIZES) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            for (const [name, s] of Object.entries(screens)) {
                await ev(s.open);
                await waitFor(s.ready, `${name} open`, 15000);
                await new Promise(r => setTimeout(r, 150)); // sleep-ok: a dialog lays out its panes after it opens; there is no event for it
                const m = await measure(s.box, s.footer);
                out.push({ screen: name, size: `${w}x${h}`, ...m });
                if (!process.env.CORAL_MEASURE_ONLY) rule(`${name} at ${w}x${h}`, m, name === 'Launch terminal' || !!s.small);
                if (process.env.CORAL_SCREENSHOT_DIR && (w === 1920 || w === 390)) {
                    fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
                    fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name.toLowerCase().replace(/ /g, '-')}-${w}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
                }
                await ev(s.close);
                await new Promise(r => setTimeout(r, 50)); // sleep-ok: the close animation, no event
            }
        }
        if (process.env.CORAL_MEASURE_OUT) fs.writeFileSync(process.env.CORAL_MEASURE_OUT, JSON.stringify(out, null, 1));
        console.log(`   ✓ ${out.length} measurements`);
    } finally { await removeBoards(ev, [TEAM]); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
