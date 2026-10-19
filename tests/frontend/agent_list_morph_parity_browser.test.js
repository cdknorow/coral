// The agent list's incremental DOM update (dom_morph.js, #3121/#3134) must show exactly what a plain innerHTML
// render shows, through the things a user does, not only feed changes (agent_list_morph covers those).
//
// One scripted session runs twice in a fresh page: once morphing, once with the kill switch (localStorage
// 'coral-no-morph' = '1'). 30 agents in three teams and a folder group, some sleeping, two archived, grouped by
// team. Steps: search and clear, collapse and expand a group, expand sleeping, open archived, select an agent,
// Move Down, drag one row below another, feed ticks (with <b>&"' in summaries), group by team off and on, a
// removal and an addition. After every step:
//   - the live list equals a fresh parse of the markup that render wrote (lastLiveListHtml), and
//   - the visible rows (in order) and the active row are the same in both runs.
// Then the same page at phone width (390 px, where the sidebar's list element sits in the phone's page as #mobile-session-list): a few
// feed ticks, and the phone list must be non-empty and the same in both runs.
//
// Drives Chrome over CDP (chrome-remote-interface); run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const agent = (i, o = {}) => Object.assign({ name: 'proj', display_name: 'Agent ' + i, agent_type: 'claude', session_id: 'sid-' + i,
    working_directory: '/repo/proj', summary: 'goal ' + i, status: 'idle', sleeping: false, archived: false, board_project: null,
    context_pct: 10 + i, working: false, waiting_for_input: false }, o);
const SESSIONS = [];
for (let i = 0; i < 30; i++) {
    const team = i < 10 ? 'team-a' : i < 20 ? 'team-b' : i < 25 ? 'team-c' : null;
    SESSIONS.push(agent(i, { board_project: team, board_job_title: team ? 'Role ' + i : undefined, name: team || 'proj',
        sleeping: i % 7 === 3, archived: i === 28 || i === 29 }));
}

// The list comes from this fixture only: /api/sessions/live answers it and the live feed socket never opens.
const STUB = `
  window.__sessions = ${JSON.stringify(SESSIONS)};
  (function () { const Real = window.WebSocket; function Dead(u) { this.url = String(u); this.readyState = 0; }
    Dead.prototype.send = function () {}; Dead.prototype.close = function () {}; Dead.prototype.addEventListener = function () {}; Dead.prototype.removeEventListener = function () {};
    function W(u, p) { if (/\\/ws\\/coral/.test(String(u))) return new Dead(u); return p === undefined ? new Real(u) : new Real(u, p); }
    W.prototype = Real.prototype; W.CONNECTING = 0; W.OPEN = 1; W.CLOSING = 2; W.CLOSED = 3; window.WebSocket = W; })();
  const real = window.fetch.bind(window);
  window.fetch = (url, opts) => {
    const p = String(url).replace(/^https?:\\/\\/[^/]+/, '').split('?')[0];
    if (p === '/api/sessions/live') return Promise.resolve(new Response(JSON.stringify(window.__sessions), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    return real(url, opts);
  };`;

