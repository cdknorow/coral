// Versions of an artifact in the viewer (#6472 layout A, #6480): the bar (Previous, "v3 of 3" opening a listbox newest first, Next, "Compare with v2"), a switch said once, the newer-version line, Compare (Changes and Side by
// side, added and removed lines worded for a screen reader), and a later version's comments: carried ones pinned with "from v2", an ambiguous one in "Needs a look", a gone one in "From earlier versions", and Reply/Resolve acting
// on the version the comment was written on. Stubbed server (the routes are Backend's, run in their own suites). 1280 and 390 px. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const { H1, H2, H3, STUB } = require('./helpers/artifact_versions_stub');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await new Promise(r => setTimeout(r, 400)); /* sleep-ok: a screenshot waits out transitions */ fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const vis = sel => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && !e.hidden && e.getClientRects().length > 0; })()`;
    const q = (sel, prop = 'textContent') => ev(`(document.querySelector(${JSON.stringify(sel)}) || {}).${prop}`);
    const openV = async (hex = H3) => { await ev(`(async () => { const v = await import(${S} + 'artifact_viewer.js'); v.openArtifactViewer(${JSON.stringify(hex)}, { filename: 'release-plan.md' }); return true; })()`); await waitFor(`!!document.querySelector('#artifact-viewer .av-current') && document.querySelector('#artifact-viewer .artifact-viewer-body').textContent.includes('Release plan')`, 'the viewer with its bar', 8000); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 820, false], [390, 844, true]]) {
            const at = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(STUB);
            await openV();

            console.log(`${at}: the bar of the newest version`);
            assert.equal(await q('#artifact-viewer .av-prev', 'ariaLabel') ?? await ev(`document.querySelector('#artifact-viewer .av-prev').getAttribute('aria-label')`), 'Previous version');
            assert.equal(await ev(`document.querySelector('#artifact-viewer .av-current').getAttribute('aria-label')`), 'Version: v3 of 3, newest');
            assert.equal(await ev(`document.querySelector('#artifact-viewer .av-current').getAttribute('aria-haspopup')`), 'listbox');
            assert.equal(await ev(`document.querySelector('#artifact-viewer .av-next').getAttribute('aria-disabled')`), 'true', `${at}: no newer version`);
            assert.equal(await q('#artifact-viewer .av-compare'), 'Compare with v2');
            assert.equal(await ev(vis('#artifact-viewer .av-notice')), false, `${at}: the newest has no "newer" line`);
            if (mobile) assert.deepEqual(await ev(`[...document.querySelectorAll('#artifact-viewer .av-bar .btn')].filter(b => b.getBoundingClientRect().height < 43.5).length`), 0, `${at}: 44 px targets`);

            console.log(`${at}: the list is a listbox, newest first, the shown one selected; keys and Escape`);
            await ev(`document.querySelector('#artifact-viewer .av-current').click(); true`);
            await waitFor(vis('#artifact-viewer .av-list'), `${at}: the list opens`, 3000);
            assert.deepEqual(await ev(`[...document.querySelectorAll('#artifact-viewer .av-option')].map(o => o.dataset.version + ':' + o.getAttribute('aria-selected'))`), ['3:true', '2:false', '1:false']);
            assert.equal(await ev(`document.querySelector('#artifact-viewer .av-list').getAttribute('role')`), 'listbox');
            assert.equal(await ev(`document.activeElement.dataset.version`), '3', `${at}: the keyboard is on the shown version`);
            await shot(`versions-list-${w}`);
            await ev(`document.activeElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true })); true`);
            assert.equal(await ev(`document.activeElement.dataset.version`), '2');
            await ev(`document.activeElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`);
            assert.equal(await ev(vis('#artifact-viewer .av-list')), false, `${at}: Escape closes the list`);
            assert.equal(await ev(`document.activeElement.classList.contains('av-current')`), true, `${at}: and the keyboard is back on the version button`);
            assert.equal(await ev(`document.getElementById('artifact-viewer').hidden`), false, `${at}: Escape did not close the viewer`);

            console.log(`${at}: choosing v2 shows it, says so once, and points to the newer one`);
            await ev(`document.querySelector('#artifact-viewer .av-current').click(); true`);
            await waitFor(vis('#artifact-viewer .av-list'), 'list again', 3000);
            await ev(`document.querySelector('#artifact-viewer .av-option[data-version="2"]').click(); true`);
            await waitFor(`document.querySelector('#artifact-viewer .artifact-viewer-body').textContent.includes('Revoke sits beside Rename')`, `${at}: v2's text`, 6000);
            assert.match(await q('#artifact-viewer .av-live'), /^Showing v2 of 3, .*, Frontend Dev/, `${at}: said once`);
            assert.equal(await ev(`document.querySelector('#artifact-viewer .av-current').getAttribute('aria-label')`), 'Version: v2 of 3');
            assert.match(await q('#artifact-viewer .av-notice'), /^v3 is newer\./);
            assert.equal(await ev(`document.activeElement.classList.contains('av-current')`), true, `${at}: the keyboard stays on the bar`);
            await ev(`document.querySelector('#artifact-viewer .av-notice .av-link').click(); true`);
            await waitFor(`document.querySelector('#artifact-viewer .artifact-viewer-body').textContent.includes('More menu')`, `${at}: Open the latest`, 6000);

            console.log(`${at}: the comments of a later version: carried, ambiguous, gone`);
            if (mobile) await ev(`document.querySelector('#artifact-viewer .ac-toggle').click(); true`);
            await waitFor(`document.querySelectorAll('#artifact-viewer .ac-card').length >= 3`, `${at}: the comments`, 6000);
            assert.match(await ev(`document.querySelector('#artifact-viewer .ac-card[data-id="1"] .ac-from').textContent`), /^from v2$/, `${at}: a carried comment says where it is from, in plain text`);
            assert.equal(await ev(`!!document.querySelector('#artifact-viewer .ac-list > .ac-card[data-id="1"]')`), true, `${at}: and is a card of the main list`);
            assert.equal(await ev(`document.querySelector('#artifact-viewer .ac-group[data-group="look"] summary').textContent`), 'Needs a look (1)');
            assert.equal(await ev(`document.querySelector('#artifact-viewer .ac-group[data-group="look"]').open`), true);
            assert.match(await ev(`document.querySelector('#artifact-viewer .ac-group[data-group="look"] .ac-pill').textContent`), /text appears twice/);
            assert.equal(await ev(`document.querySelector('#artifact-viewer .ac-group[data-group="earlier"] summary').textContent`), 'From earlier versions (1)');
            assert.equal(await ev(`document.querySelector('#artifact-viewer .ac-group[data-group="earlier"]').open`), false, `${at}: the gone group is closed`);
            assert.match(await ev(`document.querySelector('#artifact-viewer .ac-group[data-group="earlier"] .ac-card[data-id="3"]').textContent`), /text no longer present/);
            await ev(`document.querySelector('#artifact-viewer .ac-card[data-id="1"] .ac-resolve').click(); true`);
            await waitFor(`window.__posts.length === 1`, `${at}: Resolve is sent`, 4000);
            assert.match(await ev(`window.__posts[0]`), new RegExp(`/api/artifacts/${H2}/comments/1/resolve$`), `${at}: to the version the comment was written on, not the one on show`);
            await shot(`versions-comments-${w}`);

            console.log(`${at}: Compare: Changes, Side by side, back`);
            await ev(`document.querySelector('#artifact-viewer .av-compare').click(); true`);
            await waitFor(vis('#artifact-viewer .av-heading'), `${at}: the compare view`, 6000);
            assert.equal(await q('#artifact-viewer .av-heading'), 'Changes since v2');
            assert.equal(await q('#artifact-viewer .av-count'), '1 added, 1 removed');
            assert.equal(await ev(`document.activeElement.classList.contains('av-heading')`), true, `${at}: the keyboard goes to the changed content's heading`);
            assert.match(await q('#artifact-viewer .av-live'), /^Changes since v2: 1 added, 1 removed/);
            assert.deepEqual(await ev(`[...document.querySelectorAll('#artifact-viewer .av-add, #artifact-viewer .av-del')].map(l => l.textContent)`), ['Removed: Revoke sits beside Rename on every row.', 'Added: Revoke moves into a More menu on every row.']);
            assert.equal(await ev(`!!document.querySelector('#artifact-viewer .av-del del') && !!document.querySelector('#artifact-viewer .av-add ins')`), true, `${at}: <del> and <ins>`);
            assert.equal(await ev(`document.querySelector('#artifact-viewer .av-compare').getAttribute('aria-pressed')`), 'true');
            assert.equal(await q('#artifact-viewer .av-compare'), 'Back to v3');
            assert.equal(await ev(vis('#artifact-viewer .ac-panel')), false, `${at}: the comments are not shown while comparing`);
            await shot(`versions-compare-${w}`);
            await ev(`document.querySelector('#artifact-viewer .av-view-split').click(); true`);
            assert.equal(await ev(`document.querySelectorAll('#artifact-viewer .av-col').length`), 2);
            assert.equal(await ev(`document.querySelector('#artifact-viewer .av-view-split').getAttribute('aria-pressed')`), 'true');
            assert.match(await q('#artifact-viewer .av-live'), /^Changes since v2: 1 added, 1 removed/, `${at}: switching the view is not said again as a new compare`);
            await ev(`document.querySelector('#artifact-viewer .av-compare').click(); true`);
            await waitFor(`document.querySelector('#artifact-viewer .artifact-viewer-body').textContent.includes('More menu') && !document.querySelector('#artifact-viewer .av-heading')`, `${at}: back to the version`, 6000);
            assert.equal(await ev(`document.activeElement.classList.contains('av-compare')`), true, `${at}: the keyboard is on Compare`);
            if (mobile) assert.equal(await ev(`document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1`), true, `${at}: no sideways scroll`);
            await ev(`document.querySelector('#artifact-viewer .artifact-viewer-close').click(); true`);
        }
        console.log('artifact_versions: ok');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
