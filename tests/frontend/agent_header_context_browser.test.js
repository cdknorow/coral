// The agent header names the agent's team and server (#3653, the operator's phone report): at
// 390x844 the line under the name shows the team, and on a hub with remotes the server ("This
// computer" for the local one); neither is clipped, and a long team name is cut short while the
// server stays whole. Text only: markup in a name is shown as text. Desktop shows the same line.
// A real terminal agent on a team is launched (and killed at the end); the hub is faked in the
// page's state.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what, ms = 15000) => waitFor(expr, what, { timeout: ms, soft: true });
    const MOD = `(window.__coralStatic||'/static/')`;
    let agent = null;
    // The context line's state: texts, whether each part is visible and whole, and inside the header.
    const measure = `(() => {
        const box = document.getElementById('terminal-header-context');
        const team = document.getElementById('terminal-header-team'), server = document.getElementById('terminal-header-server');
        const header = document.querySelector('#live-session-view .terminal-header').getBoundingClientRect();
        const part = el => { const r = el.getBoundingClientRect(); return { text: el.textContent, shown: !box.hidden && r.width > 0 && getComputedStyle(el).display !== 'none',
            whole: el.scrollWidth <= el.clientWidth + 1, inside: r.left >= header.left - 1 && r.right <= header.right + 1 && r.bottom <= header.bottom + 1 }; };
        return { hidden: box.hidden, title: box.title, team: part(team), server: part(server), imgs: document.querySelectorAll('#terminal-header-context img').length };
    })()`;
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        const l = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await l;
        await coralReady();

        agent = await ev(`fetch('/api/sessions/launch-team', { method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ board_name: 'hdr-team-3653', working_dir: '/tmp', agent_type: 'terminal', agents: [{ name: 'Header QA', prompt: 'x' }] }) })
            .then(r => r.json()).then(j => ({ ...j.agents[0], board: j.board, label: j.board_label }))`);
        const LIVE = `import(${MOD} + 'state.js').then(({ state }) => (state.liveSessions || []).find(s => s.session_id === ${JSON.stringify(agent.session_id)}) || null)`;
        await until(`${LIVE}.then(s => !!s)`, 'the agent in the live list');
        await ev(`window.selectLiveSession(${JSON.stringify(agent.session_name)}, 'terminal', ${JSON.stringify(agent.session_id)}); true`);
        await until(`!document.getElementById('terminal-header-context').hidden`, 'the context line');

        let m = await ev(measure);
        check('at 390x844 the header shows the team under the name', m.team.shown && m.team.text === agent.label, JSON.stringify(m.team));
        check('the team label is whole and inside the header', m.team.whole && m.team.inside, JSON.stringify(m.team));
        check('no server on a Coral that is not a hub', !m.server.shown && m.server.text === '', JSON.stringify(m.server));

        // A hub with one remote: the local agent's server reads "This computer".
        await ev(`import(${MOD} + 'state.js').then(({ state }) => { state.hub = true; state.servers = [{ id: 'build-box', label: 'build-box', status: 'online' }]; return true; })`);
        const update = s => ev(`import(${MOD} + 'header_context.js').then(async m => { m.updateHeaderContext(${s}); return true; })`);
        await update(`await ${LIVE}`);
        m = await ev(measure);
        check('on a hub with remotes the server shows: "This computer" for the local agent', m.server.shown && m.server.text === 'This computer' && m.team.shown, JSON.stringify(m));
        check('team and server are whole and inside the header at 390 wide', m.team.whole && m.server.whole && m.team.inside && m.server.inside, JSON.stringify(m));

        await update(`{ board_project: 'infra', server: 'build-box' }`);
        m = await ev(measure);
        check('a remote agent shows its server', m.server.text === 'build-box' && m.team.text === 'infra' && m.server.whole, JSON.stringify(m));

        await update(`{ board_project: 'a-very-long-team-name-that-will-never-fit-in-a-phone-header-at-all', server: 'build-box' }`);
        m = await ev(measure);
        check('a long team name is cut short (ellipsis) and stays inside the header', m.team.shown && !m.team.whole && m.team.inside, JSON.stringify(m.team));
        check('...while the server stays whole', m.server.shown && m.server.whole && m.server.inside, JSON.stringify(m.server));
        check('the full text is in the title', /a-very-long-team-name/.test(m.title) && /build-box/.test(m.title), m.title);

        // A server's label is the Operator's own text; server ids are slugs.
        await ev(`import(${MOD} + 'state.js').then(({ state }) => { state.servers.push({ id: 'srv-1', label: '<b>srv</b>', status: 'online' }); return true; })`);
        await update(`{ board_project: '<img src=x onerror="window.__pwn3653=1">', server: 'srv-1' }`);
        await new Promise(r => setTimeout(r, 200)); // sleep-ok: an onerror would fire by now
        m = await ev(measure);
        const pwn = await ev(`window.__pwn3653 === undefined`);
        check('markup in a name is shown as text, never run', m.imgs === 0 && pwn && m.team.text === '<img src=x onerror="window.__pwn3653=1">' && m.server.text === '<b>srv</b>', JSON.stringify(m));

        await ev(`import(${MOD} + 'state.js').then(({ state }) => { state.hub = false; state.servers = []; return true; })`);
        await update(`{}`);
        check('an agent on no board on a plain Coral: the line is hidden', (await ev(measure)).hidden);

        // Desktop: the same line in the same header.
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await Emulation.setTouchEmulationEnabled({ enabled: false });
        await update(`await ${LIVE}`);
        m = await ev(measure);
        check('desktop shows the same team line', m.team.shown && m.team.text === agent.label && m.team.whole && m.team.inside, JSON.stringify(m.team));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        if (agent) {
            try { await ev(`fetch('/api/sessions/live/' + encodeURIComponent(${JSON.stringify(agent.session_name)}) + '/kill', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ agent_type: 'terminal', session_id: ${JSON.stringify(agent.session_id)} }) }).then(() => true)`); } catch { /* best effort */ }
        }
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