async function session(noMorph) {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async x => {
        const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { nextFrame, coralReady } = require('./helpers/wait').bind(ev);
    // Move Down saves the order on the server: both runs start from no saved order, and leave none behind.
    const resetOrder = () => ev(`fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ session_order: '[]' }) }).then(r => r.ok)`);
    const steps = [];
    const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: STUB });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        assert.ok(await resetOrder(), 'saved session order cleared');
        await ev(`localStorage.clear(); localStorage.setItem('coral-group-by-team', 'true'); ${noMorph ? "localStorage.setItem('coral-no-morph', '1');" : ''} true`);
        await Page.reload(); await Page.loadEventFired();
        // The feed is stubbed, so startup is everything but the feed.
        await coralReady('startup', 'liveList', 'liveListLoaded', 'history', 'settings');
        assert.equal(await ev(`(async () => (await import((window.__coralStatic || '/static/') + 'dom_morph.js')).morphDisabled())()`), noMorph, `kill switch ${noMorph ? 'on' : 'off'}`);
        await ev(`window.switchNavTab('agents'); window._coralSetLiveSessions(JSON.parse(JSON.stringify(window.__sessions))); true`);

        // ignoreActive: a switch moves the highlight on two rows without drawing the list (#4990), so until the next render the markup the last render wrote differs from the page in
        // those two rows' class and aria-current and nothing else; the step after it (a render) must be exactly equal again.
        const step = async (label, action, { ignoreActive = false } = {}) => {
            if (action) await ev(action);
            await nextFrame();
            steps.push({ label, ...(await ev(`(async () => {
                const list = (document.getElementById('live-sessions-list') || document.getElementById('mobile-session-list'));
                const r = await import((window.__coralStatic || '/static/') + 'render.js');
                const fresh = document.createElement('div'); fresh.innerHTML = r.lastLiveListHtml();
                const strip = h => h.replace(/\\sactive(?=["\\s])/g, '').replace(/ aria-current="true"/g, '');
                return {
                    equalsFresh: ${ignoreActive} ? strip(list.innerHTML) === strip(fresh.innerHTML) : list.innerHTML === fresh.innerHTML,
                    visible: [...list.querySelectorAll('.session-group-item')].filter(e => e.offsetParent).map(e => e.dataset.sessionId),
                    active: list.querySelector('.session-group-item.active')?.dataset.sessionId || null,
                    // Filled only at phone width (syncMobileAgentList); compared in the phone steps below.
                    phone: [...document.querySelectorAll('#mobile-session-list .session-group-item')].map(e => e.dataset.sessionId),
                };
            })()`)) });
        };
        const click = sel => `(document.querySelector(${JSON.stringify(sel)}) || { click() { throw new Error('missing ' + ${JSON.stringify(sel)}); } }).click(); true`;
        const tick = (sid, fields) => `window._coralHandleWsMessage({ type: 'coral_diff', changed: [${JSON.stringify(Object.assign({ name: 'team-a', agent_type: 'claude', session_id: sid }, fields))}], removed: [] }); true`;

        await step('initial');
        await step('search "agent 1"', `window.filterLiveAgents('agent 1'); true`);
        await step('search cleared', `window.filterLiveAgents(''); true`);
        await step('collapse the first group', click('#live-sessions-list [data-on-click="group-collapse"]'));
        await step('expand it again', click('#live-sessions-list [data-on-click="group-collapse"]'));
        await step('expand sleeping', click('#live-sessions-list [data-on-click="group-sleeping-expand"]'));
        await step('open archived', click('#live-sessions-list [data-on-click="archived-toggle"]'));
        await step('select sid-12', `window.selectLiveSession('team-b', 'claude', 'sid-12'); true`, { ignoreActive: true });
        await step('Move Down sid-12', `window.moveSessionDown('sid-12'); true`);
        for (let k = 0; k < 6; k++) await step('feed tick ' + k, tick('sid-' + k, { summary: `tick ${k} <b>&"'`, working: k % 2 === 0 }));
        // A real HTML5 drag: sid-1 dropped on the lower half of sid-6.
        await step('drag sid-1 below sid-6', `(() => {
            const a = document.querySelector('#live-sessions-list [data-session-id="sid-1"]'), b = document.querySelector('#live-sessions-list [data-session-id="sid-6"]');
            const dt = new DataTransfer(), y = b.getBoundingClientRect().bottom - 2;
            a.dispatchEvent(new DragEvent('dragstart', { bubbles: true, dataTransfer: dt }));
            b.dispatchEvent(new DragEvent('dragover', { bubbles: true, cancelable: true, dataTransfer: dt, clientY: y }));
            b.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: dt, clientY: y }));
            a.dispatchEvent(new DragEvent('dragend', { bubbles: true, dataTransfer: dt }));
            return true; })()`);
        await step('feed tick after the drag', tick('sid-2', { summary: 'after the drag' }));
        await step('group by team off', `window.toggleGroupByTeam(); true`);
        await step('group by team on', `window.toggleGroupByTeam(); true`);
        await step('sid-5 removed', `window._coralHandleWsMessage({ type: 'coral_diff', changed: [], removed: ['sid-5'] }); true`);
        await step('sid-40 added', `window._coralHandleWsMessage({ type: 'coral_diff', changed: [${JSON.stringify(agent(40, { board_project: 'team-a', name: 'team-a' }))}], removed: [] }); true`);
        // Phone width: the agent list is the phone list, cloned from the sidebar list on each render.
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await ev(`window.dispatchEvent(new Event('resize')); window.switchMobileTab?.('agents'); true`);
        await step('phone: first render', tick('sid-0', { summary: 'on a phone' }));
        for (let k = 1; k < 4; k++) await step('phone: feed tick ' + k, tick('sid-' + k, { summary: `phone tick ${k} <b>&"'`, working: k % 2 === 1 }));
        await step('phone: sid-2 removed', `window._coralHandleWsMessage({ type: 'coral_diff', changed: [], removed: ['sid-2'] }); true`);
        return steps;
    } finally {
        await Emulation.clearDeviceMetricsOverride().catch(() => {});
        await resetOrder().catch(() => {});
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
    }
}

