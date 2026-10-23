// The (i) info buttons (#6750, the operator's bug: "too large and below their label, product-wide"): every (i) in Settings (each of its groups), at 1280 and at 390 px with touch, is a circle of at most 24 px
// (the visible size), sits on its label's first line (its centre within a few pixels of that line's centre, beside the label, not on a row of its own), and on a phone has an invisible touch target of 44 px or
// more around it (the ::after of .settings-info-btn) that stays inside the dialog. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;

const MEASURE = `(() => {
    const out = [];
    for (const b of document.querySelectorAll(window.__infoRoot || '#settings-modal .settings-info-btn')) {
        const r = b.getBoundingClientRect(); if (!r.width || !r.height || !b.getClientRects().length) continue;
        const cs = getComputedStyle(b, '::after');
        // the label's first line: the first text rectangle of the sibling label (or the text in front of the button)
        let label = [...b.parentElement.children].find(c => c !== b && /^(LABEL|SPAN|STRONG|LEGEND|H4|P)$/.test(c.tagName) && c.textContent.trim() && !c.classList.contains('material-icons') && c.getClientRects().length && !c.hidden);
        let first = null;
        if (label) { const range = document.createRange(); range.selectNodeContents(label); const rects = [...range.getClientRects()].filter(x => x.width > 1 && x.height > 1); first = rects.length ? rects[0] : null; }
        out.push({ about: (b.getAttribute('aria-label') || '').slice(0, 40), w: Math.round(r.width * 10) / 10, h: Math.round(r.height * 10) / 10, cy: r.top + r.height / 2, first: first ? { top: first.top, bottom: first.bottom, cy: first.top + first.height / 2 } : null,
            hit: cs.content !== 'none' && cs.position === 'absolute' ? { w: parseFloat(cs.width), h: parseFloat(cs.height) } : null });
    }
    return out; })()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await sleep(300); /* sleep-ok: a screenshot waits out transitions */ fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`window.showSettingsModal('general'); true`);
            await waitFor(`!!document.querySelector('#settings-modal .settings-info-btn')`, 'the Settings dialog', 8000);
            const groups = await ev(`[...document.querySelectorAll('#settings-modal .settings-group-pick option, #settings-modal [role=tab][data-group]')].map(o => o.value || o.dataset.group).filter(Boolean).filter((v, i, a) => a.indexOf(v) === i)`);
            const seen = [];
            for (const g of groups) {
                await ev(`window.showSettingsModal(${JSON.stringify(g)}); true`);
                await sleep(400); // sleep-ok: the pane draws and its rows load
                const m = await ev(MEASURE);
                for (const x of m) seen.push({ g, ...x });
                if (g === 'general' || g === 'agents') await shot(`info-${g}-${w}`);
            }
            check(`${tag}: found the (i) buttons of ${groups.length} Settings groups`, seen.length >= 10, `${seen.length} buttons in ${groups.join(', ')}`);
            const big = seen.filter(x => x.w > 24.5 || x.h > 24.5);
            check(`${tag}: every (i) is a circle of at most 24 px`, big.length === 0, JSON.stringify(big.slice(0, 4)));
            const off = seen.filter(x => x.first && Math.abs(x.cy - x.first.cy) > 5);
            check(`${tag}: every (i) is centred on its label's first line (within 5 px)`, off.length === 0, JSON.stringify(off.slice(0, 4).map(x => [x.g, x.about, Math.round(x.cy - x.first.cy)])));
            if (mobile) {
                const small = seen.filter(x => !x.hit || x.hit.w < 43.5 || x.hit.h < 43.5);
                check(`${tag}: every (i) has an invisible touch target of 44 px or more around it`, small.length === 0, JSON.stringify(small.slice(0, 4).map(x => [x.g, x.about, x.hit])));
            }
        }
        // Team settings: the same (i) in its Workflow, Prompt inspection and Health check pages
        const TEAM = 'info-btn-' + Date.now().toString(36);
        try {
            for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
                const tag = `${w}px, Team settings`;
                await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
                await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
                await ev(`window.__coralReady.startup('liveList', 'settings')`);
                await ev(`fetch('/api/board/${TEAM}/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session' }) }).then(r => r.status)`);
                await ev(`import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`);
                await waitFor(`!!document.querySelector('form.working-mode-form') && document.querySelector('form.working-mode-form').dataset.initialState !== undefined`, 'the Team settings', 15000);
                const tabs = await ev(`[...document.querySelectorAll('[data-settings-tab]')].map(t => t.dataset.settingsTab)`);
                const seen = [];
                for (const t of tabs) {
                    await ev(`document.querySelector('[data-settings-tab=' + JSON.stringify(${JSON.stringify(t)}) + ']').click(); window.__infoRoot = '.settings-info-btn'; true`);
                    await sleep(500); // sleep-ok: the page draws
                    for (const x of await ev(MEASURE)) seen.push({ g: t, ...x });
                    if (t === 'workflow' || t === 'health') await shot(`info-team-${t}-${w}`);
                }
                check(`${tag}: found the (i) buttons of ${tabs.join(', ')}`, seen.length >= 3, `${seen.length} buttons`);
                const big = seen.filter(x => x.w > 24.5 || x.h > 24.5);
                check(`${tag}: every (i) is a circle of at most 24 px`, big.length === 0, JSON.stringify(big.slice(0, 4)));
                const off = seen.filter(x => x.first && Math.abs(x.cy - x.first.cy) > 5);
                check(`${tag}: every (i) is centred on its label's first line (within 5 px)`, off.length === 0, JSON.stringify(off.slice(0, 4).map(x => [x.g, x.about, Math.round(x.cy - x.first.cy)])));
                if (mobile) {
                    const small = seen.filter(x => !x.hit || x.hit.w < 43.5 || x.hit.h < 43.5);
                    check(`${tag}: every (i) has an invisible touch target of 44 px or more around it`, small.length === 0, JSON.stringify(small.slice(0, 4).map(x => [x.g, x.about, x.hit])));
                }
            }
        } finally { await removeBoards(ev, [TEAM]).catch(() => {}); }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} info button checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
