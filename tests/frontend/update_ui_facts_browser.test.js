// The update screens say what the server really sends (#5383, UI/UX's #5380; the fields are #5388's: install_kind, upgrade_command, checked_at, not_ready_reason with latest, last_rollback; the relaunch
// reasons are the docs' nine). Each relaunch reason has its own words; the top-bar control and Settings say "Not available right now: <why>" in one wording; a Homebrew install (the server's
// reason unsupported_install, with or without install_kind) gets the brew command and never "up to date"; Settings says "up to date" only when the server says up_to_date and says why nothing is ready
// otherwise; a hub relaunch is "Relaunched from Coral Hub at <time>" (never a name); a roll-back says why when the server gives a reason; the failure notice has the docs' words and a Try again only
// when it can do something; the relaunch dialog says what happens next per install kind and never promises that the page reconnects. Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const REASONS = ['owner_only', 'max_mode', 'switch_off', 'nothing_staged', 'not_verified', 'busy', 'too_soon', 'already_relaunched', 'unsupported_install', 'no_run_record'];
const staged = (over = {}, rel = {}) => ({ enabled: true, installed: '1.4.125', state: 'staged', staged: { version: '1.4.126', staged_at: '2026-10-17T03:00:00Z', days_ready: 3, verified: true }, relaunch: { available: true, reason: '', restarts: [], tmux_agents_restart: false, ack: 'ack-1', ...rel }, last_result: null, ...over });
const idle = (over = {}) => ({ enabled: true, installed: '1.4.125', state: 'none', staged: null, relaunch: { available: false, reason: 'nothing_staged', restarts: [], ack: '' }, last_result: null, ...over });

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const settle = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a redraw settles over frames; there is no completion event
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`localStorage.clear(); true`);
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`import((window.__coralStatic || '/static/') + 'update_flag.js').then(m => { m.setUpdatesEnabled(true); return true; })`);
        await ev(`(() => { const R = window.fetch; window.__st = null; window.__relaunch = { status: 202, body: { ok: true } };
            const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.origin).pathname;
                if (p === '/api/system/update') return window.__st ? json(window.__st) : json({ error: 'x' }, 404);
                if (p === '/api/system/update/relaunch-local') return json(window.__relaunch.body, window.__relaunch.status);
                if (p === '/api/cloud/status') return json({ connected: false, hub_relaunch_updates: true });
                return R(u, o); }; return true; })()`);
        const setStatus = async st => { await ev(`window.__st = ${JSON.stringify(st)}; import(${S} + 'update_ui.js').then(m => m.startUpdateUi && m.startUpdateUi()).then(() => import(${S} + 'update_ui.js')).then(m => m.refreshUpdateStatus()).then(() => true)`); };
        const pane = async () => { await ev(`window.showSettingsModal('updates'); import(${S} + 'update_settings.js').then(m => m.showUpdatesPane()).then(() => true)`); await settle(250); return ev(`(() => { const b = document.getElementById('update-pane-body'); return { text: b.textContent.replace(/\\s+/g, ' ').trim(), title: ((b.querySelector('.update-status-text b') || b.querySelector('.update-managed b')) || {}).textContent, cmd: (b.querySelector('.update-cmd code') || {}).textContent, facts: (b.querySelector('.update-facts') || {}).textContent }; })()`); };

        console.log('1. every relaunch reason has its own words, one wording for "not available"');
        const words = await ev(`import(${S} + 'update_api.js').then(m => ({ r: ${JSON.stringify(REASONS)}.map(x => m.reasonText(x)), unknown: m.reasonText('who_knows'), proto: m.reasonText('constructor'), na: m.notAvailable('busy') }))`);
        check(`the ten reasons are not "Unavailable" (${JSON.stringify(words.r.map(w => w.slice(0, 28)))})`, words.r.every(w => w && w !== 'Unavailable'));
        check('switch_off says where the switch is', /Settings > Network and Hub/.test(words.r[2]));
        check(`no_run_record says it cannot relaunch itself here (${words.r[9]})`, words.r[9] === 'Coral could not be restarted automatically on this computer, so it cannot relaunch itself here.');
        check(`an unknown reason, and "constructor", are Unavailable (${words.unknown}, ${words.proto})`, words.unknown === 'Unavailable' && words.proto === 'Unavailable');
        check(`one wording: "${words.na}"`, words.na === 'Not available right now: Another relaunch is already running.');
        await setStatus(staged({}, { available: false, reason: 'no_run_record' }));
        const nrPill = await ev(`(() => { const p = document.getElementById('update-pill'); return !p || p.hidden || getComputedStyle(p).display === 'none'; })()`);
        const nrPane = await pane();
        check(`no_run_record: no control in the top bar, and Settings carries the sentence (${nrPill}: ${nrPane.text.slice(0, 200)})`, nrPill === true && /could not be restarted automatically on this computer, so it cannot relaunch itself here\./.test(nrPane.text));
        await setStatus(staged({}, { available: false, reason: 'busy' }));
        const pillName = await ev(`document.getElementById('update-pill').getAttribute('aria-label')`);
        const p1 = await pane();
        check(`the control and Settings say it the same way (${pillName})`, /Not available right now: Another relaunch is already running\./.test(pillName) && /Not available right now: Another relaunch is already running\./.test(p1.text));

        console.log('2. the new fields, read as the server sends them');
        const norm = await ev(`import(${S} + 'update_api.js').then(m => { const n = o => m.normalizeStatus({ enabled: true, installed: '1.4.125', state: 'none', relaunch: {}, ...o }); return { kinds: ['app', 'msi', 'portable', 'tarball', 'homebrew', 'unwritable', 'unsupported', 'x'].map(k => n({ install_kind: k }).installKind), cmdOnlyBrew: [n({ install_kind: 'app', upgrade_command: 'rm -rf /' }).upgradeCommand, n({ install_kind: 'homebrew', upgrade_command: 'brew upgrade --cask coral' }).upgradeCommand], nr: ['up_to_date', 'not_promoted', 'backoff', 'check_failed', 'not_checked', 'unsupported_install', 'switch_off', 'applying', 'whatever'].map(k => n({ not_ready_reason: k }).notReady), latest: [n({ latest: '1.4.130' }).latest, n({ latest: 'v1; drop table' }).latest], none: [n({}).checkedAt, n({}).installKind, n({}).notReady, n({}).rollback], rb: [n({ last_rollback: { at: 'a', from: '1.4.130', to: '1.4.131', reason: 'swap_failed' } }).rollback.reason, n({ last_rollback: { reason: 'made_up' } }).rollback.reason] }; })`);
        check(`the six install kinds are read, "unsupported" and strangers are not (${JSON.stringify(norm.kinds)})`, JSON.stringify(norm.kinds) === JSON.stringify(['app', 'msi', 'portable', 'tarball', 'homebrew', 'unwritable', '', '']));
        check(`a command is taken only for Homebrew (${JSON.stringify(norm.cmdOnlyBrew)})`, norm.cmdOnlyBrew[0] === '' && norm.cmdOnlyBrew[1] === 'brew upgrade --cask coral');
        check(`a not-ready reason is one of the eight, a latest is a version (${JSON.stringify(norm.nr)} ${JSON.stringify(norm.latest)})`, norm.nr.slice(0, 8).every(Boolean) && norm.nr[8] === '' && norm.latest[0] === '1.4.130' && norm.latest[1] === '');
        check(`absent fields are unknown (${JSON.stringify(norm.none)}), a roll-back reason outside the five is "other" (${JSON.stringify(norm.rb)})`, norm.none[0] === '' && norm.none[1] === '' && norm.none[2] === '' && norm.none[3] === null && norm.rb[0] === 'swap_failed' && norm.rb[1] === 'other');

        console.log('3. Settings says why nothing is ready, and "up to date" only when it is true');
        for (const [name, st, want, not] of [
            ['unknown', idle(), /Nothing is ready to install/, /up to date/i],
            ['up_to_date', idle({ not_ready_reason: 'up_to_date' }), /Coral is up to date/, null],
            ['not_promoted', idle({ not_ready_reason: 'not_promoted', latest: '1.4.130' }), /Coral 1\.4\.130 is out.*Installing it is up to you/, /up to date/i],
            ['check_failed', idle({ not_ready_reason: 'check_failed' }), /could not check for updates/, /up to date/i],
            ['switch_off', idle({ not_ready_reason: 'switch_off' }), /Automatic update checks are off/, /up to date/i],
        ]) {
            await setStatus(st); const p = await pane();
            check(`${name}: "${p.title}"`, want.test(p.text) && (!not || !not.test(p.text)));
        }
        await setStatus(idle({ not_ready_reason: 'up_to_date', checked_at: '2026-10-20T08:00:00Z' }));
        check('"Last checked" is shown when the server gives it', /Last checked/.test((await pane()).facts || ''));
        await setStatus(idle({ not_ready_reason: 'up_to_date' }));
        check('and is not made up when it does not', !/Last checked/.test((await pane()).facts || ''));

        console.log('4. Homebrew and other installs that do not update themselves');
        for (const [name, st, wantTitle, wantCmd] of [
            ['Homebrew (install_kind and the reason)', idle({ install_kind: 'homebrew', upgrade_command: 'brew upgrade --cask coral', relaunch: { available: false, reason: 'unsupported_install', restarts: [], ack: '' } }), 'Homebrew updates this Coral', 'brew upgrade --cask coral'],
            ['unwritable', idle({ install_kind: 'unwritable', relaunch: { available: false, reason: 'unsupported_install', restarts: [], ack: '' } }), 'This install cannot update itself', undefined],
            ['the reason alone (an older server)', idle({ relaunch: { available: false, reason: 'unsupported_install', restarts: [], ack: '' } }), 'This install cannot update itself', undefined],
            ['not_ready_reason alone', idle({ not_ready_reason: 'unsupported_install' }), 'This install cannot update itself', undefined],
        ]) {
            await setStatus(st); const p = await pane();
            check(`${name}: "${p.title}", ${wantCmd ? 'the command ' + p.cmd : 'no command'}, never "up to date"`, p.title === wantTitle && p.cmd === wantCmd && !/up to date/i.test(p.text));
        }
        // the command is one line on a phone (it scrolls sideways, it does not wrap)
        await setStatus(idle({ install_kind: 'homebrew', upgrade_command: 'brew upgrade --cask coral', relaunch: { available: false, reason: 'unsupported_install', restarts: [], ack: '' } }));
        await Emulation.setDeviceMetricsOverride({ width: 320, height: 700, deviceScaleFactor: 1, mobile: true }); await pane();
        const cmdBox = await ev(`(() => { const c = document.querySelector('#update-pane-body .update-cmd code'); const cs = getComputedStyle(c); const lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.4; return { ws: cs.whiteSpace, ox: cs.overflowX, oneLine: c.getBoundingClientRect().height < lh * 1.6, fits: c.getBoundingClientRect().right <= innerWidth }; })()`);
        check(`the brew command stays on one line at 320 px and scrolls sideways if it must (${JSON.stringify(cmdBox)})`, cmdBox.ws === 'nowrap' && cmdBox.ox === 'auto' && cmdBox.oneLine && cmdBox.fits);
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });

        console.log('5. what a hub relaunch and a roll-back say');
        await setStatus(idle({ not_ready_reason: 'up_to_date', last_result: { at: '2026-10-20T08:21:00Z', from: '1.4.130', to: '1.4.131', result: 'relaunched', by: 'hub:Dana Q' } }));
        const lu = (await pane()).facts || '';
        check(`"Relaunched from Coral Hub at <time>", never a name (${lu})`, /Relaunched from Coral Hub at /.test(lu) && !/Dana/.test(lu));
        const rb = async (reason, restartFailed) => { await setStatus(idle({ installed: '1.4.130', last_result: { at: '2026-10-20T08:21:00Z', from: '1.4.130', to: '1.4.131', result: 'failed_rolled_back', by: 'hub:x' }, ...(reason ? { last_rollback: { at: '2026-10-20T08:21:00Z', from: '1.4.130', to: '1.4.131', reason, ...(restartFailed ? { restart_failed: true } : {}) } } : {}) })); return ev(`document.querySelector('#update-fail-banner').textContent.replace(/\\s+/g, ' ').trim()`); };
        const t1 = await rb('did_not_start'), t2 = await rb('swap_failed'), t3 = await rb('');
        check(`a roll-back says why when the server gives a reason (${t1.slice(0, 120)})`, /The new version did not start\./.test(t1) && /could not be put in place/.test(t2) && !/did not start|put in place/.test(t3) && /went back to 1\.4\.130/.test(t3));
        const t4 = await rb('did_not_start', true), t5 = await rb('did_not_start', false);
        check(`a roll-back whose restart failed says Coral was stopped (${t4.slice(0, 200)})`, /Coral was stopped by an update that could not restart it/.test(t4) && /It is on 1\.4\.130\./.test(t4) && !/Your agents are unaffected/.test(t4));
        check('and one that restarted does not', !/could not restart it/.test(t5) && /Your agents are unaffected/.test(t5));
        await setStatus(idle({ state: 'failed', last_result: { at: '2026-10-19T10:00:00Z', from: '1.4.125', to: '1.4.126', result: 'refused', by: 'local' } }));
        const vt = await ev(`document.querySelector('#update-fail-banner').textContent.replace(/\\s+/g, ' ').trim()`);
        check(`a failed verification has the docs' words (${vt.slice(0, 120)})`, /Coral found an update \(v1\.4\.126\) it could not verify, so it was not installed\. Your version is unchanged\./.test(vt));
        const againShown = async () => ev(`!document.querySelector('#update-fail-banner .update-fail-again').hidden`);
        await ev(`import(${S} + 'update_api.js').then(m => { m.serverHas.check = false; return true; })`); await setStatus(idle({ state: 'failed', last_result: { at: '2026-10-19T10:00:30Z', from: '1.4.125', to: '1.4.126', result: 'refused', by: 'local' } })); // a server without the check route
        check('Try again is not offered when there is no way to check', (await againShown()) === false);
        await ev(`import(${S} + 'update_api.js').then(m => { m.serverHas.check = true; return true; })`); await setStatus(idle({ state: 'failed', last_result: { at: '2026-10-19T10:01:00Z', from: '1.4.125', to: '1.4.126', result: 'refused', by: 'local' } }));
        check('and is offered when the server can check', (await againShown()) === true);
        await ev(`import(${S} + 'update_api.js').then(m => { m.serverHas.check = false; return true; })`);
        await setStatus(staged({ installed: '1.4.130', last_result: { at: '2026-10-20T08:21:00Z', from: '1.4.130', to: '1.4.131', result: 'failed_rolled_back', by: 'local' } }));
        check('a rolled-back update that can be relaunched again offers Try again', (await againShown()) === true);

        console.log('6. the relaunch dialog says what happens next, per install kind');
        for (const [kind, want] of [['app', /may open the disk image/], ['msi', /Windows will ask for your permission/], ['portable', /swap in the update/], ['tarball', /stays stopped: start it again/], ['', /does not start again by itself, start it/]]) {
            await ev(`window.__relaunch = { status: 202, body: { ok: true } }; import(${S} + 'update_api.js').then(a => import(${S} + 'update_dialog.js').then(d => d.openUpdateDialog(a.normalizeStatus(${JSON.stringify(staged(kind ? { install_kind: kind } : {}))})))).then(() => true)`);
            await settle(200);
            await ev(`document.querySelector('#update-confirm-modal .update-go').click(); true`);
            await waitFor(`!!document.querySelector('#update-confirm-modal .update-note')`, `${kind || 'unknown'}: the note`, 6000);
            const note = await ev(`document.querySelector('#update-confirm-modal .update-note').textContent`);
            check(`${kind || '(no kind)'}: "${note}"; "Not now?" is gone once it is sent`, want.test(note) && !/reconnects when Coral is back/.test(note) && (await ev(`!document.querySelector('#update-confirm-modal .update-later-note')`)));
            await ev(`document.querySelector('#update-confirm-modal .update-later').click(); true`); await settle(100);
        }
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        await client.close(); process.exit(failed ? 1 : 0);
    } catch (e) { console.error('FAIL', e && e.stack || e); await client.close().catch(() => {}); process.exit(1); }
})();
