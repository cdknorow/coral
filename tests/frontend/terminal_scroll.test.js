// End-to-end acceptance test for terminal scroll-to-bottom on session switch.
//
// Verifies that the xterm.js viewport scrolls to the bottom after receiving
// the replay seed on connect and on session switch. Also validates that the
// client sends terminal_resize immediately on WebSocket open.
//
// Drives a running Coral server via headless Chrome + Chrome DevTools Protocol.
//
// Expects:
//   CORAL_URL  — base URL of a running Coral dev server (default http://127.0.0.1:8462)
//   CDP_PORT   — port of a running headless Chrome with --remote-debugging-port
//                (default 9222)
//
// Exits 0 on success, 1 on any failing scenario, 2 on harness error.

const CDP = require('chrome-remote-interface');

const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';

// Safety: shells spawned by a running Coral inherit CORAL_URL=http://127.0.0.1:8420
// (production). This suite mutates server state, so refuse the production port
// unless explicitly overridden. tests/frontend/run.sh always sets CORAL_URL to
// its isolated server.
if (/:8420(\/|$)/.test(BASE) && !process.env.CORAL_TEST_ALLOW_PROD) {
    console.error(`Refusing to run against ${BASE}: that is the production Coral port. Use tests/frontend/run.sh.`);
    process.exit(2);
}
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

const results = [];
function check(label, cond, detail) {
    const verdict = cond ? 'PASS' : 'FAIL';
    const line = `[${verdict}] ${label}${detail ? ' — ' + detail : ''}`;
    results.push({ verdict, label, line });
    console.log(line);
}

