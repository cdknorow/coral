// Light and dark, desktop and phone, of every shared list bar and the Inbox (#4851): each panel is put on screen with its bar's menu open where it has one, in each theme (the way the
// dashboard sets it: settings_core's applyTheme) at 1440 and 390 px, and audited (helpers/contrast.js): text 4.5:1 (3:1 large), the borders of the controls 3:1, the focus ring 3:1. A picture of
// each state goes to CORAL_SCREENSHOT_DIR when it is set. Panels: Inbox (Needs you, Saved for later), the Saved sidebar, Servers, the agent list, Tasks, Board search, Team Artifacts, Activity,
// History events. Real isolated server, stubbed where a panel needs a hub or a feed (as its own suite does). Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, auditRegion } = require('./helpers/contrast');
const { nextFrame } = require('./helpers/wait');
const BASE = require('./test_base').BASE;

const PANELS = require('./helpers/list_bars_panels');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const results = []; const all = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    let ctx0 = {};
    const only = (process.env.PANELS || '').split(',').filter(Boolean);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const ctx = ctx0 = { ev, waitFor, Page, Emulation, client, install, BASE, assert, settle: (c, l) => waitFor(c, l, 10000) };
        for (const p of PANELS) {
            if (only.length && !only.includes(p.key)) continue;
            await p.prepare?.(ctx);
            for (const w of [1440, 390]) for (const theme of ['dark', 'light']) {
                const tag = `${p.key} ${theme} ${w}`; ctx.__phone = w < 700;
                await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile: w < 700 }); await Emulation.setTouchEmulationEnabled({ enabled: w < 700 });
                let r;
                try {
                    const roots = await p.show(ctx, { w, theme, phone: w < 700 });
                    await setTheme(ev, theme);
                    await nextFrame(ev);
                    r = await auditRegion(ev, roots.roots, { focus: roots.focus || [] });
                    await shot(`bars-${p.key}-${theme}-${w}`);
                } catch (e) { check(`${tag}: shown`, false, String(e.message || e).slice(0, 300)); continue; }
                all.push({ tag, ...r });
                check(`${tag}: ${r.checked} checks, contrast (${r.borders.length} control borders under 3:1, reported apart)`, r.fails.length === 0, r.fails.length ? JSON.stringify(r.fails) : undefined);
            }
        }
        for (const it of ((await ev(`fetch('/api/saved').then(r => r.json())`)).items || [])) await ev(`fetch('/api/saved/${it.id}', { method: 'DELETE' }).then(() => true)`); // the bookmarks the Inbox panel seeded
        if (process.env.CONTRAST_REPORT) fs.writeFileSync(process.env.CONTRAST_REPORT, JSON.stringify(all, null, 1));
    } catch (e) {
        console.error(e && e.stack || e);
        results.push(false);
    } finally {
        for (const f of (ctx0.cleanup || [])) { try { await f(); } catch (_) {} }
        try { await client.close(); } catch (_) {}
    }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS list bars, light and dark: ${results.length} states`);
    process.exitCode = failed ? 1 : 0;
})();
