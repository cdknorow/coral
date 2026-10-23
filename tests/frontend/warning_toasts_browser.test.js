// The warnings the dashboard shows when the server finds a risk (#5737): the data folder others can read, a broad Windows Firewall rule, a damaged database, and the
// Settings row for an agent tools folder others can change. They come from /api/system/status (Windows-only in production), so the page's own functions are called
// with the texts here. Held: the three warnings do not sit on each other, each fits the window at a phone width, a desktop width and 200% text, each × is a button with
// a name and a 24 px target, the firewall warning keeps its lines (one per rule), and the tools folder row says what to do. Also with forced colors on.
// Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const FIREWALL = 'Windows Firewall lets other computers reach Coral on every port, more than it needs. Coral never makes firewall rules: Windows made these when someone clicked Allow on its prompt. To limit each rule to Private networks, open PowerShell as an administrator and run:\n- Coral (C:\\Coral\\coral.exe) on the Public profile, for every port: Set-NetFirewallRule -Name \'TCP Query User{26C1}C:\\Coral\\coral.exe\' -Profile Private   (or: Remove-NetFirewallRule -Name \'TCP Query User{26C1}C:\\Coral\\coral.exe\')\n- Coral tray (C:\\Coral\\coral-tray.exe) on the Public profile, for every port: Set-NetFirewallRule -Name \'TCP Query User{7A11}C:\\Coral\\coral-tray.exe\' -Profile Private   (or: Remove-NetFirewallRule -Name \'TCP Query User{7A11}C:\\Coral\\coral-tray.exe\')';
const SECURITY = 'WARNING: Coral\'s data folder C:\\Users\\someone\\.coral can be read by Users. It holds the API key, the command-line token and the databases. Move the folder (CORAL_DATA_DIR) to one only you can read, or remove those groups from its permissions (Properties > Security).';
const DATABASE = 'The sessions database is damaged (page 12 is corrupt). Stop Coral, copy the data folder somewhere safe, then start Coral again to rebuild it from the logs.';

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const cases = [['phone 390', 390, 844, 1, '', false], ['desktop 1280', 1280, 800, 1, '', false], ['1093x614 at 125%', 1093, 614, 1.25, '', false], ['200% text at 1280', 1280, 800, 1, '32px', false], ['forced colors', 1280, 800, 1, '', true]];
        for (const [name, w, h, dpr, fs, forced] of cases) {
            console.log(name);
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: dpr, mobile: false });
            await Emulation.setEmulatedMedia({ features: forced ? [{ name: 'forced-colors', value: 'active' }] : [] });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await waitFor(`!!document.getElementById('agent-search-input')`, 'the page', 15000);
            if (fs) await ev(`document.documentElement.style.fontSize = ${JSON.stringify(fs)}; true`);
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'update_check.js'); m.showSecurityWarning(${JSON.stringify(SECURITY)}); m.showFirewallWarning(${JSON.stringify(FIREWALL)}); m.showDatabaseWarning(${JSON.stringify(DATABASE)}); return true; })()`);
            await waitFor(`document.querySelectorAll('.security-warning-toast').length === 3`, 'three warnings');
            const m = await ev(`(() => { const r = [...document.querySelectorAll('.security-warning-toast')].map(t => { const b = t.getBoundingClientRect(); const c = t.querySelector('.update-toast-close'); const cb = c.getBoundingClientRect(); const txt = t.querySelector('.security-warning-text');
                return { id: t.id, l: b.left, t: b.top, r: b.right, b: b.bottom, role: t.getAttribute('role'), closeName: c.getAttribute('aria-label'), closeTag: c.tagName + ':' + c.type, cw: cb.width, ch: cb.height, ws: getComputedStyle(txt).whiteSpace, border: getComputedStyle(t).borderLeftWidth, vw: innerWidth, vh: innerHeight }; });
                const st = document.getElementById('warning-toasts').getBoundingClientRect();
                return { stack: { l: st.left, t: st.top, r: st.right, b: st.bottom }, vw: innerWidth, vh: innerHeight, r, overflowX: document.scrollingElement.scrollWidth > innerWidth + 1 }; })()`);
            assert.equal(m.r.length, 3);
            for (const t of m.r) {
                assert.equal(t.role, 'alert', t.id + ' is announced');
                assert.ok(/^Dismiss: /.test(t.closeName), t.id + ' close button has a name: ' + t.closeName);
                assert.equal(t.closeTag, 'BUTTON:button');
                assert.ok(t.cw >= 24 && t.ch >= 24, t.id + ' close target is 24 px or more: ' + t.cw + 'x' + t.ch);
                assert.equal(t.ws, 'pre-line', t.id + ' keeps its lines');
                if (!fs) assert.ok(t.l >= -1 && t.r <= t.vw + 1 && t.t >= -1 && t.b <= t.vh + 1, t.id + ' is inside the window: ' + JSON.stringify(t));
                assert.ok(parseFloat(t.border) > 0, t.id + ' has its edge');
            }
            for (let i = 0; i < 3; i++) for (let j = i + 1; j < 3; j++) { const a = m.r[i], b = m.r[j]; const overlap = Math.min(a.r, b.r) - Math.max(a.l, b.l) > 1 && Math.min(a.b, b.b) - Math.max(a.t, b.t) > 1; assert.ok(!overlap, `${a.id} and ${b.id} do not sit on each other`); }
            assert.ok(m.stack.l >= -1 && m.stack.r <= m.vw + 1 && m.stack.t >= -1 && m.stack.b <= m.vh + 1, 'the column of warnings is inside the window (it scrolls when 200% text outgrows it): ' + JSON.stringify(m.stack));
            assert.ok(!m.overflowX, 'the page does not scroll sideways');
            // the × closes its own warning and leaves the others where they are readable
            await ev(`document.querySelector('#firewall-warning-toast .update-toast-close').click(); true`);
            const left = await ev(`[...document.querySelectorAll('.security-warning-toast')].map(t => t.id)`);
            assert.deepEqual(left.sort(), ['database-warning-toast', 'security-warning-toast']);
        }

        console.log('the tools folder row');
        await Emulation.setEmulatedMedia({ features: [] });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await waitFor(`!!document.getElementById('agent-search-input')`, 'the page', 15000);
        await ev(`(async () => { await window.showSettingsModal('files'); return true; })()`);
        await waitFor(`!!document.getElementById('settings-tools-folder-row')`, 'the row');
        await ev(`document.getElementById('settings-tab-terminal').click(); true`); // the row is in the Terminal group
        await waitFor(`(() => { const p = document.querySelector('#settings-modal .settings-pane:not([hidden])'); return !!p && p.dataset.group === 'terminal'; })()`, 'the Terminal group', 5000);
        const row = await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.renderToolsFolderStatus({ tools_folder: { path: 'C:\\\\Users\\\\someone\\\\Coral\\\\tools', writable_by: ['Users', 'Everyone'] } });
            const r = document.getElementById('settings-tools-folder-row'), s = document.getElementById('settings-tools-folder-status'); const b = s.getBoundingClientRect();
            const vis = e => e.getClientRects().length > 0;
            const shown = [...s.querySelectorAll('span, button, div')].filter(vis).map(e => e.textContent.trim()).join(' ');
            const d = document.getElementById('settings-tools-folder-detail'), mb = document.getElementById('settings-tools-folder-more');
            const folded = { shownWords: [...s.childNodes].filter(n => n.nodeType === 1 && vis(n)).map(n => n.textContent.trim()).join(' ').split(/\\s+/).filter(Boolean).length, detailHidden: d.hidden, expanded: mb.getAttribute('aria-expanded'), label: mb.textContent.trim(), controls: mb.getAttribute('aria-controls') };
            const text = s.textContent; mb.click(); const opened = { detailHidden: d.hidden, expanded: mb.getAttribute('aria-expanded'), label: mb.textContent.trim(), detailShown: vis(d) };
            return { hidden: r.hidden, text, role: s.getAttribute('role'), right: b.right, vw: innerWidth, folded, opened, shown }; })()`);
        assert.equal(row.hidden, false);
        assert.equal(row.role, 'status');
        assert.match(row.text, /^Your agent tools folder can be changed without administrator rights\./, 'one line says what is wrong (true when only the owner can change it, and when other accounts can)');
        assert.ok(row.folded.shownWords <= 16, 'folded, the row is one short line: ' + JSON.stringify(row.folded));
        assert.deepEqual([row.folded.detailHidden, row.folded.expanded, row.folded.label, row.folded.controls], [true, 'false', 'How to fix it', 'settings-tools-folder-detail']);
        assert.deepEqual(row.opened, { detailHidden: false, expanded: 'true', label: 'Show less', detailShown: true });
        assert.ok(/install Coral with the installer/.test(row.text) && /Program Files/.test(row.text), 'the detail still says the one thing that fixes it: ' + row.text);
        assert.ok(/can be changed by Users, Everyone, not only by an administrator/.test(row.text) && row.text.includes('C:\\Users\\someone\\Coral\\tools'), 'and names the folder and who can change it: ' + row.text);
        assert.ok(!/hooks/.test(row.text), 'plain words, not the word hooks');
        assert.ok(row.right <= row.vw + 1, 'the row fits a phone width');
        console.log('   ✓');
    } finally { await Emulation.setEmulatedMedia({ features: [] }).catch(() => {}); await Emulation.clearDeviceMetricsOverride().catch(() => {}); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