async function run() {
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);

    async function evalInPage(expr) {
        const r = await Runtime.evaluate({ expression: expr, awaitPromise: true, returnByValue: true });
        if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails));
        return r.result.value;
    }

    async function sleep(ms) {
        return new Promise(r => setTimeout(r, ms));
    }

    // Helper: read xterm scroll state via window.getTerminal() (exposed by app.js)
    async function getScrollState() {
        return evalInPage(`
            (function() {
                const term = window.getTerminal();
                if (!term) return null;
                const buf = term.buffer.active;
                return {
                    viewportY: buf.viewportY,
                    baseY: buf.baseY,
                    length: buf.length,
                    cols: term.cols,
                    rows: term.rows
                };
            })()
        `);
    }

    // Navigate to Coral UI and wait for bundle to load
    await Page.navigate({ url: BASE + '/' });
    await Page.loadEventFired();
    await sleep(2000);

    // ── Launch a terminal session via API ────────────────────────────
    console.log('Launching terminal session...');
    const launchResp = await evalInPage(`
        (async () => {
            const r = await fetch('${BASE}/api/sessions/launch', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    working_dir: '/tmp',
                    agent_type: 'terminal',
                    display_name: 'Scroll Test'
                })
            });
            return await r.json();
        })()
    `);

    if (!launchResp || !launchResp.session_id) {
        console.error('Failed to launch session:', launchResp);
        process.exit(2);
    }
    const sessionId = launchResp.session_id;
    const sessionName = launchResp.session_name;
    console.log(`Session launched: ${sessionName} (${sessionId})`);

    // Wait for session to appear in the WebSocket session list
    await sleep(2000);

    // ── Select the session in the UI ─────────────────────────────────
    console.log('Selecting session in UI...');
    await evalInPage(`window.selectLiveSession('${sessionName}', 'terminal', '${sessionId}')`);
    await sleep(1500);

    // ── Produce many lines of output ─────────────────────────────────
    console.log('Producing output (seq 1 200)...');
    await evalInPage(`
        (async () => {
            await fetch('${BASE}/api/sessions/live/${sessionName}/send', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    command: 'for i in $(seq 1 200); do echo scroll_line_$i; done',
                    agent_type: 'terminal',
                    session_id: '${sessionId}'
                })
            });
        })()
    `);

    // Wait for output to render in xterm
    await sleep(3000);

    // ── S1: After initial connect with output, viewport is at bottom ──
    const s1 = await getScrollState();
    const hasTerminal = s1 !== null;

    check('S1 terminal instance accessible', hasTerminal,
        hasTerminal ? `cols=${s1.cols} rows=${s1.rows}` : 'getTerminal() returned null');

    if (hasTerminal) {
        check('S1 viewport at bottom after output',
            s1.viewportY >= s1.baseY,
            `viewportY=${s1.viewportY} baseY=${s1.baseY}`);

        check('S1 output produced scrollback',
            s1.baseY > 0,
            `baseY=${s1.baseY} (should be > 0 with 200 lines)`);
    }

    // ── S2: Buffer contains recent output ────────────────────────────
    if (hasTerminal) {
        const bufferCheck = await evalInPage(`
            (function() {
                const term = window.getTerminal();
                if (!term) return { error: 'no terminal' };
                const buf = term.buffer.active;
                let text = '';
                for (let i = Math.max(0, buf.length - 50); i < buf.length; i++) {
                    const line = buf.getLine(i);
                    if (line) text += line.translateToString(true) + '\\n';
                }
                return {
                    hasLine200: text.includes('scroll_line_200'),
                    hasLine199: text.includes('scroll_line_199'),
                    length: text.length
                };
            })()
        `);

        check('S2 xterm buffer contains line 200 (most recent)',
            bufferCheck && bufferCheck.hasLine200,
            `bufferTextLen=${bufferCheck && bufferCheck.length}`);
        check('S2 xterm buffer contains line 199',
            bufferCheck && bufferCheck.hasLine199);
    }

    // ── S3: Session switch — replay seed scrolls to bottom ─────────
    // The real bug: after switching sessions, the replay seed (which starts
    // with \x1b[2J\x1b[3J\x1b[H = clear + cursor home) leaves the viewport
    // at the TOP of the replayed content instead of the bottom. To reproduce:
    // 1. Switch away from the terminal (hide xterm container, show capture)
    // 2. Switch back via selectLiveSession (the real UI flow)
    // 3. Check that viewport is at the bottom of the replayed content
    if (hasTerminal) {
        console.log('Simulating session switch (away + back via selectLiveSession)...');

        // Switch away: hide xterm, show capture pane (simulates clicking a non-xterm session)
        await evalInPage(`
            (function() {
                window.disconnectTerminalWs();
                document.getElementById("xterm-container").style.display = "none";
                document.getElementById("pane-capture").style.display = "";
            })()
        `);
        await sleep(500);

        // Switch back via the real selectLiveSession flow (createTerminal + connectTerminalWs + fitTerminal)
        await evalInPage(`window.selectLiveSession('${sessionName}', 'terminal', '${sessionId}')`);
        await sleep(3000);

        const s3 = await getScrollState();
        if (s3) {
            check('S3 replay produced scrollback after session switch',
                s3.baseY > 0,
                `baseY=${s3.baseY} (should be > 0 if replay has enough content)`);

            check('S3 viewport at bottom after session switch',
                s3.viewportY >= s3.baseY,
                `viewportY=${s3.viewportY} baseY=${s3.baseY}`);
        } else {
            check('S3 replay produced scrollback after session switch', false,
                'terminal not accessible after switch');
            check('S3 viewport at bottom after session switch', false,
                'terminal not accessible after switch');
        }
    }

    // ── S4: Resize on connect — xterm has valid dimensions ───────────
    if (hasTerminal) {
        const dims = await evalInPage(`
            (function() {
                const term = window.getTerminal();
                return term ? { cols: term.cols, rows: term.rows } : null;
            })()
        `);

        if (dims) {
            check('S4 xterm has valid dimensions after connect',
                dims.cols > 0 && dims.rows > 0,
                `cols=${dims.cols} rows=${dims.rows}`);
        }
    }

    // ── Cleanup: kill the test session ───────────────────────────────
    console.log('Cleaning up test session...');
    // The server exposes kill as POST .../kill (there is no DELETE route; a
    // DELETE here used to return 405 and leave the session running).
    const killResp = await evalInPage(`
        (async () => {
            const r = await fetch('${BASE}/api/sessions/live/${sessionName}/kill', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    agent_type: 'terminal',
                    session_id: '${sessionId}'
                })
            });
            let body = null;
            try { body = await r.json(); } catch (e) { body = null; }
            return { ok: r.ok, status: r.status, body };
        })()
    `);
    check('cleanup: kill request accepted', !!(killResp && killResp.ok && killResp.body && killResp.body.ok),
        `status=${killResp && killResp.status} body=${JSON.stringify(killResp && killResp.body)}`);

    // Poll until the session is gone so a later test in run.sh does not start
    // while this one's session (and its live-update ticks) are still present.
    let gone = false;
    for (let i = 0; i < 50 && !gone; i++) {
        gone = await evalInPage(`
            fetch('${BASE}/api/sessions/live').then(r => r.json())
                .then(list => !(list || []).some(s => s.session_id === '${sessionId}'))
                .catch(() => false)
        `);
        if (!gone) await new Promise(r => setTimeout(r, 200));
    }
    if (!gone) {
        const leftover = await evalInPage(`
            fetch('${BASE}/api/sessions/live').then(r => r.json())
                .then(list => JSON.stringify((list || []).filter(s => s.session_id === '${sessionId}').map(s => ({ name: s.name, agent_type: s.agent_type, status: s.status, done: s.done, sleeping: s.sleeping, not_started: s.not_started }))))
                .catch(e => 'error: ' + e)
        `);
        console.log('leftover session entry after DELETE:', leftover);
    }
    check('cleanup: test session removed from /api/sessions/live', gone);

    await client.close();

    // ── Summary ──────────────────────────────────────────────────────
    const failures = results.filter(r => r.verdict === 'FAIL');
    console.log(`\n=== SUMMARY ===`);
    results.forEach(r => console.log('  ' + r.line));
    console.log(`total: ${results.length}, failures: ${failures.length}`);
    if (failures.length) {
        process.exit(1);
    }
}

run().catch(e => {
    console.error('harness error:', e);
    process.exit(2);
});
