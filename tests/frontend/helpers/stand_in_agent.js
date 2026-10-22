// A stand-in Claude agent for suites that need a real session token (#3481, #3495, #3502).
//
// Coral launches it like any Claude agent, but its CLI path is a script that saves the
// CORAL_SESSION_TOKEN it was started with and then sleeps, so a suite can call agent-only
// routes as that agent. Under load (3 shards on epoch) the pane's shell can take several
// seconds to run the launch line, so the token is polled for up to 30 s. If it never comes,
// the failure says why: whether the script ran at all, whether it ran without a token, and
// the pane's last lines and the server's log lines for the session.
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');

const sleep = (ms) => new Promise(r => setTimeout(r, ms));

// The token wait: generous, since a healthy launch writes it in well under a second.
const TOKEN_DEADLINE_MS = 30000;

// Stand-ins can overlap (terminal_switch launches two), and each one's cli_path_claude replaces the last. What to give back is the value from before the FIRST live one,
// and only when the LAST one stops: restoring each stand-in's own "previous value" left the earlier stand-in's deleted script as the server's path, which a later suite on
// the same server (settings_modal_trace) then showed. OURS is every script this process set, so a path someone else set is still never overwritten (#4354).
const LIVE = new Set(), OURS = new Set();
let ORIGINAL = '';
// Every stand-in not yet stopped. A caller that throws between launchStandIn and the list it cleans up from (#5077: a check before the push; #5080) never calls stop(), which left
// cli_path_claude on the script; so the process stops whatever is left when it ends, and a caller cannot forget.
const PENDING = new Set();
process.on('beforeExit', async () => { for (const stop of [...PENDING]) await stop().catch(() => {}); });

// launchStandIn starts the stand-in. api(method, path, body) returns {status, data}, as the
// suites' own JSON helpers do. It returns {sid, name, token, launch, ms, diag, stop}; token is
// '' when it never came, and diag then explains why. Always call stop() (in a finally): it
// kills the session, restores cli_path_claude and removes the script's folder.
async function launchStandIn(api, { workingDir = '/tmp', displayName = 'Stand-in Agent', deadlineMs = TOKEN_DEADLINE_MS } = {}) {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-standin-'));
    const tokenFile = path.join(dir, 'token');
    const ranFile = path.join(dir, 'ran');
    const cli = path.join(dir, 'claude');
    // Coral runs the configured CLI with --version on every launch, at the same time as the pane's
    // launch line (recordCLIVersion), and that run has no token. The stub answers it without
    // touching the files, and only an invocation that has a token writes one: before #3502 the
    // version probe could land last and overwrite the token with an empty file, which is the
    // "no token" flake, not a slow shell. "ran" marks the launched invocation (it has --session-id).
    fs.writeFileSync(cli, [
        '#!/bin/sh',
        'case "$1" in --version) echo "0.0.0 (stand-in)"; exit 0;; auth) echo \'{"loggedIn": true}\'; exit 0;; esac', // the version probe and the sign-in check (#5838) are answered like the CLI's, never a launch
        `: > '${ranFile}'`,
        `[ -n "$CORAL_SESSION_TOKEN" ] || { echo "stand-in: no CORAL_SESSION_TOKEN" >&2; : > '${tokenFile}.none'; exec sleep 600; }`,
        `printf '%s' "$CORAL_SESSION_TOKEN" > '${tokenFile}.tmp' && mv '${tokenFile}.tmp' '${tokenFile}'`,
        'exec sleep 600', '',
    ].join('\n'), { mode: 0o755 });
    if (!LIVE.size) ORIGINAL = ((await api('GET', '/api/settings')).data?.settings || {}).cli_path_claude || '';
    LIVE.add(cli); OURS.add(cli);
    await api('PUT', '/api/settings', { cli_path_claude: cli });

    let launch = null;
    const stop = async () => {
        if (!PENDING.delete(stop)) return; // stopped already (the caller's, then the process's)
        const d = launch && launch.data;
        if (d && d.session_id) await api('POST', `/api/sessions/live/${encodeURIComponent(d.session_name)}/kill`, { agent_type: 'claude', session_id: d.session_id }).catch(() => {});
        // Restore cli_path_claude only to undo OUR own change, and never write an empty value over a path
        // someone else set (#4354): when the last live stand-in stops and cli_path_claude is still one of our
        // scripts, put back the value from before the first one -- but only if that value was non-empty. If there
        // was no previous value, or another client has changed cli_path_claude since (so it is no longer one of
        // our scripts), we leave it alone rather than clearing it to empty, which used to wipe a path set elsewhere.
        LIVE.delete(cli);
        const cur = (((await api('GET', '/api/settings').catch(() => ({}))).data || {}).settings || {}).cli_path_claude || '';
        if (!LIVE.size && OURS.has(cur) && ORIGINAL) await api('PUT', '/api/settings', { cli_path_claude: ORIGINAL }).catch(() => {});
        fs.rmSync(dir, { recursive: true, force: true });
    };

    PENDING.add(stop);
    const started = Date.now();
    try {
        launch = await api('POST', '/api/sessions/launch', { working_dir: workingDir, agent_type: 'claude', display_name: displayName });
    } catch (e) {
        await stop(); // the caller never gets stop(), so undo the setting here
        throw e;
    }
    const sid = launch.data && launch.data.session_id;
    const name = launch.data && launch.data.session_name;
    let token = '';
    if (sid) {
        for (const end = started + deadlineMs; !token && Date.now() < end; await sleep(100)) {
            try { token = fs.readFileSync(tokenFile, 'utf8').trim(); } catch { /* not yet */ }
            if (fs.existsSync(tokenFile + '.none')) break; // launched without a token: waiting won't help
        }
    }
    const ms = Date.now() - started;
    const diag = token ? '' : await diagnose(api, { launch, sid, name, ran: fs.existsSync(ranFile), none: fs.existsSync(tokenFile + '.none'), ms });
    return { sid, name, token, launch, ms, diag, stop };
}

