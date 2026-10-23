// The launch dialog's folder browser closes when the folder is chosen (#6199, Codex Test's first-run walk): after picking the project folder the "No subdirectories" list stayed open and left More options a sliver; only Browse again
// closed it. Navigating in the list puts the folder in the field, so the list now has "Use this folder" (it closes the list and keeps the field), and a recent folder closes it as well. At 1280 and 390 px; the real server's
// folder list. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const hit = sel => ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e || !e.getClientRects().length) return 'missing'; const b = e.getBoundingClientRect(); const t = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2); return (t === e || e.contains(t)) && b.bottom <= innerHeight + 1 && b.top >= 0 ? 'ok' : 'covered or off screen by ' + (t ? t.tagName + '.' + String(t.className).slice(0, 30) : 'nothing') + ' ' + Math.round(b.top) + '-' + Math.round(b.bottom); })()`);
    // A folder with a folder in it, made here: '/usr' (what this used) is no folder on Windows, where the server answers "access denied" and the list has no rows.
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'launch-folder-'));
    fs.mkdirSync(path.join(root, 'inner'));
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(async () => { await window.showLaunchModal(); document.querySelector('#launch-step-chooser [data-arg="agent"]').click(); return true; })()`);
            await waitFor(`getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none' && !!document.getElementById('btn-browse')`, `${tag}: the New Agent step`, 8000);
            // open the list, go into a folder: the field follows, the list stays (it is a list to move in)
            await ev(`document.getElementById('launch-dir').value = ${JSON.stringify(root)}; true`); // a folder with a folder in it, made above
            await ev(`document.getElementById('btn-browse').click(); true`);
            await waitFor(`document.getElementById('dir-browser').style.display !== 'none' && document.querySelectorAll('#browser-list li[data-name]').length > 0`, `${tag}: the folder list`, 8000);
            const before = await ev(`document.getElementById('launch-dir').value`);
            await ev(`document.querySelector('#browser-list li[data-name]').click(); true`);
            await waitFor(`document.getElementById('launch-dir').value !== ${JSON.stringify(before)} && document.getElementById('launch-dir').value.length > ${before.length}`, `${tag}: the field follows the folder`, 8000);
            const inside = await ev(`document.getElementById('launch-dir').value`);
            check(`${tag}: going into a folder fills the field and the list stays open`, inside.length > before.length && (await ev(`document.getElementById('dir-browser').style.display`)) !== 'none', inside);
            const use = await hit('#dir-browser .dir-browser-use');
            check(`${tag}: "Use this folder" is there, on screen and not covered`, use === 'ok', use);
            const usePhone = mobile ? await ev(`(() => { const r = document.querySelector('#dir-browser .dir-browser-use').getBoundingClientRect(); return [Math.round(r.width), Math.round(r.height)]; })()`) : null;
            if (mobile) check(`${tag}: it is a 44 px target on a phone`, usePhone[0] >= 44 && usePhone[1] >= 44, JSON.stringify(usePhone));
            await ev(`document.querySelector('#dir-browser .dir-browser-use').click(); true`);
            const after = await ev(`({ shown: document.getElementById('dir-browser').style.display !== 'none', value: document.getElementById('launch-dir').value, focus: document.activeElement && document.activeElement.id })`);
            check(`${tag}: Use this folder closes the list and keeps the folder in the field, focus on the field`, !after.shown && after.value === inside && after.focus === 'launch-dir', JSON.stringify(after));
            const more = await hit('#launch-agent-next');
            check(`${tag}: Next is on screen and reachable with the list closed`, more === 'ok', more);
            // a recent folder is a choice: it closes the list
            await ev(`document.getElementById('btn-browse').click(); true`);
            await waitFor(`document.getElementById('dir-browser').style.display !== 'none'`, `${tag}: the list again`, 8000);
            await ev(`window.browserSelectRecent(${JSON.stringify(root)})`);
            await waitFor(`document.getElementById('dir-browser').style.display === 'none'`, `${tag}: a recent folder closes the list`, 8000);
            check(`${tag}: choosing a recent folder closes the list`, true);
            await ev(`window.hideLaunchModal(); true`);
        }
    } finally { try { fs.rmSync(root, { recursive: true, force: true }); } catch { /* the folder is the test's own, in the temp folder */ } /* the harness closes the page */ }
    const failed = results.filter(r => !r).length;
    console.log(`launch_folder_browser_use: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
