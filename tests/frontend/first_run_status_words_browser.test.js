// A new person's first screens say what is going on (#6382, first-run report #6370 items 3, 6, 11): a brand-new agent's composer says "Starting <agent>" in its box and Send says why a message may wait (from the row's own `starting`, #6381), and goes back to its own words once the CLI has written something; the Team view's "?" button has a name and "No signal yet" says what it waits for (a remote agent keeps its own reason); Analytics shows no list of Coral's
// own /api routes until an agent has really used a tool. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
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

        console.log('1. The composer of a starting agent');
        const words = await ev(`(async () => {
            const st = await import(${S} + 'agent_starting.js'), ss = await import(${S} + 'session_state.js');
            return { fresh: st.agentIsStarting({ session_id: 'f1', agent_type: 'claude', starting: true }),
                wrote: st.agentIsStarting({ session_id: 'f2', agent_type: 'claude', starting: false }),
                working: st.agentIsStarting({ session_id: 'f3', agent_type: 'claude' }),
                terminal: st.agentIsStarting({ session_id: 'f4', agent_type: 'terminal', starting: true }),
                old: st.agentIsStarting({ session_id: 'f5', agent_type: 'claude', starting: true, sleeping: true }),
                starting: ss.startingPlaceholder('proj1'), normal: ss.commandPlaceholder('proj1', 'claude') };
        })()`);
        check('a row the list marks starting is starting; one that is not marked, a terminal or a sleeping one is not', words.fresh === true && !words.wrote && !words.working && !words.terminal && !words.old, JSON.stringify(words));
        check('the box says Starting <name>, and the normal words are unchanged', /^Starting proj1/.test(words.starting) && words.normal === 'Message proj1, or paste an image', JSON.stringify([words.starting, words.normal]));

        await ev(`(() => { const entry = window.__m.entry.bind(window.__m); window.__m.entry = a => ({ ...entry(a), starting: true }); return true; })()`);
        await ev(`window.__m.add('start-agent', 'A', 3, { sleeping: false }); true`);
        await sleep(600); // sleep-ok: the first live list renders from several answers; no single completion event
        await ev(`window.selectLiveSession('start-agent', 'claude', 'start-agent-id', 'local'); true`);
        await waitFor(`/^Starting /.test(document.getElementById('command-input').placeholder)`, 'the starting placeholder', 6000);
        const box = await ev(`(() => { const b = document.querySelector('.btn-send'); return { ph: document.getElementById('command-input').placeholder, title: b && b.getAttribute('title'), label: b && b.getAttribute('aria-label') }; })()`);
        check('a selected new agent shows "Starting <name>" in its box', /^Starting start-agent/.test(box.ph), box.ph);
        check('Send says why a message may wait, in its title and its name', /still starting/.test(box.title || '') && /still starting/.test(box.label || ''), JSON.stringify(box));
        await ev(`(async () => { const st = await import(${S} + 'agent_starting.js'); st.syncSendStarting(false); return true; })()`);
        const back = await ev(`(() => { const b = document.querySelector('.btn-send'); return { title: b.getAttribute('title'), label: b.getAttribute('aria-label') }; })()`);
        check('and Send has its own words back once the agent is up', !/still starting/.test(back.title || '') && back.label === 'Send command', JSON.stringify(back));

        console.log('2. The Team view');
        const team = 'wording-team-' + Date.now();
        await ev(`(() => { const R = window.fetch;
            const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.origin).pathname;
                if (p.endsWith('/status') && p.includes('/api/board/')) return json({ agents: [
                    { name: 'Quiet', subscriber_id: 'q', agent_type: 'claude', role: 'Dev', availability: 'unknown', available: false, reason: 'No confirmed idle signal', tasks: [] },
                    { name: 'Far', subscriber_id: 'f', agent_type: 'claude', role: 'Dev', availability: 'unknown', available: false, reason: 'Remote agent availability is not observed locally', tasks: [] }],
                    summary: { available: 0, unknown: 2 }, unassigned_tasks: [], health_report: ['All good.'], observed_at: new Date().toISOString() });
                return R(u, o); };
            return true; })()`);
        await ev(`(async () => { const m = await import(${S} + 'team_availability.js'); await m.showTeamAvailability(${JSON.stringify(team)}); return true; })()`);
        await waitFor(`document.querySelectorAll('#team-availability-dialog .availability-agent').length === 2`, 'the Team view', 8000);
        const view = await ev(`(() => { const d = document.getElementById('team-availability-dialog'); const q = d.querySelector('.availability-help summary');
            return { help: q.getAttribute('aria-label'), title: q.getAttribute('title'), note: d.querySelector('.availability-help p').textContent, cards: [...d.querySelectorAll('.availability-agent')].map(c => c.querySelector('p') && c.querySelector('p').textContent) }; })()`);
        check('the "?" button has a name, and its note explains No signal yet', view.help === 'What these counts mean' && view.title === 'What these counts mean' && /No signal yet means the agent has not sent its first message/.test(view.note), JSON.stringify([view.help, view.note]));
        check('"No signal yet" says what it waits for; a remote agent keeps its own reason', view.cards[0] === "Waiting for this agent's first message to Coral." && /^Remote agent availability/.test(view.cards[1]), JSON.stringify(view.cards));
        await ev(`document.querySelector('#team-availability-dialog [data-dialog-close]').click(); true`);

        console.log('3. Analytics');
        const rows = await ev(`(async () => {
            const m = await import(${S} + 'cost_dashboard.js');
            const own = [{ operation: '/api/board/{project}/messages', call_type: 'api', agent_name: '', calls: 49 }, { operation: '/api/cloud/status', call_type: 'api', agent_name: '', calls: 16 }];
            const real = { operation: 'Bash', call_type: 'tool', agent_name: 'Dev', calls: 3 };
            return { none: m.shownCallMetrics(own).length, empty: m.shownCallMetrics([]).length, mixed: m.shownCallMetrics(own.concat([real])).length, agentApi: m.shownCallMetrics([{ operation: '/api/board/{project}/tasks', call_type: 'api', agent_name: 'Dev', calls: 2 }]).length };
        })()`);
        check('only Coral\'s own routes: no rows; with a tool call (or a route an agent called): the table as it is', rows.none === 0 && rows.empty === 0 && rows.mixed === 3 && rows.agentApi === 1, JSON.stringify(rows));
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} first-run status-words checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
