// Servers in the agent list (#3085): short display names, one search-row server button instead of the
// strip and per-row chips; with 2+ servers 'N servers', an optional 'Show server' prefix on group headers
// ("pro-coral-hub", the server in italics, #3507) and optional 'Group by server' headers that collapse
// (persisted): the outermost, flush-left level, with a computer icon and no status dot (#3507).
// Keyboard, phone width, 44 px touch; agent state dots kept under server headers; team view from a
// prefixed row; the top tabs in the operator's order (Agents, Servers, ..., Docs).
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

const ONE = [{ id: 'm1', label: 'studio-mac.local', status: 'online', version: '1.4.6', last_seen: new Date().toISOString() }];
const THREE = [
    { id: 'm1', label: 'studio-mac.local', status: 'online', version: '1.4.6', last_seen: new Date().toISOString() },
    { id: 'm2', label: 'workstation.lan', status: 'online', version: '1.4.6', last_seen: new Date().toISOString() },
    { id: 'm3', label: 'Old Laptop', status: 'unreachable', version: '1.3.9', last_seen: '2026-10-01T00:00:00Z', last_error: 'dial tcp: timeout' },
];

const SETTINGS_PRELOAD = `(() => {
    window.__settingsPuts = [];
    const inner = window.fetch;
    window.fetch = (url, opts = {}) => {
        if (new URL(String(url), location.origin).pathname === '/api/settings' && (opts.method || 'GET').toUpperCase() === 'PUT') {
            try { window.__settingsPuts.push(JSON.parse(opts.body)); } catch { window.__settingsPuts.push(opts.body); }
        }
        return inner(url, opts);
    };
})();`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();

    // Cloud Hub: the list is about the machines only (no local row), so one machine = one server.
    const scene = (servers, extra = '') => ev(`(async () => {
        T.state.hub = true; T.state.cloud = true;
        T.state.servers = ${JSON.stringify(servers)};
        T.state.liveSessions = []; window.__m.agents = {};
        ${JSON.stringify(servers)}.forEach((s, i) => {
            window.__m.add('solo-' + s.id, 'S', 3, { sleeping: false, server: s.id });
            window.__m.add('team-' + s.id, 'T', 3, { sleeping: false, server: s.id, board_project: 'team-' + s.id });
        });
        ${extra}
        T.render.renderLiveSessions(T.state.liveSessions);
        return true;
    })()`);
    const view = () => ev(`(() => {
        const host = document.querySelector('#live-sessions-list')?.closest('aside, nav, .sidebar, body')?.querySelector('[data-server-switcher-host]') || document.querySelector('[data-server-switcher-host]');
        const sw = host && host.querySelector('details[data-server-switcher]');
        const sum = sw && sw.querySelector('summary');
        return {
            button: sw ? { label: sum.querySelector('.server-switcher-label').textContent, cls: sw.className, aria: sum.getAttribute('aria-label'), title: sum.getAttribute('title'), open: sw.open } : null,
            strip: document.querySelectorAll('#live-sessions-list .server-strip-row, #live-sessions-list .server-strip-item').length,
            chips: [...document.querySelectorAll('#live-sessions-list .row-server-prefix .row-server-name')].map(c => c.textContent.trim()),
            headers: [...document.querySelectorAll('#live-sessions-list li.server-group-header')].map(h => ({ id: h.dataset.serverGroup, name: h.querySelector('.server-group-name').textContent, state: h.querySelector('.server-group-state')?.textContent || null, expanded: h.querySelector('.server-group-toggle').getAttribute('aria-expanded') })),
            rows: [...document.querySelectorAll('#live-sessions-list li[data-session-id]')].map(li => li.dataset.sessionId),
            showServerRow: (r => r ? !r.hidden && getComputedStyle(r).display !== 'none' : null)(document.getElementById('display-show-server-row')),
            groupRow: (r => r ? !r.hidden && getComputedStyle(r).display !== 'none' : null)(document.getElementById('display-group-by-server')?.closest('label, li, div')),
        };
    })()`);
    const openSwitcher = () => ev(`(() => { const d = document.querySelector('[data-server-switcher-host] details[data-server-switcher]'); d.open = true; return true; })()`);

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: SETTINGS_PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.clear(); true`);

        console.log('1. Short names');
        const names = await ev(`(async () => {
            const { shortServerName } = await import((window.__coralStatic || '/static/') + 'server_status.js');
            const cases = ['studio-mac.local', 'workstation.lan', 'nas.home', 'box.example.com', '10.0.0.5', 'fe80::1', 'Old Laptop', 'build-01', 'My Server v1.2'];
            return Object.fromEntries(cases.map(c => [c, shortServerName({ id: 'x', label: c })]));
        })()`);
        assert.deepEqual(names, {
            'studio-mac.local': 'studio-mac', 'workstation.lan': 'workstation', 'nas.home': 'nas',
            'box.example.com': 'box', '10.0.0.5': '10.0.0.5', 'fe80::1': 'fe80::1', 'Old Laptop': 'Old Laptop', 'build-01': 'build-01',
            'My Server v1.2': 'My Server v1.2',
        });
        console.log('   ✓ hostnames lose their domain; IPs, IPv6 and renamed labels stay');

        console.log('2. One server: a compact button, no strip, no chips, no headers');
        await scene(ONE);
        let v = await view();
        assert.ok(v.button, 'the server button is shown');
        assert.equal(v.button.label, 'studio-mac', 'short name on the button');
        assert.match(v.button.aria, /^Server studio-mac\.local: Online$/, `accessible name has the full name and status (${v.button.aria})`);
        assert.equal(v.strip, 0, 'no strip'); assert.deepEqual(v.chips, [], 'no row chips with one server');
        assert.deepEqual(v.headers, [], 'no server headers with one server');
        assert.equal(v.groupRow, false, "'Group by server' is not offered with one server");
        await openSwitcher();
        const panel1 = await ev(`(() => {
            const p = document.querySelector('[data-server-switcher-host] .server-switcher-panel');
            const dl = Object.fromEntries([...p.querySelectorAll('dl.server-details-list dt')].map(dt => [dt.textContent, dt.nextElementSibling.textContent]));
            const open = p.querySelector('button.server-open-btn');
            return { dl, open: open && { text: open.textContent, id: open.dataset.serverOpen } };
        })()`);
        assert.equal(panel1.dl.Status, 'Online'); assert.equal(panel1.dl.Name, 'studio-mac.local');
        assert.equal(panel1.dl.Agents, '2'); assert.equal(panel1.dl.Version, '1.4.6'); assert.ok('Last contact' in panel1.dl);
        assert.deepEqual(panel1.open, { text: 'Open in Servers', id: 'm1' });
        // Keyboard: Escape closes and returns focus to the button.
        await ev(`(() => { const s = document.querySelector('[data-server-switcher-host] summary'); s.focus(); s.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); return true; })()`);
        await sleep(50);
        assert.equal(await ev(`document.querySelector('[data-server-switcher-host] details[data-server-switcher]').open`), false, 'Escape closes the panel');
        assert.equal(await ev(`document.activeElement === document.querySelector('[data-server-switcher-host] summary')`), true, 'focus returns to the button');
        // The open state survives a list re-render.
        await openSwitcher();
        await ev(`T.render.renderLiveSessions(T.state.liveSessions); true`);
        assert.equal(await ev(`document.querySelector('[data-server-switcher-host] details[data-server-switcher]').open`), true, 'the panel stays open across re-renders');
        await ev(`document.querySelector('[data-server-switcher-host] details[data-server-switcher]').open = false; true`);
        // 'Show server' with one server: off by default; turning it on saves the setting and shows chips.
        assert.equal(v.showServerRow, true, "'Show server' is offered with one server");
        assert.equal(await ev(`document.getElementById('display-show-server').checked`), false, 'off by default with one server');
        await ev(`(() => { const c = document.getElementById('display-show-server'); c.click(); return true; })()`);
        await waitFor(`window.__settingsPuts.some(p => p && p.show_session_server === true)`, 'PUT /api/settings {show_session_server: true}', 3000);
        const stored = await ev(`fetch('/api/settings').then(r => r.json()).then(d => String((d.settings || d).show_session_server))`);
        assert.equal(stored.toLowerCase(), 'true', `the server keeps the setting (${stored})`);
        await waitFor(`document.querySelectorAll('#live-sessions-list .row-server-prefix').length > 0`, 'server prefixes after Show server', 3000);
        v = await view();
        assert.ok(v.chips.every(c => /^studio-mac/.test(c)), `chips use the short name (${JSON.stringify(v.chips)})`);
        await ev(`document.getElementById('display-show-server').click(); true`);
        await waitFor(`window.__settingsPuts.some(p => p && p.show_session_server === false)`, 'PUT {show_session_server: false}', 3000);
        console.log('   ✓ button "studio-mac", panel details + Open in Servers, Escape, Show server saves + chips');

        console.log('3. Three servers (one offline): N servers, chips by default, Group by server, collapse persisted');
        await ev(`window.__settingsPuts.length = 0; (async () => { const s = await import((window.__coralStatic || '/static/') + 'state.js'); if (s.state.settings) delete s.state.settings.show_session_server; })(); true`);
        await scene(THREE);
        v = await view();
        assert.equal(v.button.label, '3 servers');
        assert.match(v.button.aria, /^3 servers, 1 not online/, `aggregate accessible name (${v.button.aria})`);
        assert.ok(!/server-online/.test(v.button.cls), `aggregate dot is not online while one is down (${v.button.cls})`);
        assert.equal(v.strip, 0, 'no strip');
        assert.ok(v.chips.length > 0, "'Show server' is on by default with 2+ servers");
        assert.ok(v.chips.some(c => /^Old Laptop/.test(c)) && v.chips.some(c => /^workstation/.test(c)), `chips name each server (${JSON.stringify(v.chips)})`);
        assert.equal(v.groupRow, true, "'Group by server' is offered with 2+ servers");
        await openSwitcher();
        const items = await ev(`[...document.querySelectorAll('[data-server-switcher-host] button.server-switcher-item')].map(b => ({ id: b.dataset.serverOpen, name: b.querySelector('.server-switcher-name').textContent, meta: b.querySelector('.server-switcher-meta').textContent }))`);
        assert.deepEqual(items.map(i => i.name), ['studio-mac', 'workstation', 'Old Laptop']);
        assert.match(items[2].meta, /^Unreachable · 2$/i, `offline item shows its state and count (${items[2].meta})`);
        await ev(`document.querySelector('[data-server-switcher-host] details[data-server-switcher]').open = false; true`);
        // #3507 ungrouped: the server BEFORE the team name, in italics, with no dot; an offline one is dimmed.
        const pre = await ev(`(() => {
            const li = id => [...document.querySelectorAll('#live-sessions-list li')].find(l => l.querySelector('.row-server-prefix') && l.textContent.includes(id));
            const one = (id) => { const l = li(id); const p = l && l.querySelector('.row-server-prefix'); const w = l && l.querySelector('.row-name-with-server');
                return p && { text: w.textContent.replace(p.querySelector('.sr-only').textContent, ''), italic: getComputedStyle(p.querySelector('.row-server-name')).fontStyle,
                    down: p.classList.contains('is-down'), title: p.getAttribute('title'), sr: p.querySelector('.sr-only').textContent.trim(), op: Number(getComputedStyle(p).opacity),
                    first: w.firstElementChild === p }; };
            return { m2: one('team-m2'), m3: one('team-m3'), dots: document.querySelectorAll('#live-sessions-list .row-server-prefix .server-dot, #live-sessions-list .row-server-chip').length };
        })()`);
        assert.equal(pre.m2.text, 'workstation-team-m2', `"server-team" (${JSON.stringify(pre.m2)})`);
        assert.ok(pre.m2.first, 'the server comes before the team name');
        assert.equal(pre.m2.italic, 'italic', 'the server part is in italics');
        assert.equal(pre.m2.down, false); assert.equal(pre.m2.title, 'workstation.lan: Online'); assert.equal(pre.m2.sr, '(server)');
        assert.equal(pre.m3.down, true, 'an offline server is marked'); assert.ok(pre.m3.op < pre.m2.op, `and dimmed (${pre.m3.op} < ${pre.m2.op})`);
        assert.equal(pre.m3.title, 'Old Laptop: Unreachable', 'its state is in the tooltip'); assert.equal(pre.m3.sr, '(server, unreachable)', 'and for screen readers');
        assert.equal(pre.dots, 0, 'no status dot or chip on a row');
        // Team view from a prefixed row opens that server's team.
        await ev(`[...document.querySelectorAll('#live-sessions-list li')].find(l => l.querySelector('.row-server-prefix') && l.textContent.includes('team-m2')).querySelector('.team-open-btn').click(); true`);
        await waitFor(`T.state.selectedTeam === '@m2/team-m2'`, 'team view of @m2/team-m2', 3000, `T.state.selectedTeam`);
        await ev(`window.exitTeamContext({ goBack: false, force: true }); true`);
        console.log('   ✓ ungrouped: "workstation-team-m2", server in italics first, offline dimmed with tooltip + sr text, no dots; team view opens @m2/team-m2');
        // Group by server.
        await ev(`document.getElementById('display-group-by-server').click(); true`);
        await waitFor(`document.querySelectorAll('#live-sessions-list li.server-group-header').length === 3`, 'server headers', 3000);
        assert.equal(await ev(`localStorage.getItem('coral-group-by-server')`), 'true', 'grouping persisted');
        v = await view();
        assert.deepEqual(v.headers.map(h => [h.id, h.name, h.state]), [['m1', 'studio-mac', null], ['m2', 'workstation', null], ['m3', 'Old Laptop', 'unreachable']]);
        assert.deepEqual(v.chips, [], 'no row chips while grouped');
        // #3507 grouped: the header is the outermost level (left of its teams' rows), with a computer
        // icon and no status dot; the offline one is dimmed with its state in the tooltip and label.
        const lay = await ev(`(() => {
            const L = document.getElementById('live-sessions-list');
            const hs = [...L.querySelectorAll('li.server-group-header')];
            const icon = h => h.querySelector('.server-group-icon');
            const rowLefts = [...L.querySelectorAll('li.session-board-card, li.session-group-header, li[data-session-id]')].filter(r => r.getBoundingClientRect().width > 0)
                .map(r => Math.round((r.querySelector('.team-group-icon, .group-name-line, .session-label') || r).getBoundingClientRect().left));
            const m3 = L.querySelector('li.server-group-header[data-server-group="m3"]'), m2 = L.querySelector('li.server-group-header[data-server-group="m2"]');
            return { dots: L.querySelectorAll('li.server-group-header .server-dot').length, icons: hs.filter(icon).length, n: hs.length,
                headerLeft: Math.max(...hs.map(h => Math.round(icon(h).getBoundingClientRect().left))), rowMin: Math.min(...rowLefts),
                m3: { down: m3.classList.contains('is-down'), title: m3.querySelector('.server-group-toggle').title, aria: m3.querySelector('.server-group-toggle').getAttribute('aria-label'),
                    op: Number(getComputedStyle(icon(m3)).opacity), opName: Number(getComputedStyle(m3.querySelector('.server-group-name')).opacity) },
                m2op: Number(getComputedStyle(icon(m2)).opacity),
                // Agent state is untouched: every agent row still has its state dot, with a tooltip.
                agents: [...L.querySelectorAll('li[data-session-id]')].filter(r => r.getBoundingClientRect().width > 0).map(r => ({ id: r.dataset.sessionId, dot: !!r.querySelector('.session-dot'), title: r.querySelector('.session-dot')?.getAttribute('title') || '' })) };
        })()`);
        assert.equal(lay.dots, 0, 'no status dot on a server header'); assert.equal(lay.icons, lay.n, 'every server header has the computer icon');
        assert.ok(lay.headerLeft < lay.rowMin, `server headers sit left of every team and agent row (icon ${lay.headerLeft} < rows ${lay.rowMin})`);
        assert.ok(lay.m3.down && lay.m3.op < lay.m2op && lay.m3.opName < 1, `the offline server is dimmed (${JSON.stringify(lay.m3)})`);
        assert.equal(lay.m3.title, 'Old Laptop: Unreachable'); assert.match(lay.m3.aria, /^Server Old Laptop: Unreachable, 2 agents\. Collapse$/);
        assert.ok(lay.agents.length >= 4 && lay.agents.every(a => a.dot && a.title), `agent state dots kept under server headers (${JSON.stringify(lay.agents)})`);
        console.log(`   ✓ grouped: headers outermost (icon at ${lay.headerLeft}px, rows from ${lay.rowMin}px), computer icon, no dots, offline dimmed + tooltip + aria; ${lay.agents.length} agent rows keep their state dot`);
        const m2rows = ['solo-m2-id', 'team-m2-id'];
        assert.ok(m2rows.every(id => v.rows.includes(id)), 'm2 agents listed while expanded');
        await ev(`document.querySelector('li.server-group-header[data-server-group="m2"] .server-group-toggle').click(); true`);
        await sleep(100);
        v = await view();
        assert.equal(v.headers.find(h => h.id === 'm2').expanded, 'false', 'aria-expanded=false after collapse');
        assert.equal(await ev(`localStorage.getItem('coral-server-collapsed:m2')`), 'true', 'collapse persisted');
        assert.ok(m2rows.every(id => !v.rows.includes(id)), 'a collapsed server shows none of its agents');
        assert.ok(['solo-m1-id', 'solo-m3-id'].every(id => v.rows.includes(id)), 'other servers unaffected');
        // ⋮ details on the offline server.
        await ev(`document.querySelector('li.server-group-header[data-server-group="m3"] details[data-server-details]').open = true; true`);
        const d3 = await ev(`(() => { const p = document.querySelector('li.server-group-header[data-server-group="m3"] .server-switcher-panel'); const dl = Object.fromEntries([...p.querySelectorAll('dt')].map(dt => [dt.textContent, dt.nextElementSibling.textContent])); return { dl, hint: !!p.querySelector('.server-details-hint'), buttons: [...p.querySelectorAll('button')].map(b => b.textContent) }; })()`);
        assert.equal(d3.dl.Status, 'Unreachable'); assert.equal(d3.dl.Error, 'dial tcp: timeout'); assert.ok('Last contact' in d3.dl, 'one wording, Last contact, online or not (#3152)'); assert.ok(d3.hint, 'offline hint');
        assert.deepEqual(d3.buttons, ['Open in Servers', 'Collapse']);
        // Persistence across a reload.
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await scene(THREE);
        v = await view();
        assert.equal(v.headers.length, 3, 'still grouped after reload');
        assert.equal(v.headers.find(h => h.id === 'm2').expanded, 'false', 'm2 still collapsed after reload');
        assert.ok(m2rows.every(id => !v.rows.includes(id)), 'and its agents still hidden');
        await ev(`document.querySelector('li.server-group-header[data-server-group="m2"] .server-group-toggle').click(); true`);
        await sleep(100);
        assert.equal(await ev(`localStorage.getItem('coral-server-collapsed:m2')`), 'false', 'expand persisted too');
        console.log('   ✓ "3 servers", aggregate status, chips by default, headers incl. (unreachable), collapse + reload persistence, ⋮ details');

        console.log('4. Colliding short names stay distinct; panel items say which server needs an update');
        // F-a: two servers whose short names collide are shown by their full label (or a shortest unique form).
        await scene([{ id: 'a', label: 'box.lan', status: 'online' }, { id: 'b', label: 'box.home', status: 'online' }, { id: 'c', label: 'nas.lan', status: 'online' }]);
        await openSwitcher();
        const shown = await ev(`[...document.querySelectorAll('[data-server-switcher-host] button.server-switcher-item')].map(b => ({ id: b.dataset.serverOpen, name: b.querySelector('.server-switcher-name').textContent }))`);
        const byId = Object.fromEntries(shown.map(x => [x.id, x.name]));
        assert.equal(new Set(shown.map(x => x.name)).size, shown.length, `colliding short names are told apart (${JSON.stringify(shown)})`);
        assert.ok(byId.a.startsWith('box') && 'box.lan'.startsWith(byId.a) && byId.a !== 'box', `box.lan shows as its full label or a unique prefix of it (${byId.a})`);
        assert.ok(byId.b.startsWith('box') && 'box.home'.startsWith(byId.b) && byId.b !== 'box', `box.home likewise (${byId.b})`);
        assert.equal(byId.c, 'nas', 'a name that does not collide stays short');
        await ev(`document.querySelector('[data-server-switcher-host] details[data-server-switcher]').open = false; localStorage.setItem('coral-group-by-server', 'true'); T.render.renderLiveSessions(T.state.liveSessions); true`);
        const heads = await ev(`[...document.querySelectorAll('#live-sessions-list li.server-group-header .server-group-name')].map(n => n.textContent)`);
        assert.equal(new Set(heads).size, heads.length, `group headers are told apart too (${JSON.stringify(heads)})`);
        await ev(`localStorage.setItem('coral-group-by-server', 'false'); true`);
        // F-b: the N-servers panel marks the server that needs an update, visibly and in its accessible name.
        await scene([{ ...THREE[0] }, { ...THREE[1], update: 'required', latest_coral: '1.5.0', coral_version: '1.3.9' }, { ...THREE[2], update: 'available', latest_coral: '1.5.0' }]);
        await openSwitcher();
        const its = await ev(`[...document.querySelectorAll('[data-server-switcher-host] button.server-switcher-item')].map(b => ({ id: b.dataset.serverOpen, marker: b.querySelector('.server-update-marker')?.className || null, markerText: b.querySelector('.server-update-marker')?.textContent || null, aria: b.getAttribute('aria-label') }))`);
        const it = Object.fromEntries(its.map(x => [x.id, x]));
        assert.equal(it.m2.marker, 'server-update-marker server-update-required', `m2 item marked (${JSON.stringify(it.m2)})`);
        assert.equal(it.m2.markerText, 'Update required');
        assert.match(it.m2.aria, /\. Update required(\.|$)/, `m2 aria says so (${it.m2.aria})`);
        assert.equal(it.m3.marker, 'server-update-marker server-update-available');
        assert.match(it.m3.aria, /\. Update available(\.|$)/, `m3 aria says so (${it.m3.aria})`);
        assert.equal(it.m1.marker, null, 'no marker on an up-to-date server');
        assert.ok(!/Update/.test(it.m1.aria), 'and nothing in its aria-label');
        await ev(`document.querySelector('[data-server-switcher-host] details[data-server-switcher]').open = false; localStorage.setItem('coral-group-by-server', 'false'); true`);
        console.log(`   ✓ colliding names ${JSON.stringify(shown.map(x => x.name))}; items marked required/available with aria`);

        console.log('5. Phone 375 px with a coarse pointer: no overflow, 44 px targets');
        await Emulation.setDeviceMetricsOverride({ width: 375, height: 812, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await Emulation.setEmitTouchEventsForMouse({ enabled: true, configuration: 'mobile' });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.setItem('coral-group-by-server', 'true'); true`);
        await scene(THREE);
        await ev(`(async () => { const mob = await import((window.__coralStatic || '/static/') + 'mobile.js'); mob.syncMobileAgentList(); return true; })()`);
        await waitFor(`!!document.querySelector('#mobile-agent-list [data-server-switcher-host] summary')`, 'phone server button', 3000);
        await ev(`document.querySelector('#mobile-agent-list [data-server-switcher-host] details').open = true; true`);
        const phone = await ev(`(() => {
            const coarse = matchMedia('(pointer: coarse)').matches;
            const sizes = [...document.querySelectorAll('#mobile-agent-list [data-server-switcher-host] summary, #mobile-agent-list .server-switcher-item, #mobile-agent-list .server-group-toggle, #mobile-agent-list .server-group-more-btn')]
                .filter(el => el.getBoundingClientRect().width > 0)
                .map(el => ({ cls: el.className, h: Math.round(el.getBoundingClientRect().height), w: Math.round(el.getBoundingClientRect().width) }));
            return { coarse, overflow: document.documentElement.scrollWidth - innerWidth, small: sizes.filter(s => s.h < 44 || s.w < 44), count: sizes.length };
        })()`);
        assert.ok(phone.coarse, 'coarse pointer emulated');
        assert.ok(phone.count >= 7, `phone: the button, its items and the headers' controls are present (${phone.count})`);
        assert.equal(phone.overflow, 0, 'phone: no horizontal overflow');
        assert.deepEqual(phone.small, [], 'phone: every server control is at least 44 x 44');
        await Emulation.setTouchEmulationEnabled({ enabled: false });
        await Emulation.clearDeviceMetricsOverride();
        console.log(`   ✓ ${phone.count} controls ≥ 44 px, overflow 0`);

        console.log('6. Top tabs in the operator\'s order (#3507): Agents, Servers, then the local tabs, Docs last');
        // The scene fakes a hub with servers, so let the page recompute the Servers tab as a live update would.
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'servers_tab.js'); m.updateServersTabVisibility(); return true; })()`);
        const tabs = await ev(`(() => { const b = [...document.querySelectorAll('.top-nav-tabs .top-nav-tab')]; return { dom: b.map(x => x.id),
            shown: b.filter(x => !x.hidden && getComputedStyle(x).display !== 'none').map(x => x.textContent.trim()) }; })()`);
        assert.deepEqual(tabs.dom.slice(0, 2), ['nav-tab-agents', 'nav-tab-servers'], `Servers right after Agents (${tabs.dom})`);
        assert.ok(tabs.dom.indexOf('nav-tab-docs') > tabs.dom.indexOf('nav-tab-tokens'), 'Docs after the local tabs');
        assert.deepEqual(tabs.shown.filter(t => ['Agents', 'Servers', 'Docs'].includes(t)), ['Agents', 'Servers', 'Docs'], `shown in that order (${tabs.shown})`);
        console.log(`   ✓ ${tabs.shown.join(', ')}`);

        console.log('\nAll server switcher checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
