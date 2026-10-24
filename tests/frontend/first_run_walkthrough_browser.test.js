// First-run walkthrough (#6371): one suite that walks a brand-new Coral from its first page to a launched agent and a launched team, on a desktop and on a phone, so a break a new person
// would hit on day one fails the gate (the beta blockers #6330 and #6343 were all found by hand). Each viewport is a fresh install of its own: an empty data folder and HOME, a stand-in
// `claude` that is installed and ready, and a hub URL that is explicit but points at a port nobody listens on (so the first-launch Hub offer appears and nothing leaves the machine).
//
//   1. First page: the "Create a password" page (signed out, no data without it), then the home screen.
//   2. Coral Hub: the first-launch offer opens and "Skip for now" keeps Coral local (the answer is saved, a reload does not ask again).
//   3. New Agent in two steps: "+ New Agent" opens the choice (AI Agent / Agent Team / Terminal), AI Agent opens the form, a folder and Launch start the agent and OPEN it; a first prompt
//      reaches the CLI.
//   2b. A Hub question answered in the TERMINAL (#6381: Enter skips, only "yes" connects; the skip is stored as welcome_choice skipped and welcome_pending False, the answer the dashboard's own Skip saves):
//      a page loaded after it does not ask again, while Settings > Network and Hub and the home Coral Hub card still offer Connect. (The terminal side itself, Enter and yes on a real pty, is
//      cmd/coral's TestCloudStartupPrompt* in cloud_startup_pty_unix_test.go.)
//   4. Launch Team: the choice, then Agent Team: a name, a folder, a first task, Launch; the team's agents start and the first task reaches the CLI.
//
// Not walked here: the Terms (the EULA is a terminal prompt of the release build; this runs the dev build, which skips it: internal/license has its tests) and a real CLI.
// Expects CDP_PORT and CORAL_TEST_BIN (the built coral, from run_cdp_suites.mjs). Drives Chrome over CDP. Exit 0 / 1.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const net = require('node:net');
const path = require('node:path');
const { spawn } = require('./helpers/child_process.js');
const CDP = require('chrome-remote-interface');

if (process.platform === 'win32') { console.log('first_run_walkthrough: skipped on Windows (sh stand-ins, /usr/bin PATH)'); process.exit(0); }
const BIN = process.env.CORAL_TEST_BIN;
if (!BIN || !fs.existsSync(BIN)) { console.error('CORAL_TEST_BIN is not set to a built coral (run this through run_cdp_suites.mjs)'); process.exit(2); }

const PASSWORD = 'first-run-walk-pass-6371';
const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: server start-up poll spacing
const freePort = () => new Promise((res, rej) => { const s = net.createServer(); s.listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => res(p)); }); s.on('error', rej); });

