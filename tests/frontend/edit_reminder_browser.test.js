// Edit reminder opens with what is in force (#6329, #6352): the Team view's Edit reminder asks the person-only route GET /api/board/{team}/reminder?subscriber_id=... and opens the dialog with the stored instruction (shown as one line) and
// the exact seconds as minutes (30 s is 0.5, 150 s is 2.5); anything but a 200 that says active (a refusal, active false, a broken body, a lost connection) means no text: the box opens empty with the interval the Team view has. Remind agent (no reminder
// yet) asks nothing. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
        const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        const team = 'er-team-' + Date.now();
        await ev(`(() => { const R = window.fetch; window.__reminderReads = []; window.__reminder = { status: 200, body: { active: true, subscriber_id: 'qa', message: 'Check the build\\nthen  report', interval_seconds: 150 } };
            const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const p = url.pathname;
                if (p.startsWith('/api/board/') && p.endsWith('/reminder') && (o.method || 'GET').toUpperCase() === 'GET') {
                    window.__reminderReads.push(url.search);
                    if (window.__reminder === 'network') return Promise.reject(new TypeError('Failed to fetch'));
                    if (window.__reminder === 'broken') return Promise.resolve(new Response('<html>', { status: 200, headers: { 'content-type': 'text/html' } }));
                    return json(window.__reminder.body, window.__reminder.status); }
                if (p.endsWith('/status') && p.includes('/api/board/')) return json({ agents: [
                    { name: 'Backend', subscriber_id: 'be', agent_type: 'claude', role: 'Backend Dev', availability: 'available', available: true, reason: 'Idle.', tasks: [] },
                    { name: 'QA', subscriber_id: 'qa', agent_type: 'claude', role: 'QA Engineer', availability: 'busy', available: false, reason: 'Working.', reminder: true, reminder_interval_seconds: 300, tasks: [] }],
                    summary: { available: 1, busy: 1 }, unassigned_tasks: [], health_report: ['All good.'], observed_at: new Date().toISOString() });
                return R(u, o); };
            return true; })()`);
        await ev(`(async () => { const m = await import(${S} + 'team_availability.js'); await m.showTeamAvailability(${JSON.stringify(team)}); return true; })()`);
        await waitFor(`!!document.querySelector('#team-availability-dialog .availability-agent')`, 'the Team view', 8000);
        await waitFor(`[...document.querySelectorAll('#team-availability-dialog .availability-agent button')].some(b => b.textContent === 'Edit reminder')`, 'the reminder buttons', 8000);
        // the Team view redraws its cards now and then, so a button is found and clicked in one step
        const clickButton = text => ev(`(async () => { for (let i = 0; i < 60; i++) { const b = [...document.querySelectorAll('#team-availability-dialog .availability-agent button')].find(x => x.textContent === ${JSON.stringify(text)}); if (b) { b.click(); return true; } await new Promise(r => setTimeout(r, 50)); } return false; })()`);
        const dialog = () => ev(`(() => { const d = document.getElementById('reminder-dialog'); if (!d || !d.open) return null; return { title: d.querySelector('h3').textContent, message: (d.querySelector('#reminder-message') || {}).value, minutes: d.querySelector('#reminder-minutes').value, submit: d.querySelector('.reminder-go').textContent }; })()`);
        const close = async () => { await ev(`document.querySelector('#reminder-dialog .reminder-cancel').click(); true`); await sleep(250); }; // sleep-ok: the dialog closes over a frame

        await clickButton('Remind agent');
        await waitFor(`document.getElementById('reminder-dialog')?.open === true`, 'the dialog for Remind agent', 4000);
        let d = await dialog();
        check('Remind agent (no reminder yet) asks for nothing and opens empty at 5 minutes', (await ev('window.__reminderReads.length')) === 0 && d.message === '' && d.minutes === '5', JSON.stringify([d.message, d.minutes]));
        await close();

        await clickButton('Edit reminder');
        await waitFor(`document.getElementById('reminder-dialog')?.open === true`, 'the dialog for Edit reminder', 4000);
        d = await dialog();
        check('Edit reminder asks the route for that agent only, and opens with the stored instruction as one line and the exact minutes (150 s is 2.5)', (await ev('window.__reminderReads')).join() === '?subscriber_id=qa' && d.message === 'Check the build then report' && d.minutes === '2.5' && /Edit the reminder for QA/.test(d.title) && d.submit === 'Save reminder', JSON.stringify([await ev('window.__reminderReads'), d]));
        await close();

        for (const [what, value, wantMinutes] of [
            ['a refusal (403)', { status: 403, body: { error: 'operator_only' } }, '5'],
            ['active false', { status: 200, body: { active: false } }, '5'],
            ['another status', { status: 404, body: {} }, '5'],
            ['a body that is not JSON', 'broken', '5'],
            ['a lost connection', 'network', '5'],
            ['an answer without a text', { status: 200, body: { active: true, interval_seconds: 30 } }, '5'],
            ['active false with a stale text in the body', { status: 200, body: { active: false, message: 'stale', interval_seconds: 30 } }, '5'],
            ['a 500 whose body looks like an active reminder', { status: 500, body: { active: true, message: 'not stored', interval_seconds: 30 } }, '5'],
        ]) {
            await ev(`window.__reminder = ${JSON.stringify(value)}; true`);
            await clickButton('Edit reminder');
            await waitFor(`document.getElementById('reminder-dialog')?.open === true`, 'the dialog', 4000);
            d = await dialog();
            check(`${what}: no text to show, so the box is empty and the minutes are the Team view's 5 (300 s)`, d.message === '' && d.minutes === wantMinutes, JSON.stringify(d));
            await close();
        }
        await ev(`window.__reminder = { status: 200, body: { active: true, subscriber_id: 'qa', message: 'x', interval_seconds: 30 } }; true`);
        await clickButton('Edit reminder');
        await waitFor(`document.getElementById('reminder-dialog')?.open === true`, 'the dialog', 4000);
        d = await dialog();
        check('30 seconds opens as 0.5 minutes, not 1', d.minutes === '0.5' && d.message === 'x', JSON.stringify(d));
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} edit reminder checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
