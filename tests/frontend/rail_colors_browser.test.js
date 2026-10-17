// The collapsed sidebar (the icon rail) uses the expanded list's colours (operator, 2026-10-17; #3507):
// a team's chip is neutral unless the user picked the team's colour, and then it shows that colour;
// folders are neutral too; an agent's tile shows the name colour the user picked for it, deepened in
// the light theme as the agent's row is, and the default text colour otherwise. Before, every team got
// a colour hashed from its name and every folder one from the agent palette. Both themes. Run against
// an isolated dev server. CORAL_SHOT_DIR, when set, gets a screenshot of the rail in each theme.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 2, mobile: false });
        for (const theme of ['dark', 'light']) {
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            // The app's own theme switch: a theme's colours are inline variables on <html>, so data-theme alone mixes two themes.
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars(); m.applyTheme(${JSON.stringify(theme)}); return true; })()`);
            const v = await ev(`(() => {
                // The tiles fade their colours (0.12 s): read the end state, not a frame of the fade.
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
                window.__RAIL_LIVE = live; T.state.liveSessions = live;
                if (!document.querySelector('.layout').classList.contains('sidebar-collapsed')) window.toggleSidebarRail();
                T.render.renderLiveSessions(live);
                return true;
            })()`);
            assert.ok(v);
            // The user picks a colour for picked-team, keyed as the full list keys it (the header's data-group).
            const keys = await ev(`[...document.querySelectorAll('#sidebar-rail-list .sidebar-rail-group')].map(b => b.dataset.group)`);
            const picked = keys.find(k => /picked-team/.test(k));
            const plain = keys.find(k => /plain-team/.test(k));
            const folder = keys.find(k => !/team/.test(k));
            assert.ok(picked && plain && folder, `${theme}: a chip per team and the folder: ${keys}`);
            await ev(`(() => { localStorage.setItem('coral-board-colors', JSON.stringify({ ${JSON.stringify(picked)}: '#d08770' })); T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
            await waitFor(`!!document.querySelector('#sidebar-rail-list .sidebar-rail-group.has-accent')`, `${theme}: the picked team's chip`);
            // One synchronous step: render the list (its name colours too) and read it, so the page's own poll
            // (whose mock entries carry no name colour) cannot re-render in between.
            const r = await ev(`(() => {
                T.state.liveSessions = window.__RAIL_LIVE; T.render.renderLiveSessions(window.__RAIL_LIVE);
                const cs = e => getComputedStyle(e);
                const chip = k => [...document.querySelectorAll('#sidebar-rail-list .sidebar-rail-group')].find(b => b.dataset.group === k);
                const tile = sid => document.querySelector('#sidebar-rail-list .sidebar-rail-item[data-session-id="' + sid + '"] .rail-tile');
                const probe = document.createElement('div'); document.body.appendChild(probe);
                const resolve = c => { probe.style.color = ''; probe.style.color = c; const v = cs(probe).color; return v; };
                const root = cs(document.documentElement);
                const out = {
                    neutralBg: resolve(root.getPropertyValue('--rail-tile-bg-hover').trim()),
                    tileText: resolve(root.getPropertyValue('--rail-tile-text').trim()),
                    deepened: resolve('color-mix(in srgb, #bf616a 62%, black)'),
                    accent: resolve('#d08770'), named: resolve('#bf616a'),
                    plain: { bg: cs(chip(${JSON.stringify(plain)})).backgroundColor, fg: cs(chip(${JSON.stringify(plain)})).color, cls: chip(${JSON.stringify(plain)}).className },
                    folder: { bg: cs(chip(${JSON.stringify(folder)})).backgroundColor, fg: cs(chip(${JSON.stringify(folder)})).color, cls: chip(${JSON.stringify(folder)}).className },
                    picked: { bg: cs(chip(${JSON.stringify(picked)})).backgroundColor, cls: chip(${JSON.stringify(picked)}).className },
                    namedTile: cs(tile('sid-p1')).color, plainTile: cs(tile('sid-p2')).color,
                };
                probe.remove();
                return out;
            })()`);
            for (const g of ['plain', 'folder']) {
                assert.ok(!/has-accent/.test(r[g].cls), `${theme}: the ${g} chip has no colour of its own: ${r[g].cls}`);
                assert.equal(r[g].bg, r.neutralBg, `${theme}: the ${g} chip is neutral`);
                assert.equal(r[g].fg, r.tileText, `${theme}: the ${g} chip's letter is the tiles' text colour`);
            }
            assert.equal(r.picked.bg, r.accent, `${theme}: the team with a picked colour shows it`);
            assert.equal(r.namedTile, theme === 'light' ? r.deepened : r.named, `${theme}: an agent's picked name colour colours its tile${theme === 'light' ? ', deepened' : ''}`);
            assert.equal(r.plainTile, r.tileText, `${theme}: an agent without one keeps the tile's text colour`);
            if (process.env.CORAL_SHOT_DIR) {
                const rect = await ev(`(() => { T.render.renderLiveSessions(window.__RAIL_LIVE); const b = document.getElementById('sidebar-rail-list').getBoundingClientRect(); return { x: b.x, y: b.y, width: b.width, height: Math.min(b.height, 400) }; })()`);
                const shot = await Page.captureScreenshot({ format: 'png', clip: { ...rect, scale: 1 } });
                fs.writeFileSync(path.join(process.env.CORAL_SHOT_DIR, `rail_colors_${theme}.png`), Buffer.from(shot.data, 'base64'));
            }
            await ev(`localStorage.removeItem('coral-board-colors'); localStorage.removeItem('coral-group-by-team'); localStorage.removeItem('coral-theme'); true`);
        }
        console.log('PASS rail colours: neutral team and folder chips, a picked team colour, picked agent name colours (deepened in light); dark and light');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