async function freshServer() {
    const root = require('node:fs').mkdtempSync(path.join('/tmp', 'coral-walk-'));
    const dirs = { root, home: path.join(root, 'home'), data: path.join(root, 'data'), bin: path.join(root, 'bin'), work: path.join(root, 'project'), teamWork: path.join(root, 'team-project') };
    for (const d of [dirs.home, dirs.data, dirs.bin, dirs.work, dirs.teamWork]) fs.mkdirSync(d);
    const got = path.join(root, 'got');
    fs.writeFileSync(path.join(dirs.bin, 'claude'), [
        '#!/bin/sh',
        'case "$1" in --version) echo "2.0.0 (stand-in)"; exit 0;; auth) echo \'{"loggedIn": true}\'; exit 0;; esac',
        "printf 'stand-in claude ready\\n> '",
        `while IFS= read -r line; do printf '%s\\n' "$line" >> '${got}'; done`,
        'exec sleep 600', '',
    ].join('\n'), { mode: 0o755 });
    const port = await freePort(), deadHub = await freePort();
    assert.notEqual(port, 8420);
    const log = path.join(root, 'coral.log');
    const env = { PATH: `${dirs.bin}:/usr/bin:/bin`, HOME: dirs.home, TMPDIR: root, CORAL_DATA_DIR: dirs.data, CORAL_TELEMETRY_DISABLED: '1' }; // demo seeding stays ON: a fresh install is what a person gets, and the check below is that it holds nothing that runs
    const proc = spawn(BIN, ['--home', dirs.data, '--host', '127.0.0.1', '--port', String(port), '--no-browser', '--cloud-url', `http://127.0.0.1:${deadHub}`, '--backend', 'pty'],
        { env, stdio: ['ignore', fs.openSync(log, 'w'), fs.openSync(log, 'a')], detached: true });
    let exited = null; proc.on('exit', (c, s) => { exited = `exit ${c} ${s}`; }); proc.on('error', e => { exited = String(e); });
    const base = `http://127.0.0.1:${port}`;
    for (let i = 0; i < 200; i++) {
        if (exited) throw new Error(`the fresh server did not start: ${exited}\n${fs.readFileSync(log, 'utf8').slice(-600)}`);
        try { if ((await fetch(`${base}/api/health`)).ok) break; } catch { /* not up yet */ }
        await sleep(100);
    }
    const stop = async () => {
        try { process.kill(-proc.pid, 'SIGTERM'); } catch { /* gone */ }
        for (let i = 0; i < 50 && !exited; i++) await sleep(100);
        try { process.kill(-proc.pid, 'SIGKILL'); } catch { /* gone */ }
        try { fs.chmodSync(root, 0o700); } catch { /* ok */ }
        if (!process.env.QA_KEEP_FIRST_RUN) fs.rmSync(root, { recursive: true, force: true });
    };
    return { base, dirs, got, log, stop };
}

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Input, Emulation } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    // coralReady with what a red needs to name itself: where the page is, whether it loaded, what it shows, and the server's log tail (the one 59 s first attempt on ci/v161's gate said only "timed out ... (last: false)", #6557).
    const ready = async (server) => {
        try { await coralReady(); } catch (e) {
            const page = await ev(`({ url: location.href, state: document.readyState, text: document.body ? document.body.innerText.replace(/\\s+/g, ' ').slice(0, 200) : null, scripts: document.scripts.length, ready: typeof window.__coralReady })`).catch(x => String(x).slice(0, 80));
            let tail = ''; try { tail = fs.readFileSync(server.log, 'utf8').slice(-500); } catch { /* no log */ }
            throw new Error(`${String((e && e.message) || e)} | page ${JSON.stringify(page)} | server log tail ${JSON.stringify(tail)}`);
        }
    };
    const servers = [];
    const failures = [];
    const screen = () => ev(`(() => { const vis = e => !!e && e.offsetParent !== null; return { url: location.pathname + location.search, text: document.body.innerText.replace(/\\s+/g, ' ').slice(0, 240), modals: [...document.querySelectorAll('.modal')].filter(m => getComputedStyle(m).display !== 'none').map(m => (m.querySelector('h3,h2') || {}).textContent || m.id), toast: [...document.querySelectorAll('.toast')].map(t => t.textContent.trim()).slice(-2) }; })()`);
    const live = () => ev(`fetch('/api/sessions/live').then(r => r.json()).then(l => (Array.isArray(l) ? l : l.sessions || []).map(x => ({ session_id: x.session_id, name: x.name || x.session_name || '', board: x.board_name || x.board || '' })))`);
    const visible = sel => `[...document.querySelectorAll(${JSON.stringify(sel)})].some(e => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden')`;
    const clickVisible = async sel => { assert.ok(await ev(`(() => { const b = [...document.querySelectorAll(${JSON.stringify(sel)})].find(e => e.getClientRects().length); if (!b) return false; b.click(); return true; })()`), `no visible ${sel} to click (screen: ${JSON.stringify(await screen())})`); };
    const setValue = (id, v) => ev(`(() => { const d = document.getElementById(${JSON.stringify(id)}); d.value = ${JSON.stringify(v)}; d.dispatchEvent(new Event('input', { bubbles: true })); d.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
    const gotText = async s => { try { return fs.readFileSync(s.got, 'utf8'); } catch { return ''; } };
    const gotContains = async (s, text, ms = 30000) => { const until = Date.now() + ms; let t = ''; while (Date.now() < until) { t = await gotText(s); if (t.includes(text)) return t; await sleep(100); } return t; };
    // Whatever stands between Launch and the launch itself (the permission question) is answered the way a first-timer would: the recommended button.
    const answerQuestions = async () => {
        for (let i = 0; i < 40; i++) {
            if (await ev(`(() => { const b = document.querySelector('body > .modal:not([id]) [data-action="enable"]'); if (b) { b.click(); return true; } return false; })()`)) return true;
            await sleep(50);
        }
        return false;
    };

    const walk = async (tag, [w, h, mobile]) => {
        console.log(`${tag}: a brand-new install, ${w}x${h}`);
        const s = await freshServer(); servers.push(s);
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });

        // 1. the first page
        await Page.navigate({ url: s.base + '/' }); await Page.loadEventFired();
        await waitFor(`!!document.getElementById('create-form')`, `${tag}: the "Create a password" page`, { timeout: 15000, diag: `document.body.innerText.slice(0, 300)` });
        const anon = await ev(`fetch('/api/sessions/live').then(r => r.status)`);
        assert.ok(anon === 401 || anon === 403, `${tag}: signed out, the agent list is refused (got ${anon})`);
        await ev(`(() => { for (const id of ['password', 'confirm']) { const i = document.getElementById(id); i.value = ${JSON.stringify(PASSWORD)}; i.dispatchEvent(new Event('input', { bubbles: true })); } document.getElementById('submit').click(); return true; })()`);
        await waitFor(`!document.getElementById('create-form')`, `${tag}: leaving the password page`, { timeout: 15000, diag: `document.body.innerText.slice(0, 300)` });
        await ready(s);
        console.log(`   ✓ ${tag}: signed out until a password is created`);

        // 2. Coral Hub: offered once, skipped
        await waitFor(visible('[data-editions="skip"]'), `${tag}: the first-launch Coral Hub offer`, { timeout: 15000, diag: `document.body.innerText.slice(0, 300)` });
        assert.match(await ev(`document.getElementById('editions-title').textContent`), /Connect to Coral Hub/);
        await clickVisible('[data-editions="skip"]');
        await waitFor(`!document.querySelector('[data-editions="skip"]') || document.querySelector('[data-editions="skip"]').getClientRects().length === 0`, `${tag}: the offer closing`, { timeout: 8000 });
        const choice = await ev(`fetch('/api/settings').then(r => r.json()).then(j => ({ choice: String((j.settings || j).welcome_choice), pending: String((j.settings || j).welcome_pending) }))`);
        assert.equal(choice.choice, 'skipped', `${tag}: "Skip for now" is remembered (${JSON.stringify(choice)})`);
        console.log(`   ✓ ${tag}: the Coral Hub offer is skipped and the answer saved`);
        // a new install runs nothing on its own (#6384): whatever the examples are, no scheduled job is switched on
        const jobs = await ev(`fetch('/api/scheduled/jobs').then(r => r.json()).then(j => (Array.isArray(j) ? j : j.jobs || []).map(x => ({ name: x.name, enabled: x.enabled, description: String(x.description || '').slice(0, 30) })))`);
        assert.deepEqual(jobs.filter(x => x.enabled), [], `${tag}: a fresh data folder has an enabled scheduled job, which would start an agent and spend CLI credit on a new install (${JSON.stringify(jobs)})`);
        assert.ok(jobs.length === 0 || jobs.every(x => /^Example/.test(x.description)), `${tag}: a seeded job says it is an example (${JSON.stringify(jobs)})`);
        console.log(`   ✓ ${tag}: no scheduled job is on in a fresh install (${jobs.length} example job(s), off)`);
        await waitFor(`/What do you want to work on\\?/.test(document.body.innerText)`, `${tag}: the home screen`, { timeout: 15000, diag: `document.body.innerText.slice(0, 300)` });

        // 3. New Agent in two steps: the choice, then the form
        const before = (await live()).map(x => x.session_id);
        await clickVisible('[data-on-click="launch-show"]');
        await waitFor(visible('#launch-step-chooser .launch-type-card'), `${tag}: the choice of what to launch`, { timeout: 8000, diag: `document.body.innerText.slice(0, 300)` });
        const cards = await ev(`[...document.querySelectorAll('#launch-step-chooser .launch-type-card')].map(c => c.textContent.replace(/\\s+/g, ' ').replace('smart_toy', '').trim())`);
        assert.deepEqual(cards.slice(0, 3), ['New agent', 'New team', 'Terminal'], `${tag}: step one offers the kinds in this order (${JSON.stringify(cards)})`);
        await ev(`document.querySelector('#launch-step-chooser .launch-type-card[data-arg="agent"]').click(); true`);
        await waitFor(`(() => { const e = document.getElementById('launch-step-agent'); return !!e && getComputedStyle(e).display !== 'none' && !!document.getElementById('launch-dir'); })()`, `${tag}: the agent form (step two)`, { timeout: 8000, diag: `document.body.innerText.slice(0, 300)` });
        await setValue('launch-dir', s.dirs.work);
        await ev(`document.querySelector('#launch-step-agent [data-on-click="m-launch-session"]').click(); true`);
        await answerQuestions();
        let agent = null;
        for (const until = Date.now() + 20000; Date.now() < until && !agent; await sleep(100)) agent = (await live()).find(x => !before.includes(x.session_id)) || null;
        assert.ok(agent, `${tag}: Launch started no agent (screen: ${JSON.stringify(await screen())}; server log tail: ${fs.readFileSync(s.log, 'utf8').slice(-300)})`);
        assert.ok(await ev(`Promise.race([window.__coralReady.agentSelected(${JSON.stringify(agent.session_id)}).then(() => true), new Promise(r => setTimeout(() => r(false), 10000))])`), `${tag}: after Launch the new agent is not opened (screen: ${JSON.stringify(await screen())})`);
        await waitFor(`(() => { const c = document.getElementById('command-input'); return !!c && c.offsetParent !== null && !c.disabled; })()`, `${tag}: the message box of the opened agent`, { timeout: 15000, diag: `document.body.innerText.slice(0, 300)` });
        const PROMPT = `hello from the ${tag} first run`;
        await ev(`document.getElementById('command-input').focus(); true`);
        await Input.insertText({ text: PROMPT });
        const k = { key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 };
        await Input.dispatchKeyEvent({ type: 'keyDown', text: '\r', ...k }); await Input.dispatchKeyEvent({ type: 'keyUp', ...k });
        assert.ok((await gotContains(s, PROMPT)).includes(PROMPT), `${tag}: the first prompt never reached the CLI (it got ${JSON.stringify(await gotText(s))})`);
        console.log(`   ✓ ${tag}: New Agent in two steps starts an agent, opens it, and its first prompt reaches the CLI`);

        // 4. Launch Team: the choice, then the team form; the team's agents start and the first task reaches the CLI
        const teamName = `${tag}-first-team`, GOAL = `first task from the ${tag} walkthrough`;
        const beforeTeam = (await live()).map(x => x.session_id);
        if (!await ev(visible('[data-on-click="launch-show"]'))) { // on a phone the opened agent fills the page: back to the Agents list, where New is
            await ev(`window.switchNavTab('agents'); true`);
            await waitFor(visible('[data-on-click="launch-show"]'), `${tag}: the Agents list with its New button`, { timeout: 8000, diag: `document.body.innerText.slice(0, 300)` });
        }
        await clickVisible('[data-on-click="launch-show"]');
        await waitFor(visible('#launch-step-chooser .launch-type-card[data-arg="team"]'), `${tag}: the choice of what to launch, again`, { timeout: 8000, diag: `document.body.innerText.slice(0, 300)` });
        await ev(`document.querySelector('#launch-step-chooser .launch-type-card[data-arg="team"]').click(); true`);
        await waitFor(`(() => { const e = document.getElementById('launch-step-team'); return !!e && getComputedStyle(e).display !== 'none' && !!document.getElementById('team-board-name'); })()`, `${tag}: the team form`, { timeout: 8000, diag: `document.body.innerText.slice(0, 300)` });
        // the templates are listed by their own request after the form shows: wait for them (a condition), then read; an empty list after the wait is the failure the assertion is for (it read [] on a slow run, 11 of 20 at CPU x6)
        await waitFor(`document.querySelectorAll('#team-template-list [role=option]').length >= 1`, `${tag}: the team templates to be listed`, { timeout: 15000, diag: `document.getElementById('team-template-list') ? document.getElementById('team-template-list').outerHTML.slice(0, 300) : 'no list'` }).catch(() => {});
        const templates = await ev(`[...document.querySelectorAll('#team-template-list [role=option]')].map(o => o.dataset.name)`);
        assert.ok(templates.length >= 1, `${tag}: a new install offers at least one team template to start from (${JSON.stringify(templates)})`);
        await ev(`document.querySelector('#team-template-list [role=option]').click(); true`); // the first template in the list
        await waitFor(`document.querySelectorAll('#team-agents-list .team-agent-row').length >= 1`, `${tag}: the template's members`, { timeout: 8000, diag: `document.body.innerText.slice(0, 300)` });
        const members = await ev(`document.querySelectorAll('#team-agents-list .team-agent-row').length`);
        assert.ok(members >= 1, `${tag}: a new team starts with members to launch (${members})`);
        await setValue('team-board-name', teamName);
        await setValue('launch-team-dir', s.dirs.teamWork);
        await ev(`document.getElementById('launch-team-next').click(); true`); // Team, Members, Start (#6397)
        await ev(`document.getElementById('launch-team-next').click(); true`);
        await setValue('team-goal', GOAL);
        await ev(`document.getElementById('launch-team-go').click(); true`);
        await answerQuestions();
        let team = [];
        for (const until = Date.now() + 60000; Date.now() < until && team.length < members; await sleep(100)) team = (await live()).filter(x => !beforeTeam.includes(x.session_id) && !before.includes(x.session_id));
        assert.ok(team.length >= 1, `${tag}: Launch Team started no agent (screen: ${JSON.stringify(await screen())}; server log tail: ${fs.readFileSync(s.log, 'utf8').slice(-400)})`);
        // the first task is the team's first message: it is on the team's board, the Orchestrator has it unread, and its CLI was told to read it
        const orch = await (async () => { for (const until = Date.now() + 30000; Date.now() < until; await sleep(100)) { const l = await ev(`fetch('/api/sessions/live').then(r => r.json()).then(l => (Array.isArray(l) ? l : l.sessions || []).filter(x => x.board_is_orchestrator && /first-team/.test(x.board_label || '')).map(x => ({ board: x.board_project, unread: x.board_unread })))`); if (l[0] && l[0].unread >= 1) return l[0]; } return null; })();
        assert.ok(orch, `${tag}: the Orchestrator never got the first task as an unread board message (screen: ${JSON.stringify(await screen())})`);
        const posted = await ev(`fetch('/api/board/' + encodeURIComponent(${JSON.stringify(orch.board)}) + '/messages/all').then(r => r.json()).then(l => (Array.isArray(l) ? l : l.messages || []).map(m => String(m.content || '')))`);
        assert.ok(posted.some(c => c.includes(GOAL)), `${tag}: the team's board holds the first task (${JSON.stringify(posted).slice(0, 300)})`);
        assert.ok((await gotContains(s, 'unread message on the message board', 45000)).includes('unread message on the message board'), `${tag}: the Orchestrator's CLI was never told it has a message (it got ${JSON.stringify(await gotText(s))})`);
        assert.match(await ev(`location.pathname`), /^\/team\//, `${tag}: after Launch Team the person is on the team's page`);
        console.log(`   ✓ ${tag}: Launch Team starts all ${members} agents, opens the team, and the first task is on its board with the Orchestrator told`);
        return s;
    };

    // The Hub question was answered in the terminal: the server holds what the terminal stores for a skip, and no page asks again; Settings and the Hub card still connect.
    const terminalSkipWalk = async (tag, [w, h, mobile]) => {
        console.log(`${tag}: the Hub question skipped in the terminal, ${w}x${h}`);
        const s = await freshServer(); servers.push(s);
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
        await Page.navigate({ url: s.base + '/' }); await Page.loadEventFired();
        await waitFor(`!!document.getElementById('create-form')`, `${tag}: the "Create a password" page`, { timeout: 15000, diag: `document.body.innerText.slice(0, 300)` });
        await ev(`(() => { for (const id of ['password', 'confirm']) { const i = document.getElementById(id); i.value = ${JSON.stringify(PASSWORD)}; i.dispatchEvent(new Event('input', { bubbles: true })); } document.getElementById('submit').click(); return true; })()`);
        await waitFor(`!document.getElementById('create-form')`, `${tag}: leaving the password page`, { timeout: 15000, diag: `document.body.innerText.slice(0, 300)` });
        await ready(s);
        // what the terminal's Enter stores (cmd/coral rememberSkip): one request, the dashboard's own answer
        const put = await ev(`fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ welcome_choice: 'skipped', welcome_pending: 'False' }) }).then(r => r.status)`);
        assert.equal(put, 200, `${tag}: the stored skip is accepted`);
        await Page.navigate({ url: s.base + '/' }); await Page.loadEventFired();
        await ready(s);
        await waitFor(`/What do you want to work on\\?/.test(document.body.innerText)`, `${tag}: the home screen`, { timeout: 15000, diag: `document.body.innerText.slice(0, 300)` });
        // the first-launch offer would open a moment after the settings load: it does not (a bounded look, the offer's own wait for a loading connector is about 3 s)
        for (let i = 0; i < 40; i++) { assert.ok(!await ev(visible('[data-editions="skip"]')), `${tag}: the Coral Hub offer opened although the question was answered in the terminal`); await sleep(100); }
        const st = await ev(`fetch('/api/settings').then(r => r.json()).then(j => ({ choice: String((j.settings || j).welcome_choice), pending: String((j.settings || j).welcome_pending) }))`);
        assert.deepEqual(st, { choice: 'skipped', pending: 'False' }, `${tag}: the answer stays stored (${JSON.stringify(st)})`);
        console.log(`   ✓ ${tag}: a page loaded after a terminal skip does not ask again`);
        // Settings > Network and Hub still offers Connect
        await ev(`window.showSettingsModal('network'); true`);
        await waitFor(visible('#settings-cloud-connect'), `${tag}: Connect in Settings > Network and Hub`, { timeout: 10000, diag: `document.body.innerText.slice(0, 300)` });
        await ev(`window.hideSettingsModal(); true`);
        await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display === 'none'`, `${tag}: Settings closing`, { timeout: 5000 });
        // the home Hub card still opens the Connect screen (a phone has no such home: it shows the agent list, #4497, so there Settings is the way)
        if (!mobile) {
            await ev(`window.switchNavTab('agents'); true`);
            await waitFor(visible('#editions-home [data-editions="home-hub"]'), `${tag}: the Coral Hub card on the home screen`, { timeout: 10000, diag: `document.body.innerText.slice(0, 300)` });
            await clickVisible('#editions-home [data-editions="home-hub"]');
            await waitFor(visible('.editions-connect[data-cloud="connect"]'), `${tag}: Connect on the Hub card's screen`, { timeout: 10000, diag: `document.body.innerText.slice(0, 300)` });
        }
        console.log(`   ✓ ${tag}: Settings > Network and Hub${mobile ? '' : ' and the home Coral Hub card'} still offer Connect`);
    };

    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        for (const [tag, vp] of [['desktop', [1280, 900, false]], ['phone', [390, 844, true]]]) {
            try { await walk(tag, vp); } catch (e) { failures.push(`${tag}: ${String((e && e.message) || e).split('\n')[0]}`); console.log('   ✗ ' + failures[failures.length - 1]); }
            for (const s of servers.splice(0)) await s.stop().catch(() => {});
            try { await terminalSkipWalk(tag, vp); } catch (e) { failures.push(`${tag} (terminal skip): ${String((e && e.message) || e).split('\n')[0]}`); console.log('   ✗ ' + failures[failures.length - 1]); }
            for (const s of servers.splice(0)) await s.stop().catch(() => {});
        }
        if (failures.length) throw new Error(`${failures.length} first-run walkthrough(s) failed:\n - ` + failures.join('\n - '));
        console.log('first_run_walkthrough: ok');
    } finally {
        for (const s of servers) await s.stop().catch(() => {});
        await client.close();
    }
})().catch(e => { console.error('FAIL:', e && e.stack || e); process.exit(1); });
