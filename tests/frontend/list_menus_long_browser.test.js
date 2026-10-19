// The agent list's menus open at their button in a long, scrolled list (#5149; found when content-visibility on the groups made each group the containing block of position: fixed, and the row
// and group menus opened 380 px away with the first item out of reach). 48 agents in 8 teams, the list scrolled to its middle and to its end: an agent row's menu button and a team header's
// menu button each open a menu whose vertical span meets the button's (under it, over it, or beside it when it is too tall for the room), that lies inside the window, and whose first item is what a click at its centre
// reaches. Escape closes it. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a layout settle after a scroll; there is no event for it
    const key = async (k, code) => { await client.Input.dispatchKeyEvent({ type: 'keyDown', key: k, code: k, windowsVirtualKeyCode: code }); await client.Input.dispatchKeyEvent({ type: 'keyUp', key: k, code: k, windowsVirtualKeyCode: code }); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { T.state.liveSessions = []; window.__m.agents = {}; for (let i = 0; i < 48; i++) window.__m.add('agent-' + String(i).padStart(2, '0'), 'A', 3, { sleeping: false });
            T.state.liveSessions.forEach((s, i) => { s.board_name = 'team-' + Math.floor(i / 6); s.board_project = s.board_name; }); T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
        await waitFor(`document.querySelectorAll('#live-sessions-list > li').length >= 8`, 'the groups', 8000);
        await sleep(500); // sleep-ok: a layout settle; there is no event for it
        const scrollTo = frac => ev(`(() => { const l = document.getElementById('live-sessions-list'); let s = l; while (s && s.scrollHeight <= s.clientHeight + 1) s = s.parentElement; if (!s) return null; s.scrollTop = Math.round((s.scrollHeight - s.clientHeight) * ${frac}); return s.scrollTop; })()`);
        const SEL = `const lis = [...document.querySelectorAll('#live-sessions-list > li')]; const li = lis[(PICK)(lis)]; const btn = WHERE;`;
        const open = async (where, pick) => {
            const sel = SEL.replace('PICK', pick).replace('WHERE', where);
            const found = await ev(`(() => { ${sel} if (!btn) return false; btn.scrollIntoView({ block: 'center' }); return true; })()`);
            if (!found) return { missing: true };
            await sleep(150); // sleep-ok: the scroll settles before the click
            await ev(`(() => { ${sel} btn.click(); return true; })()`);
            await sleep(150); // sleep-ok: the menu is placed after the click
            return ev(`(() => { ${sel} const menu = btn.closest('.sidebar-kebab-wrap, .group-kebab, li, div')?.querySelector('.overflow-menu, [role=menu]') || btn.nextElementSibling; if (!menu) return { missing: 'menu' };
                const b = btn.getBoundingClientRect(), m = menu.getBoundingClientRect(); const item = menu.querySelector('.overflow-menu-item, [role=menuitem]'); const ir = item ? item.getBoundingClientRect() : null;
                const hit = ir ? document.elementFromPoint(ir.left + ir.width / 2, ir.top + ir.height / 2) : null;
                return { btn: [Math.round(b.top), Math.round(b.bottom)], menu: [Math.round(m.top), Math.round(m.bottom), Math.round(m.left), Math.round(m.right)], shown: m.width > 0 && getComputedStyle(menu).display !== 'none', hit: !!hit && menu.contains(hit), inside: m.top >= 0 && m.bottom <= innerHeight + 1 && m.left >= 0 && m.right <= innerWidth + 1 }; })()`);
        };
        for (const [name, frac, pick] of [['the middle', 0.5, 'l => Math.floor(l.length / 2)'], ['the end', 1, 'l => l.length - 1']]) {
            await scrollTo(frac); await sleep(300); // sleep-ok: a layout settle; there is no event for it
            for (const [what, where] of [['an agent row', `li.querySelector('li.session-group-item .sidebar-kebab-btn')`], ['a team header', `li.querySelector('.session-group-header .sidebar-kebab-btn, .group-kebab .sidebar-kebab-btn')`]]) {
                const r = await open(where, pick);
                const near = r.btn && r.menu && r.menu[0] <= r.btn[1] + 40 && r.menu[1] >= r.btn[0] - 40; // the menu's span meets the button's: under it, over it, or (a tall menu that must fit the window) beside it
                check(`list scrolled to ${name}: ${what}'s menu opens at its button, inside the window, and its first item takes the click`, !r.missing && r.shown && near && r.inside && r.hit, JSON.stringify(r));
                await key('Escape', 27); await sleep(200); // sleep-ok: a layout settle; there is no event for it
                check(`list scrolled to ${name}: Escape closes ${what}'s menu`, await ev(`![...document.querySelectorAll('#live-sessions-list .overflow-menu, #live-sessions-list [role=menu]')].some(m => m.getBoundingClientRect().width > 0 && getComputedStyle(m).display !== 'none')`));
            }
        }
    } catch (e) {
        console.error(e && e.stack || e);
        results.push(false);
    } finally { try { await client.close(); } catch (_) {} }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS list menus in a long list: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
