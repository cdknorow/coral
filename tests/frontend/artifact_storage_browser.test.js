// Free space and automatic cleanup (#3554), on this computer's dashboard: Settings shows the store
// by age; Prune and Shrink check first and say "Nothing to do" when nothing qualifies; automatic
// cleanup, once turned on, waits for a review: "Review and start" shows what it would do with
// focus on Cancel, and starting it clears the wait; and an artifact can be kept (never pruned)
// from the agent's Artifacts tab, which survives a reload.
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
    const until = (expr, what) => waitFor(expr, what, { timeout: 8000, soft: true });
    let sid = null, name = null;
    const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await Emulation.setFocusEmulationEnabled({ enabled: true });
        let loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();

        const launch = await ev(`fetch('/api/sessions/launch', { method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ working_dir: '/tmp', agent_type: 'terminal', display_name: 'Storage QA' }) }).then(r => r.json())`);
        sid = launch.session_id; name = launch.session_name || launch.name;
        const up = await ev(`fetch('/api/agent/artifacts?session_id=${sid}', { method: 'POST', headers: { 'X-Artifact-Name': 'keep-me.txt', 'X-Artifact-Media-Type': 'text/plain' },
            body: 'an artifact to keep ' + Date.now() }).then(async r => ({ status: r.status, body: await r.json() }))`);
        const hex = String(up.body.uri || '').split('/').pop();

        // Settings: the store by age, and Prune/Shrink check first.
        await ev(`window.showSettingsModal(); true`);
        await until(`/by age: /i.test((document.getElementById('settings-storage-detail') || {}).textContent || '')`, 'the breakdown'); // the text says "Artifacts by age:": this wait looked for "By age: ", never matched, and cost its whole timeout every run
        const detail = await ev(`document.getElementById('settings-storage-detail').innerText`);
        check('Settings shows the store by age', /Artifacts by age: <1d /.test(detail) && /Task diffs by age: /.test(detail), JSON.stringify(detail));
        // The store these two checks read is the server's own, which a session of this run, or an earlier suite on the same server, may have left saved diffs in: a Shrink that has something to shrink asks for a confirmation and says nothing in the line. The page's answer to an empty store is what is under test, so the dry run is answered empty.
        await ev(`(() => { window.__realFetch = window.fetch; window.fetch = (u, o = {}) => String(u).includes('/api/artifacts/prune') && o.method === 'POST' ? Promise.resolve(new Response(JSON.stringify({ dry_run: true, kinds: {}, freed: 0, removed: 0 }), { status: 200, headers: { 'content-type': 'application/json' } })) : window.__realFetch(u, o); })(); true`);
        await ev(`document.getElementById('settings-prune-kind').value = 'objects'; document.getElementById('settings-prune-days').value = '7'; document.getElementById('settings-storage-prune').click(); true`);
        await until(`(document.getElementById('settings-storage-result') || {}).textContent === 'Nothing to do.'`, 'the prune check');
        check('Prune of week-old unlinked artifacts finds nothing on a fresh store, and asks nothing',
            (await ev(`document.getElementById('settings-storage-result').textContent`)) === 'Nothing to do.' &&
            (await ev(`getComputedStyle(document.getElementById('confirm-modal')).display === 'none'`)), JSON.stringify(await ev(`document.getElementById('settings-storage-result').textContent`)));
        await ev(`document.getElementById('settings-storage-shrink').click(); true`);
        await until(`(document.getElementById('settings-storage-result') || {}).textContent === 'Nothing to do.'`, 'the shrink check');
        { const t = await ev(`document.getElementById('settings-storage-result').textContent`); check('Shrink with no stored diffs: nothing to do', t === 'Nothing to do.', JSON.stringify(t)); }
        await ev(`window.fetch = window.__realFetch; true`); // the real answers for what follows
        await ev(`window.hideSettingsModal && window.hideSettingsModal(); true`);

        // Automatic cleanup: turned on, it waits for a review.
        const put = await ev(`fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ artifact_retention_days: 30, artifact_retention_kinds: 'diffs' }) }).then(r => r.status)`);
        await ev(`window.showSettingsModal(); true`);
        await until(`!document.getElementById('settings-retention-review').hidden`, 'the Review button');
        const waiting = await ev(`({ status: document.getElementById('settings-retention-status').innerText, days: document.getElementById('settings-retention-days').value, review: !document.getElementById('settings-retention-review').hidden })`);
        check('turned on (30 days), it says it waits for a review and offers one', put === 200 && waiting.days === '30' && waiting.review && /waiting: review what it would do/.test(waiting.status), JSON.stringify(waiting));
        await ev(`document.getElementById('settings-retention-review').click(); true`);
        await until(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the review');
        await until(`document.activeElement && document.activeElement.closest('#confirm-modal') && document.activeElement.id !== 'confirm-modal-yes'`, 'focus on Cancel');
        const dlg = await ev(`({ text: document.getElementById('confirm-modal-message').textContent, focus: document.activeElement.textContent.trim() })`);
        check('the review says what the first run would remove (at most 2 GiB a run), focus on Cancel',
            /first run would remove/.test(dlg.text) && /2 GiB a run/.test(dlg.text) && dlg.focus === 'Cancel', JSON.stringify(dlg));
        await ev(`document.getElementById('confirm-modal-yes').click(); true`);
        await until(`document.getElementById('settings-retention-review').hidden`, 'the wait cleared');
        const started = await ev(`({ status: document.getElementById('settings-retention-status').innerText, review: !document.getElementById('settings-retention-review').hidden })`);
        check('starting it clears the wait', !started.review && /^On \(30 days\)\./.test(started.status), JSON.stringify(started));
        await ev(`window.hideSettingsModal && window.hideSettingsModal(); true`);
        await ev(`fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ artifact_retention_days: 0 }) }).then(r => r.status)`);

        // Keep, from the agent's Artifacts tab.
        loaded = Page.loadEventFired();
        await Page.navigate({ url: `${BASE}/agents/${sid}` });
        await loaded;
        await coralReady();
        await until(`!!document.getElementById('agentic-tab-artifacts')`, 'the Artifacts tab');
        await ev(`document.getElementById('agentic-tab-artifacts').click(); true`);
        const row = `[...document.querySelectorAll('.team-artifact-row')].find(r => r.textContent.includes('keep-me.txt'))`;
        await until(`!!(${row}) && !!(${row}).querySelector('.team-artifact-keep')`, 'the Keep toggle');
        const before = await ev(`(${row}).querySelector('.team-artifact-keep').getAttribute('aria-pressed')`);
        await ev(`(${row}).querySelector('.team-artifact-keep').click(); true`);
        await until(`(${row}).querySelector('.team-artifact-keep').getAttribute('aria-pressed') === 'true'`, 'kept');
        const list = await ev(`fetch('/api/system/artifact-store/kept').then(r => r.json())`);
        check('Keep marks it (pressed) and Coral lists it as kept', before === 'false' && (list.kept || []).includes(hex), JSON.stringify({ before, list }));
        loaded = Page.loadEventFired();
        await Page.reload();
        await loaded;
        await coralReady();
        await until(`!!document.getElementById('agentic-tab-artifacts')`, 'the Artifacts tab again');
        await ev(`document.getElementById('agentic-tab-artifacts').click(); true`);
        await until(`!!(${row}) && (${row}).querySelector('.team-artifact-keep').getAttribute('aria-pressed') === 'true'`, 'kept after the reload');
        check('still kept after a reload', (await ev(`(${row}).querySelector('.team-artifact-keep').getAttribute('aria-pressed')`)) === 'true');
        await ev(`(${row}).querySelector('.team-artifact-keep').click(); true`);
        await until(`(${row}).querySelector('.team-artifact-keep').getAttribute('aria-pressed') === 'false'`, 'released');
        const after = await ev(`fetch('/api/system/artifact-store/kept').then(r => r.json())`);
        check('a second click releases it', !(after.kept || []).includes(hex), JSON.stringify(after));

        // Not offered in the pop-out.
        await ev(`document.body.classList.add('popout-mode'); true`);
        await ev(`(() => { const b = [...document.querySelectorAll('#agentic-panel-artifacts-view .team-artifacts-action')].find(x => x.getAttribute('aria-label') === 'Refresh'); b && b.click(); return !!b; })()`);
        await until(`!!(${row}) && !(${row}).querySelector('.team-artifact-keep')`, 'the pop-out list');
        check('no Keep in the pop-out', !(await ev(`!!(${row}) && !!(${row}).querySelector('.team-artifact-keep')`)));
        await ev(`document.body.classList.remove('popout-mode'); true`);
    } catch (e) {
        check('no error', false, e.message);
    } finally {
        if (sid) await ev(`fetch('/api/sessions/live/${encodeURIComponent(name)}/kill', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ agent_type: 'terminal', session_id: '${sid}' }) }).then(r => r.status)`).catch(() => {});
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
    }
    const failed = results.filter(x => !x).length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(2); });
