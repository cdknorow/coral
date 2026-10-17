// #4396: the Team Artifacts panel says where a row came from (stubbed data; it does not wait for the server side of C1).
//  - provenance "unverified": the link text with "Link not verified", no size, no type, no Preview, no Download, never merged with a real row of the same id
//  - source "shared": "Sent to <server>/<board>"; source "received": "From <server>", "not scanned" in Details; neither shows a producer
//  - a legacy row (no provenance) renders as before: a missing object still says Missing
//  - the words are in the page (screen readers read them with the name), the rows keep their keyboard order, and the table fits a 390 px phone at 200% text
// Run against an isolated Coral dev server and headless Chrome via CDP. CORAL_SCREENSHOT_DIR=<dir> saves screenshots.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const SHOTS = process.env.CORAL_SCREENSHOT_DIR;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => {
        const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
        return result.result.value;
    };
    const settle = () => ev('new Promise(resolve => setTimeout(resolve, 250))'); // sleep-ok: the panel rebuilds after the stubbed list answers; there is no completion event
    const shot = async name => {
        if (!SHOTS) return;
        fs.mkdirSync(SHOTS, { recursive: true });
        const { data } = await Page.captureScreenshot({ format: 'png' });
        fs.writeFileSync(path.join(SHOTS, name + '.png'), Buffer.from(data, 'base64'));
    };
    const rowOf = name => `[...document.querySelectorAll('tr.team-artifact-row')].find(r => r.querySelector('.team-artifact-name').textContent === ${JSON.stringify(name)})`;
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1200, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await ev(`(async () => {
            const {state} = await import((window.__coralStatic||'/static/')+'state.js');
            const files = await import((window.__coralStatic||'/static/')+'changed_files.js');
            state.currentSession = { type:'live', name:'test-ui', session_id:'ui-a', board_project:'design-team' };
            const {showView} = await import((window.__coralStatic||'/static/')+'utils.js'); showView('live-session-view');
            const side = document.getElementById('agentic-state'); side.classList.remove('collapsed'); side.style.width = '680px';
            const hex = 'a'.repeat(64), other = 'c'.repeat(64), sent = 'd'.repeat(64), got = 'e'.repeat(64);
            window.__items = [
                { id: hex, name: 'Design review.md', media_type: 'text/markdown', size: 1800, task_id: 11, task_title: 'Review', created_at: '2026-10-02T15:00:00Z', uri: 'coral://artifacts/' + hex, available: true, source: 'completion', provenance: 'uploaded', produced_by: 'Worker A', session_id: 'w-a' },
                { id: 'uri:coral://artifacts/' + hex, name: 'Design review.md', task_id: 12, task_title: 'Cites the same id', created_at: '2026-10-02T15:10:00Z', uri: 'coral://artifacts/' + hex, available: false, source: 'completion', provenance: 'unverified' },
                { id: 'uri:coral://artifacts/' + other, name: "Another team's report.pdf", task_id: 13, created_at: '2026-10-02T15:20:00Z', uri: 'coral://artifacts/' + other, available: false, source: 'completion', provenance: 'unverified' },
                { id: 'shared:' + sent + ':1', name: 'Test report.txt', media_type: 'text/plain', size: 900, task_id: 0, created_at: '2026-10-02T16:00:00Z', uri: 'coral://artifacts/' + sent, available: true, source: 'shared', shared_to: 'homelab/qa', sent_by: 'Orchestrator', produced_by: null, session_id: null },
                { id: 'received:' + got, name: 'Their logs.txt', media_type: 'text/plain', size: 2048, task_id: 0, created_at: '2026-10-02T17:00:00Z', uri: 'coral://artifacts/' + got, available: true, source: 'received', received_from: 'homelab (ab12cd34)', from_board: 'platform', sent_by: 'Orchestrator (platform)', produced_by: null, session_id: null },
                { id: 'old', name: 'Earlier screenshot.png', media_type: 'image/png', task_id: 9, created_at: '2026-10-01T10:00:00Z', uri: 'coral://artifacts/' + 'f'.repeat(64), available: false, source: 'completion' },
            ];
            const real = window.fetch;
            window.fetch = async (url, options) => {
                const u = String(url);
                if (u.includes('/api/board/') && u.includes('/artifacts?')) return new Response(JSON.stringify({ project: decodeURIComponent(u.split('/')[3]), artifacts: window.__items, has_more: false, truncated: false }), { headers: { 'Content-Type': 'application/json' } });
                if (u.includes('/api/')) return new Response(JSON.stringify({ files: [] }), { headers: { 'Content-Type': 'application/json' } });
                return real(url, options);
            };
            files.initFileSearch(); files.syncFilesViewerSession();
        })()`);
        await ev(`localStorage.removeItem('coral-agentic-tab-top'); document.getElementById('agentic-tab-team-artifacts').click()`); await settle();
        assert.equal(await ev(`document.querySelectorAll('tr.team-artifact-row').length`), 6, 'every row is listed: the unverified one is not merged into the real row with the same id');

        console.log('1. An unverified row says so and offers nothing to open');
        const unv = await ev(`(() => { const rows = [...document.querySelectorAll('tr.team-artifact-row')].filter(r => r.dataset.origin === 'unverified'); return rows.map(r => ({ name: r.querySelector('.team-artifact-name').textContent, tag: r.querySelector('.team-artifact-name').tagName, badge: r.querySelector('.team-artifact-badge').textContent, type: r.querySelector('td.team-artifact-col-type').textContent, preview: r.querySelectorAll('.team-artifact-preview').length, links: r.querySelectorAll('a[href]').length, action: r.querySelector('.team-artifact-col-actions').textContent, details: r.querySelector('dl').textContent })); })()`);
        assert.equal(unv.length, 2);
        for (const r of unv) {
            assert.equal(r.badge, 'Link not verified');
            assert.equal(r.tag, 'SPAN', 'the name is not a button: nothing to preview');
            assert.equal(r.type, '—');
            assert.equal(r.preview, 0); assert.equal(r.links, 0);
            assert.equal(r.action, 'Not verified');
            assert.doesNotMatch(r.details, /MIME|Size/);
            assert.match(r.details, /Statusthis team has not uploaded|StatusLink not verified/);
        }
        await shot('origin-desktop');

        console.log('2. The real row with the same id is still managed');
        const real = await ev(`(() => { const r = ${rowOf('Design review.md')}; return { origin: r.dataset.origin || '', preview: r.querySelectorAll('.team-artifact-preview').length, type: r.querySelector('td.team-artifact-col-type').textContent, badge: !!r.querySelector('.team-artifact-badge') }; })()`);
        assert.deepEqual(real, { origin: '', preview: 1, type: 'Markdown', badge: false });

        console.log('3. A shared row and a received row are labelled and never show a producer');
        const sh = await ev(`(() => { const r = ${rowOf('Test report.txt')}; return { badge: r.querySelector('.team-artifact-badge').textContent, preview: r.querySelectorAll('.team-artifact-preview').length, details: r.querySelector('dl').textContent }; })()`);
        assert.equal(sh.badge, 'Sent to homelab/qa'); assert.equal(sh.preview, 1);
        assert.doesNotMatch(sh.details, /Producer/); assert.match(sh.details, /Sent tohomelab\/qa/); assert.match(sh.details, /Sent byOrchestrator/);
        const rc = await ev(`(() => { const r = ${rowOf('Their logs.txt')}; return { badge: r.querySelector('.team-artifact-badge').textContent, preview: r.querySelectorAll('.team-artifact-preview').length, details: r.querySelector('dl').textContent }; })()`);
        assert.equal(rc.badge, 'From homelab (ab12cd34)'); assert.equal(rc.preview, 1);
        assert.doesNotMatch(rc.details, /Producer/);
        assert.match(rc.details, /Board \(claimed by the sender\)platform/); assert.match(rc.details, /CheckedNot scanned/); assert.match(rc.details, /Sent byOrchestrator \(platform\)/);

        console.log('4. A legacy row renders as before');
        const old = await ev(`(() => { const r = ${rowOf('Earlier screenshot.png')}; return { badge: !!r.querySelector('.team-artifact-badge'), action: r.querySelector('.team-artifact-col-actions').textContent, origin: r.dataset.origin || '' }; })()`);
        assert.deepEqual(old, { badge: false, action: 'Missing', origin: '' });

        console.log('5. The words are in the page for a screen reader, and the keyboard order is unchanged');
        const ax = await ev(`(() => { const r = ${rowOf('Their logs.txt')}; const b = r.querySelector('.team-artifact-badge'); return { hidden: !!b.closest('[aria-hidden=true]'), after: !!(r.querySelector('.team-artifact-name').compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING), stops: [...r.querySelectorAll('button, a[href], summary')].length }; })()`);
        assert.equal(ax.hidden, false); assert.equal(ax.after, true);
        const stops = await ev(`[...document.querySelectorAll('tr.team-artifact-row')].map(r => r.querySelectorAll('button, a[href], summary').length)`);
        assert.ok(stops[1] === 1 && stops[2] === 1, 'an unverified row has one stop (Details) and no Preview or Download: ' + JSON.stringify(stops));

        console.log('6. A 390 px phone at 200% text: the table fits and the words stay in their row');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await ev(`document.getElementById('agentic-state').style.width = ''; document.documentElement.style.fontSize = '32px'; (document.getElementById('popout-panel-toggle-btn') || document.getElementById('panel-toggle-btn')).click(); true`); await settle();
        await ev(`window.switchAgenticTab('team-artifacts', 'top'); true`); await settle();
        assert.ok(await ev(`[...document.querySelectorAll('.team-artifact-badge')].filter(b => b.getBoundingClientRect().width > 0).length`) >= 4, 'the badges are on screen on the phone (the drawer is open)');
        const fit = await ev(`(() => { const s = document.querySelector('.team-artifacts-scroll'); const sr = s.getBoundingClientRect(); const out = [...document.querySelectorAll('.team-artifact-badge')].filter(b => { const r = b.getBoundingClientRect(); return r.right > sr.right + 1 || r.left < sr.left - 1; }).length; return { fits: s.scrollWidth <= s.clientWidth + 1, docOver: document.documentElement.scrollWidth - innerWidth, out, badge: parseFloat(getComputedStyle(document.querySelector('.team-artifact-badge')).fontSize) }; })()`);
        assert.equal(fit.fits, true, 'no horizontal scroll in the table'); assert.ok(fit.docOver <= 0, 'no sideways scroll'); assert.equal(fit.out, 0, 'every badge stays inside the table');
        assert.ok(fit.badge >= 20, 'the badge text grows with the root font: ' + fit.badge);
        await shot('origin-phone-200');
        console.log('PASS');
    } catch (e) {
        console.error(e && e.stack || e);
        process.exitCode = 1;
    } finally {
        try { await client.close(); } catch (_) {}
    }
})();
