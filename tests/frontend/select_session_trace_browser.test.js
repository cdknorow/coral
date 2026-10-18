// Choosing an agent, step by step (#4653): selectLiveSession (sessions.js) is a 275-line function that stops the last agent's refreshers, builds the current session, redraws the header, the composer,
// the tabs and the lists, attaches the terminal or the chat and starts every loader. Splitting it must change nothing. This selects agents in every situation (awake, sleeping, on a board with each saved
// tab, off a board, a terminal agent, on a remote server of a hub, in chat or terminal view, after another agent with a draft in the box, from a team's view that agrees or declines to leave) and
// records after each: the current session, the header, the composer, the panes and tabs, the quick actions, the sidebar's active row, the page's address, and what was fetched and which sockets
// were opened. Compared with fixtures/select_session_trace.json (sha256 per scenario, with the readable lines). CORAL_UPDATE_GOLDEN=1 rewrites it. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const GOLDEN = path.join(__dirname, 'fixtures', 'select_session_trace.json');
const sha = t => require('node:crypto').createHash('sha256').update(t).digest('hex');

const RECORD = `(() => {
    window.__seen = { fetches: [], sockets: [] };
    const realFetch = window.fetch;
    window.fetch = function (u, o) { try { const url = new URL(String(u), location.href); window.__seen.fetches.push(((o && o.method) || 'GET').toUpperCase() + ' ' + url.pathname + (url.search ? '?' + [...url.searchParams.keys()].sort().join('&') : '')); } catch (e) {} return realFetch.apply(this, arguments); };
    const RealWS = window.WebSocket;
    window.WebSocket = function (u, p) { try { window.__seen.sockets.push(new URL(String(u), location.href).pathname); } catch (e) {} return new RealWS(u, p); };
    window.WebSocket.prototype = RealWS.prototype; Object.assign(window.WebSocket, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 });
})()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, sleep } = await connect();
    const trace = {};
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        const SNAP = `(() => {
            const g = id => document.getElementById(id);
            const a = (el, ...k) => el ? Object.fromEntries(k.map(n => [n, n === 'text' ? el.textContent.replace(/\\\\s+/g, ' ').trim() : n === 'class' ? el.className : n === 'display' ? el.style.display : n === 'hidden' ? el.hidden : el.getAttribute(n)])) : null;
            const cs = T.state.currentSession;
            return {
                session: cs ? Object.fromEntries(Object.entries(cs).sort()) : null,
                name: a(g('session-name'), 'text'), termLabel: a(g('terminal-header-label'), 'text'), badge: a(g('session-type-badge'), 'text', 'class'),
                openLink: a(g('terminal-open-window-link'), 'href', 'hidden'),
                composer: g('command-input') ? { placeholder: g('command-input').placeholder, value: g('command-input').value, disabled: g('command-input').disabled } : null,
                capture: a(g('capture-wrapper'), 'class'), paneCapture: a(g('pane-capture'), 'display'), xterm: a(g('xterm-container'), 'display'), summary: a(g('session-summary'), 'display'),
                boardTab: a(g('agentic-tab-board'), 'display'), tabTop: localStorage.getItem('coral-agentic-tab-top'),
                activeTab: [...document.querySelectorAll('#agentic-state [role=tab][aria-selected=true], #agentic-state .agentic-tab.active')].map(e => e.id).join(','),
                quick: g('quick-actions') ? g('quick-actions').textContent.replace(/\\\\s+/g, ' ').trim() : null,
                sidebarActive: [...document.querySelectorAll('#live-sessions-list .session-group-item.active')].map(e => e.dataset.sessionId).join(','),
                view: [...document.querySelectorAll('.view, [id$="-view"]')].filter(e => e.style.display && e.style.display !== 'none' && getComputedStyle(e).display !== 'none').map(e => e.id).join(','),
                path: location.pathname,
                commands: JSON.stringify(T.state.currentCommands || null),
                tasks: [(T.state.currentAgentTasks || []).length, (T.state.currentBoardTasks || []).length, (T.state.currentAgentEvents || []).length],
                fetches: [...new Set(window.__seen.fetches)].sort(), sockets: window.__seen.sockets.slice(),
            };
        })()`;
        const run = async (name, setup, calls) => {
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
            await ev(RECORD);
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            await ev(`localStorage.clear(); ${setup}; T.render.renderLiveSessions(T.state.liveSessions); true`);
            await sleep(300); // sleep-ok: the list and startup loads settle over frames; there is no completion event
            const steps = [];
            for (const [label, expr] of calls) {
                await ev(`window.__seen.fetches.length = 0; window.__seen.sockets.length = 0; true`);
                await ev(`(async () => { ${expr} })()`);
                await sleep(900); // sleep-ok: the selection starts loaders that answer over several frames; there is no completion event
                const s = await ev(SNAP);
                steps.push({ label, ...s });
            }
            trace[name] = steps;
            assert.ok(steps.length && steps[0].session !== undefined, name);
        };
        const sel = (n, server = 'local', type = 'claude') => `await window.selectLiveSession(${JSON.stringify(n)}, ${JSON.stringify(type)}, ${JSON.stringify(n + '-id')}, ${JSON.stringify(server)});`;
        const add = (n, o = {}) => `window.__m.add(${JSON.stringify(n)}, 'M', 6, ${JSON.stringify({ sleeping: false, ...o })});`;
        await run('awake agent, chat view', `localStorage.setItem('coral-live-view-mode', 'chat'); ${add('a1')}`, [['select a1', sel('a1')]]);
        await run('awake agent, terminal view', `localStorage.setItem('coral-live-view-mode', 'terminal'); ${add('a1')}`, [['select a1', sel('a1')]]);
        await run('sleeping agent', `localStorage.setItem('coral-live-view-mode', 'terminal'); ${add('s1', { sleeping: true })}`, [['select s1', sel('s1')]]);
        await run('archived agent', `${add('r1', { archived: true })}`, [['select r1', sel('r1')]]);
        await run('terminal-type agent', `localStorage.setItem('coral-live-view-mode', 'chat'); ${add('t1', { agent_type: 'terminal' })}`, [['select t1', sel('t1', 'local', 'terminal')]]);
        await run('board agent, no saved tab', `${add('b1', { board_project: 'team-a' })}`, [['select b1', sel('b1')]]);
        await run('board agent, saved tab files', `localStorage.setItem('coral-agentic-tab-top', 'files'); ${add('b1', { board_project: 'team-a' })}`, [['select b1', sel('b1')]]);
        await run('no-board agent, saved tab board', `localStorage.setItem('coral-agentic-tab-top', 'board'); ${add('a1')}`, [['select a1', sel('a1')]]);
        await run('switching with a draft', `localStorage.setItem('coral-live-view-mode', 'chat'); ${add('a1')} ${add('a2', { board_project: 'team-a' })}`, [['select a1', sel('a1')], ['type a draft', `document.getElementById('command-input').value = 'half a thought';`], ['select a2', sel('a2')], ['back to a1', sel('a1')]]);
        await run('remote agent on a hub', `T.state.hub = true; T.state.servers = [{ id: 'local', label: 'here', status: 'online' }, { id: 'm1', label: 'box', status: 'online' }]; ${add('x1', { server: 'm1' })}`, [['select x1', sel('x1', 'm1')]]);
        await run('selection by id only', `${add('a1')}`, [['select a1 without a server', `await window.selectLiveSession('a1', 'claude', 'a1-id');`]]);
        await run('leaving a team view that agrees', `${add('a1')} T.state.selectedTeam = 'team-z'; window.exitTeamContext = async () => { T.state.selectedTeam = null; return true; };`, [['select a1', sel('a1')]]);
        await run('leaving a team view that declines', `${add('a1')} T.state.selectedTeam = 'team-z'; window.exitTeamContext = async () => false;`, [['select a1', sel('a1')]]);
        await run('three quick switches', `${add('a1')} ${add('a2')} ${add('a3')}`, [['select a1, a2, a3 without waiting', `window.selectLiveSession('a1', 'claude', 'a1-id', 'local'); window.selectLiveSession('a2', 'claude', 'a2-id', 'local'); await window.selectLiveSession('a3', 'claude', 'a3-id', 'local');`]]);
        // The same twice.
        const first = JSON.stringify(trace['awake agent, chat view']);
        await run('again', `localStorage.setItem('coral-live-view-mode', 'chat'); ${add('a1')}`, [['select a1', sel('a1')]]);
        const again = JSON.stringify(trace['again']), was = JSON.stringify(trace['awake agent, chat view']);
        const normalise = t => JSON.stringify(JSON.parse(t).map(s => ({ ...s, label: 'x' })));
        assert.equal(normalise(again), normalise(first), 'the same trace twice');
        delete trace['again']; void was;
        const hashes = {}, lines = {};
        for (const [name, steps] of Object.entries(trace)) {
            hashes[name] = steps.map(s => ({ step: s.label, sha256: sha(JSON.stringify(s)) }));
            lines[name] = steps.map(s => `${s.label} | session ${s.session && s.session.name}/${s.session && s.session.server} | header ${s.name && s.name.text} / ${s.termLabel && s.termLabel.text} | composer ${s.composer && JSON.stringify(s.composer)} | path ${s.path} | tab ${s.activeTab} | sockets ${s.sockets.join(',')} | fetches ${s.fetches.join(' ; ')}`);
        }
        if (process.env.CORAL_UPDATE_GOLDEN) {
            fs.mkdirSync(path.dirname(GOLDEN), { recursive: true });
            fs.writeFileSync(GOLDEN, JSON.stringify({ about: 'what selectLiveSession did, scenario by scenario, for select_session_trace_browser.test.js: sha256 of each step (session, header, composer, panes, tabs, quick actions, address, fetches, sockets) and its readable line', hashes, lines }, null, 1) + '\n');
            console.log('golden written: ' + Object.keys(hashes).length + ' scenarios');
            return;
        }
        const want = JSON.parse(fs.readFileSync(GOLDEN, 'utf8'));
        assert.deepEqual(Object.keys(hashes), Object.keys(want.hashes), 'the same scenarios');
        for (const k of Object.keys(want.hashes)) {
            hashes[k].forEach((h, i) => {
                if (h.sha256 !== want.hashes[k][i].sha256) assert.fail(`${k} / ${h.step}: changed.\n  now:  ${lines[k][i]}\n  was:  ${want.lines[k][i]}`);
            });
        }
        console.log(`PASS select session trace: ${Object.keys(want.hashes).length} scenarios identical`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
