// The agent list's ≡ display menu on a phone (#3991). Its "Group by server" and "Show server" options lived only in the sidebar header,
// which is hidden below 768 px, so a phone could not group the list by server at all. The phone's list header now has the same menu.
// At 390 px with two servers: the menu button is there (44 px target), it offers Show summaries, Show server and Group by server,
// ticking Group by server groups the phone's list under the two servers and the menu stays open, unticking puts it back; with one
// server there is no Group by server; the menu fits the screen. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`localStorage.removeItem('coral-group-by-server'); true`);
        const scene = servers => ev(`(async () => {
            T.state.hub = true; T.state.cloud = true; T.state.servers = ${JSON.stringify(servers)}; T.state.liveSessions = []; window.__m.agents = {};
            ${JSON.stringify(servers)}.forEach(s => { window.__m.add('solo-' + s.id, 'S', 3, { sleeping: false, server: s.id }); window.__m.add('team-' + s.id, 'T', 3, { sleeping: false, server: s.id, board_project: 'team-' + s.id }); });
            T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
        const view = () => ev(`(() => {
            const list = document.getElementById('mobile-agent-list');
            const vis = e => !!e && !e.hidden && e.getClientRects().length > 0;
            const menu = list.querySelector('[data-display-panel]');
            const sum = list.querySelector('[data-display-button]');
            const panel = menu;
            const rows = menu ? [...menu.querySelectorAll('.list-display-option')].map(l => ({ text: l.textContent.trim(), shown: vis(l), checked: l.querySelector('input').checked })) : [];
            const pr = panel && vis(panel) ? panel.getBoundingClientRect() : null;
            const sr = sum && sum.getBoundingClientRect();
            return { menu: !!menu, button: sr ? { w: Math.round(sr.width), h: Math.round(sr.height), shown: vis(sum) } : null, open: !!(sum && sum.getAttribute('aria-expanded') === 'true'), rows,
                panelFits: pr ? pr.left >= 0 && pr.right <= innerWidth : null, overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
                headers: [...list.querySelectorAll('li.server-group-header')].map(h => h.querySelector('.server-group-name').textContent.trim()),
                grouped: localStorage.getItem('coral-group-by-server'), dupIds: ['display-group-by-server', 'display-show-server', 'display-show-summary'].filter(id => document.querySelectorAll('#' + id).length > 1) };
        })()`);
        const click = label => ev(`(() => { const l = [...document.querySelectorAll('#mobile-agent-list .list-display-option')].find(x => x.textContent.trim() === ${JSON.stringify(label)}); l.querySelector('input').click(); return true; })()`);

        const TWO = [{ id: 'm1', label: 'studio-mac', status: 'online' }, { id: 'm2', label: 'workstation', status: 'online' }];
        await scene(TWO);
        await waitFor(`!!document.querySelector('#mobile-agent-list [data-display-panel]')`, 'the phone display menu', 4000);
        let v = await view();
        assert.ok(v.menu && v.button.shown, 'the phone list header has the ≡ display menu');
        assert.ok(v.button.w >= 36 && v.button.h >= 36, `a usable target (${v.button.w} x ${v.button.h}; 44 with a coarse pointer)`);
        assert.deepEqual(v.dupIds, [], 'no id appears twice (the sidebar keeps its ids)');
        await ev(`(() => { const b = document.querySelector('#mobile-agent-list [data-display-button]'); if (b.getAttribute('aria-expanded') !== 'true') b.click(); return true; })()`);
        v = await view();
        assert.deepEqual(v.rows.map(r => [r.text, r.shown]), [['Show summaries', true], ['Show server', true], ['Group by server', true], ['Show branches', true], ['Show provider', true], ['Show model', true], ['Show thinking level', true]], 'Show summaries, Show server, Group by server, Show branches, Show provider, Show model and Show thinking level are offered with two servers');
        assert.equal(v.panelFits, true, 'the menu panel fits the 390 px screen');
        assert.equal(v.overflow, 0, 'no horizontal overflow');
        assert.equal(v.headers.length, 0, 'not grouped to begin with');
        await click('Group by server');
        await waitFor(`document.querySelectorAll('#mobile-agent-list li.server-group-header').length === 2`, 'two server headers in the phone list', 4000);
        v = await view();
        assert.deepEqual(v.headers.sort(), ['studio-mac', 'workstation'], 'the phone list is grouped under its two servers');
        assert.equal(v.grouped, 'true'); assert.ok(v.open, 'the menu stays open while it is used');
        assert.equal(v.rows.find(r => r.text === 'Group by server').checked, true, 'and its box reads ticked');
        await click('Group by server');
        await waitFor(`document.querySelectorAll('#mobile-agent-list li.server-group-header').length === 0`, 'the grouping is gone', 4000);
        v = await view();
        assert.equal(v.grouped, 'false'); assert.equal(v.rows.find(r => r.text === 'Group by server').checked, false);
        // one server: nothing to group by
        await scene([TWO[0]]);
        await waitFor(`!![...document.querySelectorAll('#mobile-agent-list .list-display-option')].length`, 'the menu with one server', 4000);
        v = await view();
        const group = v.rows.find(r => r.text === 'Group by server');
        assert.ok(!group || group.shown === false, 'with one server there is no Group by server');
        console.log('PASS phone display menu: present at 390 px, Group by server groups the phone list and stays open, absent with one server');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