(async () => {
    const morph = await session(false);
    const plain = await session(true);
    assert.equal(morph.length, plain.length);
    const at = label => morph.find(s => s.label === label);
    // The actions really changed the list (otherwise equal runs would prove nothing).
    assert.ok(at('search "agent 1"').visible.length < at('initial').visible.length, 'search narrows the list');
    assert.ok(at('collapse the first group').visible.length < at('initial').visible.length, 'collapse hides rows');
    assert.ok(at('expand sleeping').visible.length > at('expand it again').visible.length, 'expanding sleeping shows rows');
    assert.ok(at('open archived').visible.length > at('expand sleeping').visible.length, 'opening archived shows rows');
    assert.equal(at('select sid-12').active, 'sid-12', 'the selected row is active');
    const pos = (s, id) => s.visible.indexOf(id);
    const before = at('feed tick 5'), dragged = at('drag sid-1 below sid-6');
    assert.ok(pos(before, 'sid-1') < pos(before, 'sid-6') && pos(dragged, 'sid-1') > pos(dragged, 'sid-6'), `the drag moved sid-1 below sid-6 (${dragged.visible.join()})`);
    const phoneSteps = morph.filter(s => s.label.startsWith('phone:'));
    assert.ok(phoneSteps.length >= 4 && phoneSteps.every(s => s.phone.length > 0), `the phone list is filled at phone width (${JSON.stringify(phoneSteps.map(s => s.phone.length))})`);
    assert.ok(!at('phone: sid-2 removed').phone.includes('sid-2') && at('phone: feed tick 3').phone.includes('sid-2'), 'a removal reaches the phone list');
    for (let i = 0; i < morph.length; i++) {
        const m = morph[i], p = plain[i];
        assert.ok(m.equalsFresh, `${m.label}: morphed list equals a fresh parse of its markup`);
        assert.ok(p.equalsFresh, `${p.label}: plain list equals a fresh parse of its markup`);
        assert.deepEqual(m.phone, p.phone, `${m.label}: same phone list`);
        if (m.label.startsWith('phone:')) continue; // the sidebar is off screen at phone width
        assert.deepEqual(m.visible, p.visible, `${m.label}: same visible rows, in order, with and without the morph`);
        assert.equal(m.active, p.active, `${m.label}: same active row`);
    }
    console.log(`   ✓ ${morph.length} steps: morph == fresh parse, and visible rows, active row and (at 390 px, ${phoneSteps.length} steps) the phone list match the kill-switch run`);
    console.log('\nAll agent list morph parity checks passed');
})().catch(err => { console.error(err); process.exit(1); });
