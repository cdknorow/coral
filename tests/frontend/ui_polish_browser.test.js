// UI polish from QA's Windows walkthrough (#290, rows 10-19): the skip link, a focus ring on the sidebar search and the launch folder field, the end of a
// Windows path kept in a shortened path, the disclosure arrow of Settings > CLI paths, the wake text that no longer says "below", and the team member buttons that
// name the member. Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const STATIC = "(window.__coralStatic || '/static/')";

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setFocusEmulationEnabled({ enabled: true }); // a page nobody is looking at has no focus, so :focus would match nothing
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await waitFor(`!!document.getElementById('agent-search-input')`, 'the page', 20000);

        console.log('the skip link is the first stop and moves the focus to the main area');
        const skip = await ev(`(() => {
            const a = document.querySelector('.skip-link');
            const first = [...document.querySelectorAll('a[href], button, input, select, textarea, [tabindex]')].find(e => e.tabIndex >= 0 && !e.closest('[hidden]'));
            const before = location.href;
            a.focus(); const shown = a.getBoundingClientRect();
            a.click();
            return { exists: !!a, isFirst: first === a, onScreen: shown.top >= 0 && shown.left >= 0, focusedMain: document.activeElement && document.activeElement.id === 'main-stage', sameAddress: location.href === before, text: a.textContent };
        })()`);
        assert.ok(skip.exists, 'there is a skip link');
        assert.ok(skip.isFirst, 'and it is the first stop');
        assert.ok(skip.onScreen, 'it is on screen when it has the focus');
        assert.ok(skip.focusedMain, 'activating it focuses the main area');
        assert.ok(skip.sameAddress, 'without changing the address (the hash is a route)');

        console.log('the sidebar search and the launch folder field have a focus ring, not only a border colour');
        const ring = await ev(`(() => {
            const search = document.getElementById('agent-search-input'); search.focus();
            const cs = getComputedStyle(search);
            const rules = [];
            for (const sheet of document.styleSheets) { let list; try { list = sheet.cssRules; } catch (_) { continue; } for (const r of list) { if (r.selectorText && /#launch-dir:focus-visible/.test(r.selectorText)) rules.push(r.style.outlineWidth || r.style.outline); } }
            return { searchOutline: cs.outlineStyle + ' ' + cs.outlineWidth, launchRules: rules };
        })()`);
        assert.ok(/solid 2px/.test(ring.searchOutline), 'the sidebar search shows a 2 px outline when focused: ' + ring.searchOutline);
        assert.ok(ring.launchRules.length > 0 && ring.launchRules.every(w => /2px/.test(w)), 'the launch folder field has a focus-visible outline rule: ' + JSON.stringify(ring.launchRules));

        console.log('a shortened path keeps its end, on Windows too');
        const paths = await ev(`(async () => { const m = await import(${STATIC} + 'session_state.js'); return {
            win: m._shortPath('D:\\\\software\\\\coral\\\\José Ñúñez\\\\Proyecto largo', 2),
            slashes: m._shortPath('C:/Users/x/work/repo/sub', 2),
            unc: m._shortPath('\\\\\\\\host\\\\share\\\\a\\\\b\\\\c', 2),
            short: m._shortPath('C:\\\\work', 2),
            trailing: m._shortPath('D:\\\\a\\\\b\\\\c\\\\', 2),
            posix: m._shortPath('/Users/me/work/repo/sub', 2),
            home: m._shortPath('/home/me/a/b/c', 2) }; })()`);
        assert.equal(paths.win, '…\\José Ñúñez\\Proyecto largo');
        assert.equal(paths.slashes, '…\\repo\\sub', 'a Windows path written with slashes is cut on either');
        assert.equal(paths.unc, '…\\b\\c');
        assert.equal(paths.short, 'C:\\work', 'a short path is left whole');
        assert.equal(paths.trailing, '…\\b\\c', 'a trailing separator does not count');
        assert.equal(paths.posix, '…/repo/sub', 'a POSIX path is unchanged');
        assert.equal(paths.home, '…/b/c');

        console.log('Settings > CLI paths shows its disclosure arrow; the wake text points at the right place');
        await ev(`(async () => { await window.showSettingsModal('agents'); return true; })()`);
        await waitFor(`!!document.querySelector('.settings-cli-details > summary') && !!document.getElementById('tb-fixed')`, 'the Settings rows');
        const settings = await ev(`(() => {
            const sum = document.querySelector('.settings-cli-details > summary');
            const fixed = document.getElementById('tb-fixed');
            return { display: sum && getComputedStyle(sum).display, fixed: fixed && fixed.textContent };
        })()`);
        assert.equal(settings.display, 'list-item', 'a summary that is not flex keeps its arrow');
        assert.ok(settings.fixed && !/below/.test(settings.fixed), 'the wake text does not say "below" (main says when agents wake, with no pointer): ' + settings.fixed);

        console.log('the team member buttons name the member');
        const labels = await ev(`(async () => {
            const m = await import(${STATIC} + 'team_dialogs.js');
            const list = document.getElementById('team-agents-list');
            if (!list) return null;
            m._addTeamAgent('Lead Developer', 'Plans the work.');
            const row = list.lastElementChild;
            const out = [...row.querySelectorAll('button[aria-label]')].map(b => b.getAttribute('aria-label'));
            row.remove();
            return out;
        })()`);
        assert.ok(Array.isArray(labels), 'the Launch team list is in the page');
        assert.ok(labels.includes('Edit Lead Developer'), 'Edit names the member: ' + JSON.stringify(labels));
        assert.ok(labels.includes('Move Lead Developer up') && labels.includes('Move Lead Developer down') && labels.includes('Remove Lead Developer'), JSON.stringify(labels)); // main's wording (team_dialogs.js)
        console.log('   ✓');
    } finally { await Emulation.clearDeviceMetricsOverride().catch(() => {}); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
