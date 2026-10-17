// The collapsed sidebar (the icon rail) looks like the expanded list (operator, 2026-10-17; #4184): the same
// agents are rendered both ways and the rail's computed colours are compared with the list's own.
// - a team chip has no fill and its letter is the colour of the team's header in the list (neutral, or
//   the colour the user picked for the team); a folder chip the same as its header;
// - an agent tile has no tile of its own, and its initials are the colour of that agent's name in its
//   row (the plain name colour, or the name colour the user picked, deepened in the light theme);
// - the selected agent's tile has the selected row's background and its name's colour.
// Both themes, switched as the app does (applyTheme). Run against an isolated dev server.
// CORAL_SHOT_DIR, when set, gets the list and the rail for the same team in each theme.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 2, mobile: false });
        const shot = async (selector, name) => {
            if (!process.env.CORAL_SHOT_DIR) return;
            const rect = await ev(`(() => { const b = document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect(); return { x: b.x, y: b.y, width: b.width, height: Math.min(b.height, 420) }; })()`);
            const png = await Page.captureScreenshot({ format: 'png', clip: { ...rect, scale: 1 } });
            fs.writeFileSync(path.join(process.env.CORAL_SHOT_DIR, name), Buffer.from(png.data, 'base64'));
        };
        for (const theme of ['dark', 'light']) {
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars(); m.applyTheme(${JSON.stringify(theme)}); return true; })()`);
            await ev(`(() => {
                // The tiles and rows fade their colours: read the end state, not a frame of the fade.
                const still = document.createElement('style'); still.textContent = '* { transition: none !important; }'; document.head.appendChild(still);
                localStorage.removeItem('coral_collapsed_groups');
                localStorage.setItem('coral-group-by-team', 'true');
                window.__m.agents = {};
                const add = (name, sid, extra = {}) => window.__m.add(name, 'A', 1, { sleeping: false, session_id: sid, ...extra });
                add('lead', 'sid-p1', { board_project: 'plain-team' }); add('qa', 'sid-p2', { board_project: 'plain-team' });
                add('ux', 'sid-c1', { board_project: 'picked-team' });
                add('solo', 'sid-f1');
                const live = Object.values(window.__m.agents).map(x => window.__m.entry(x));
                live.find(s => s.session_id === 'sid-p1').name_color = '#bf616a';
                window.__RAIL_LIVE = live;
                return true;
            })()`);
            // The user picks a colour for picked-team, under the key both views use (the header's data-group).
            const keys = await ev(`(() => {
                if (document.querySelector('.layout').classList.contains('sidebar-collapsed')) window.toggleSidebarRail();
                T.state.liveSessions = window.__RAIL_LIVE; T.render.renderLiveSessions(window.__RAIL_LIVE);
                return [...document.querySelectorAll('#live-sessions-list .session-group-header[data-group]')].map(h => h.dataset.group);
            })()`);
            const picked = keys.find(k => /picked-team/.test(k)), plain = keys.find(k => /plain-team/.test(k)), folder = keys.find(k => !/team/.test(k));
            assert.ok(picked && plain && folder, `${theme}: a header per team and the folder: ${keys}`);
            await ev(`localStorage.setItem('coral-board-colors', JSON.stringify({ ${JSON.stringify(picked)}: '#d08770' })); true`);
            // One synchronous step per view: render (with the name colour and the selection) and read, so
            // the page's own poll (whose mock entries carry no name colour) cannot re-render in between.
            const read = (rail) => ev(`(() => {
                const layout = document.querySelector('.layout');
                if (layout.classList.contains('sidebar-collapsed') !== ${rail}) window.toggleSidebarRail();
                T.state.currentSession = { type: 'live', name: 'qa', agent_type: 'claude', session_id: 'sid-p2' };
                T.state.liveSessions = window.__RAIL_LIVE; T.render.renderLiveSessions(window.__RAIL_LIVE);
                const cs = e => e ? getComputedStyle(e) : null;
                const keys = ${JSON.stringify({ picked, plain, folder })};
                const out = { groups: {}, agents: {} };
                if (${rail}) {
                    const chip = k => [...document.querySelectorAll('#sidebar-rail-list .sidebar-rail-group')].find(b => b.dataset.group === k);
                    for (const [n, k] of Object.entries(keys)) { const c = cs(chip(k)); out.groups[n] = c && { color: c.color, bg: c.backgroundColor }; }
                    for (const sid of ['sid-p1', 'sid-p2', 'sid-c1']) {
                        const t = cs(document.querySelector('#sidebar-rail-list .sidebar-rail-item[data-session-id="' + sid + '"] .rail-tile'));
                        out.agents[sid] = t && { color: t.color, bg: t.backgroundColor, border: t.borderTopWidth };
                    }
                } else {
                    const head = k => document.querySelector('#live-sessions-list .session-group-header[data-group="' + CSS.escape(k) + '"] .group-name-line');
                    for (const [n, k] of Object.entries(keys)) { const c = cs(head(k)); out.groups[n] = c && { color: c.color }; }
                    for (const sid of ['sid-p1', 'sid-p2', 'sid-c1']) {
                        const li = document.querySelector('#live-sessions-list li[data-session-id="' + sid + '"]');
                        out.agents[sid] = li && { color: cs(li.querySelector('.session-label-name')).color, bg: cs(li).backgroundColor };
                    }
                }
                return out;
            })()`);
            const list = await read(false);
            await shot('#live-sessions-list', `rail_colors_${theme}_list.png`);
            const rail = await read(true);
            await shot('#sidebar-rail-list', `rail_colors_${theme}_rail.png`);
            const clear = 'rgba(0, 0, 0, 0)';
            for (const g of ['plain', 'picked', 'folder']) {
                assert.ok(list.groups[g] && rail.groups[g], `${theme}: ${g} is in both views`);
                assert.equal(rail.groups[g].color, list.groups[g].color, `${theme}: the ${g} chip's letter is its list header's colour`);
                assert.equal(rail.groups[g].bg, clear, `${theme}: the ${g} chip has no fill`);
            }
            assert.notEqual(list.groups.picked.color, list.groups.plain.color, `${theme}: the picked team colour shows in the list (and so in the rail)`);
            for (const sid of ['sid-p1', 'sid-p2', 'sid-c1']) {
                assert.equal(rail.agents[sid].color, list.agents[sid].color, `${theme}: ${sid}'s initials are its name's colour in the list`);
                assert.equal(rail.agents[sid].border, '0px', `${theme}: ${sid}'s tile has no border`);
            }
            assert.equal(rail.agents['sid-p2'].bg, list.agents['sid-p2'].bg, `${theme}: the selected tile has the selected row's background`);
            assert.equal(rail.agents['sid-p1'].bg, clear, `${theme}: an unselected tile has no fill`);
            assert.notEqual(list.agents['sid-p1'].color, list.agents['sid-c1'].color, `${theme}: the picked name colour differs from the plain one`);
            await ev(`localStorage.removeItem('coral-board-colors'); localStorage.removeItem('coral-group-by-team'); localStorage.removeItem('coral-theme'); true`);
        }
        console.log('PASS rail colours: chips, tiles and the selected tile match the expanded list (team, picked team, folder; plain, picked and selected names); dark and light');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
