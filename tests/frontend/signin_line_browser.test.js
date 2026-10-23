// The one calm line while Claude is not signed in (#5478, for #5469): when GET /api/system/status carries claude_signed_in false, the dashboard shows "Claude is not signed in:
// background jobs are waiting" in the status area, with "Sign in with claude in a terminal." on hover (title) and on tap (the button opens it); it is a line, not an alert or a toast,
// it goes by itself when the field turns true (the page asks again every minute), and it is not there while the field is absent. Both themes (contrast), and a phone.
// The status answer is the real server's with the field added by the page's fetch (set by window.__signed). Run against an isolated dev server.
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');

const PRELOAD = `(() => {
    window.__signed = false; // false, true or 'absent'
    const R = window.fetch;
    window.fetch = async function (u, o) {
        const resp = await R.apply(this, arguments);
        let p = ''; try { p = new URL(String(u && u.url || u), location.origin).pathname; } catch { /* not a URL */ }
        if (p !== '/api/system/status' || !resp.ok) return resp;
        const data = await resp.clone().json();
        if (window.__signed !== 'absent') data.claude_signed_in = window.__signed;
        return new Response(JSON.stringify(data), { status: 200, headers: { 'content-type': 'application/json' } });
    };
    const si = window.setInterval; window.__ticks = []; window.setInterval = function (f, ms, ...r) { if (ms === 60000) window.__ticks.push(f); return si.call(this, f, ms, ...r); };
})()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor } = await connect();
    const check = (label, ok, detail) => { console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); if (!ok) process.exitCode = 1; };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const state = () => ev(`(() => { const l = document.getElementById('claude-signin-line'), b = document.getElementById('signin-line-btn'), h = document.getElementById('signin-line-hint');
            const r = l.getBoundingClientRect();
            return { shown: !l.hidden && l.getClientRects().length > 0, text: l.querySelector('button').textContent.replace(/\\s+/g, ' ').trim(), title: b.title, role: l.getAttribute('role'), hintShown: !h.hidden, expanded: b.getAttribute('aria-expanded'),
                alerts: document.querySelectorAll('#claude-signin-line [role=alert], .toast').length, color: getComputedStyle(l).color, errorColor: getComputedStyle(document.documentElement).getPropertyValue('--error').trim(),
                fits: r.left >= 0 && r.right <= innerWidth, overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
        const load = async (view, signed) => {
            await Emulation.setDeviceMetricsOverride({ width: view.width, height: view.height, deviceScaleFactor: 1, mobile: view.mobile });
            await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: view.theme }] });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired();
            await ev(`window.__signed = ${JSON.stringify(signed)}; true`);
            await waitFor(`!document.getElementById('startup-loading') || document.getElementById('startup-loading').classList.contains('hidden')`, 'the page to start', 20000);
            await installAudit(ev); await setTheme(ev, view.theme);
        };

        for (const view of [{ name: 'desktop, dark', width: 1280, height: 900, mobile: false, theme: 'dark' }, { name: 'desktop, light', width: 1280, height: 900, mobile: false, theme: 'light' }, { name: 'phone 390, dark', width: 390, height: 844, mobile: true, theme: 'dark' }, { name: 'phone 390, light', width: 390, height: 844, mobile: true, theme: 'light' }]) {
            console.log(`${view.name}: the line is there while the field is false, calm, readable, and goes when it turns true`);
            // the field is false from the start: the page's first status answer shows the line
            await Emulation.setDeviceMetricsOverride({ width: view.width, height: view.height, deviceScaleFactor: 1, mobile: view.mobile });
            await load(view, false);
            await waitFor(`!document.getElementById('claude-signin-line').hidden`, 'the line from the first status answer');
            let s = await state();
            check(`${view.name}: "${s.text}"`, s.shown && s.text === "info Claude isn't signed in: scheduled jobs and workflows that use it are waiting");
            check('hover says what to do (title)', s.title === 'Sign in with claude in a terminal');
            check('a calm line: status role, no alert, no toast, not the error colour', s.role === 'status' && s.alerts === 0 && s.color !== s.errorColor);
            check('the hint is shut until it is tapped', s.hintShown === false && s.expanded === 'false');
            await ev(`document.getElementById('signin-line-btn').click(); true`);
            s = await state();
            check('a tap opens "Sign in with claude in a terminal."', s.hintShown === true && s.expanded === 'true' && /Sign in with claude in a terminal\./.test(await ev(`document.getElementById('signin-line-hint').textContent`)));
            check('it fits the screen (no horizontal overflow)', s.fits && s.overflow === 0, `overflow ${s.overflow}`);
            const audit = await auditRegion(ev, ['#claude-signin-line'], { focus: ['#signin-line-btn'] });
            check(`both themes: readable (4.5:1) and a visible focus ring (${audit.checked} checked)`, audit.fails.length === 0 && audit.checked >= 2, JSON.stringify(audit.fails));
            // it names what waits, it is only where that matters, and it can be dismissed for the day (#6200)
            const where = async tab => { await ev(`window.switchNavTab(${JSON.stringify(tab)}); true`); await new Promise(r => setTimeout(r, 200)); return ev(`getComputedStyle(document.getElementById('claude-signin-line')).display !== 'none' && !document.getElementById('claude-signin-line').hidden`); }; // sleep-ok: the tab's page settles
            const seen = {}; for (const tab of ['agents', 'jobs', 'workflows', 'history', 'board', 'docs', 'tokens', 'hub-inbox']) seen[tab] = await where(tab);
            check('shown on Agents, Jobs and Workflows only: not on Chats, Board, Docs, Analytics or Inbox', seen.agents && seen.jobs && seen.workflows && !seen.history && !seen.board && !seen.docs && !seen.tokens && !seen['hub-inbox'], JSON.stringify(seen));
            await where('agents');
            check('it has a Dismiss button, named, and a touch target on a phone', await ev(`(() => { const b = document.getElementById('signin-line-dismiss'); const r = b.getBoundingClientRect(); return b.getAttribute('aria-label') === 'Dismiss the Claude sign-in note' && r.width >= (${view.mobile ? 44 : 20}) && r.height >= (${view.mobile ? 44 : 20}); })()`));
            await ev(`document.getElementById('signin-line-dismiss').click(); true`);
            check('Dismiss hides it, and it stays away after the next look (a day)', (await state()).shown === false && (await ev(`window.__ticks.forEach(f => f()); new Promise(r => setTimeout(r, 400)).then(() => document.getElementById('claude-signin-line').hidden)`)) === true); // sleep-ok: the status read lands
            await ev(`localStorage.removeItem('coral-signin-line-dismissed'); window.__ticks.forEach(f => f()); true`);
            await waitFor(`!document.getElementById('claude-signin-line').hidden`, 'the line to come back after the dismissal ends');
            // the field turns true: the next look hides it (and a tap's hint with it)
            await ev(`window.__signed = true; window.__ticks.forEach(f => f()); true`);
            await waitFor(`document.getElementById('claude-signin-line').hidden`, 'the line to go');
            s = await state();
            check('it goes by itself when the field turns true', s.shown === false && s.hintShown === false);
            await ev(`window.__signed = false; window.__ticks.forEach(f => f()); true`);
            await waitFor(`!document.getElementById('claude-signin-line').hidden`, 'the line to come back');
            check('and comes back when it turns false again, with the hint shut', (await state()).hintShown === false);
            await ev(`window.__signed = 'absent'; window.__ticks.forEach(f => f()); true`);
            await waitFor(`document.getElementById('claude-signin-line').hidden`, 'the line to go for an absent field');
            check('an absent field is "not known": no line', (await state()).shown === false);
        }
    } finally { try { await client.close(); } catch (_) {} }
})().catch(e => { console.error(e); process.exit(1); });