// diagnose says why no token came: the launch answer, how far the script got, the pane's
// last lines and the server's log lines for the session (CORAL_TEST_LOG, set by the runners).
async function diagnose(api, { launch, sid, name, ran, none, ms }) {
    const out = [`no session token after ${ms} ms`];
    if (!sid) {
        out.push(`launch answered ${launch && launch.status}: ${JSON.stringify(launch && launch.data).slice(0, 300)}`);
        return scrub(out.join('\n'));
    }
    out.push(!ran ? 'the stand-in script never started (the launch line did not run in the pane)'
        : none ? 'the stand-in script was launched WITHOUT CORAL_SESSION_TOKEN in its environment'
            : 'the stand-in script started but has not written the token');
    try {
        const cap = await api('GET', `/api/sessions/live/${encodeURIComponent(name)}/capture?agent_type=claude&session_id=${encodeURIComponent(sid)}`);
        const text = (cap.data && cap.data.capture) || `(no capture: ${JSON.stringify(cap.data).slice(0, 200)})`;
        out.push('pane (last 15 lines):', ...String(text).replace(/\s+$/, '').split('\n').slice(-15).map(l => '  | ' + l));
    } catch (e) { out.push(`pane capture failed: ${e.message}`); }
    const log = process.env.CORAL_TEST_LOG;
    if (log) {
        try {
            const short = sid.slice(0, 8);
            const lines = fs.readFileSync(log, 'utf8').split('\n').filter(l => l.includes(short) || (name && l.includes(name)));
            out.push(`server log (${lines.length} lines for the session, last 15):`, ...lines.slice(-15).map(l => '  | ' + l.slice(0, 400)));
        } catch (e) { out.push(`server log unreadable: ${e.message}`); }
    } else {
        out.push('server log: CORAL_TEST_LOG not set (run through run.sh or run_cdp_suites.mjs)');
    }
    return scrub(out.join('\n'));
}

// scrub keeps the person out of a failure that may be pasted on a board: the pane's prompt
// carries the user and host names, and paths carry the home folder.
function scrub(text) {
    const swaps = [[os.homedir(), '~'], [os.userInfo().username, '<user>'], [os.hostname().split('.')[0], '<host>']];
    for (const [from, to] of swaps) if (from && from.length > 2) text = text.split(from).join(to);
    return text;
}

module.exports = { launchStandIn, TOKEN_DEADLINE_MS };
