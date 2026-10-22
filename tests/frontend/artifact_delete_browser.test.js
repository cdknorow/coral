// The Operator deletes an artifact from the dashboard (#3553): an agent's Artifacts tab lists
// its upload with a Delete button; the confirmation has no default (focus on Cancel) and says
// links will show "unavailable" and a browser's cached copy may remain; Delete removes the row
// and the artifact answers 404; the button isn't offered in the pop-out; and Settings shows the
// store's size, with the saved diffs on their own lines.
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
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await Emulation.setFocusEmulationEnabled({ enabled: true });
        let loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();

        // A terminal agent uploads a file (default mode: its session id, no token).
        const launch = await ev(`fetch('/api/sessions/launch', { method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ working_dir: '/tmp', agent_type: 'terminal', display_name: 'Delete QA' }) }).then(r => r.json())`);
        sid = launch.session_id; name = launch.session_name || launch.name;
        const up = await ev(`fetch('/api/agent/artifacts?session_id=${sid}', { method: 'POST', headers: { 'X-Artifact-Name': 'delete-me.txt', 'X-Artifact-Media-Type': 'text/plain' },
            body: 'an artifact for the delete test ' + Date.now() }).then(async r => ({ status: r.status, body: await r.json() }))`);
        check('the agent uploads an artifact', up.status === 201 && /^coral:\/\/artifacts\/[a-f0-9]{64}$/.test(up.body.uri), JSON.stringify(up).slice(0, 200));
        const hex = String(up.body.uri).split('/').pop();
        const up2 = await ev(`fetch('/api/agent/artifacts?session_id=${sid}', { method: 'POST', headers: { 'X-Artifact-Name': 'kept.txt' }, body: 'kept ' + Date.now() }).then(r => r.status)`);

        // Its Artifacts tab, on the agent's page.
        loaded = Page.loadEventFired();
        await Page.navigate({ url: `${BASE}/agents/${sid}` });
        await loaded;
        await coralReady();
        await until(`!!document.getElementById('agentic-tab-artifacts')`, 'the Artifacts tab');
        await ev(`document.getElementById('agentic-tab-artifacts').click(); true`);
        const row = `[...document.querySelectorAll('.team-artifact-row')].find(r => r.textContent.includes('delete-me.txt'))`;
        await until(`!!(${row})`, 'the uploaded artifact');
        const btn = await ev(`(() => { const b = (${row}) && (${row}).querySelector('.team-artifact-delete'); return b && { tag: b.tagName, type: b.type, aria: b.getAttribute('aria-label') }; })()`);
        check('its row has a Delete button', !!btn && btn.tag === 'BUTTON' && btn.aria === 'Delete delete-me.txt', JSON.stringify(btn));

        // The confirmation: no default, honest text.
        await ev(`(${row}).querySelector('.team-artifact-delete').click(); true`);
        await until(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirmation');
        await until(`document.activeElement && document.activeElement.closest('#confirm-modal') && document.activeElement.id !== 'confirm-modal-yes'`, 'focus on Cancel');
        const dlg = await ev(`({ text: document.getElementById('confirm-modal-message').textContent, focus: document.activeElement.textContent.trim() })`);
        check('the confirmation names the file, says links show "unavailable" and a cached copy may remain, focus on Cancel',
            dlg.text.includes('delete-me.txt') && dlg.text.includes('unavailable') && /a browser already cached may remain/.test(dlg.text) && dlg.focus === 'Cancel', JSON.stringify(dlg));
        // Cancel keeps it.
        await ev(`document.activeElement.click(); true`);
        let st = await ev(`fetch('/api/artifacts/${hex}').then(r => r.status)`);
        check('Cancel deletes nothing', st === 200 && (await ev(`!!(${row})`)), String(st));

        // Delete.
        await ev(`(${row}).querySelector('.team-artifact-delete').click(); true`);
        await until(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirmation again');
        await ev(`document.getElementById('confirm-modal-yes').click(); true`);
        await until(`!(${row})`, 'the row gone');
        st = await ev(`fetch('/api/artifacts/${hex}').then(r => r.status)`);
        const note = await ev(`((document.querySelector('.team-artifacts-notice') || {}).textContent || '')`);
        check('Delete removes the row, says so, and the artifact is gone (404)', !(await ev(`!!(${row})`)) && st === 404 && note === 'Deleted delete-me.txt.', JSON.stringify({ st, note }));

        // Not offered in the pop-out (its page is the agent, not the Operator's console).
        await ev(`document.body.classList.add('popout-mode'); true`);
        const kept = `[...document.querySelectorAll('.team-artifact-row')].find(r => r.textContent.includes('kept.txt'))`;
        await ev(`(() => { const b = [...document.querySelectorAll('#agentic-panel-artifacts-view .team-artifacts-action')].find(x => x.getAttribute('aria-label') === 'Refresh'); b && b.click(); return !!b; })()`);
        await until(`!!(${kept}) && !(${kept}).querySelector('.team-artifact-delete')`, 'the list re-rendered in pop-out mode');
        const pop = await ev(`({ up: ${up2}, rows: [...document.querySelectorAll('.team-artifact-row')].map(r => r.textContent.slice(0, 40)), del: !!(${kept}) && !!(${kept}).querySelector('.team-artifact-delete'),
            status: (document.querySelector('.team-artifacts-status') || {}).textContent })`);
        check('no Delete in the pop-out', pop.up === 201 && pop.rows.some(r => r.includes('kept.txt')) && !pop.del, JSON.stringify(pop));
        await ev(`document.body.classList.remove('popout-mode'); true`);

        // Settings: the store's size, the diffs on their own lines, the limit.
        await ev(`window.showSettingsModal(); true`);
        await until(`/ of 20\\.0 GiB/.test((document.getElementById('settings-artifact-store-size') || {}).textContent || '')`, 'the size lines');
        const sizes = await ev(`({ text: document.getElementById('settings-artifact-store-size').innerText, limit: document.getElementById('settings-artifact-store-max').value })`);
        check('Settings shows the artifacts against the limit (the 20 GiB default), and the diffs',
            /^.* of 20\.0 GiB/m.test(sizes.text) && /Diffs: .* in \d+ files?/.test(sizes.text) && sizes.limit === '20', JSON.stringify(sizes));
        await ev(`window.hideSettingsModal && window.hideSettingsModal(); true`);
    } catch (e) {
        check('no error', false, e.message);
    } finally {
        if (sid) await ev(`fetch('/api/sessions/live/${encodeURIComponent(name)}/kill', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ agent_type: 'terminal', session_id: '${sid}' }) }).then(r => r.status)`).catch(() => {});
        await client.close();
    }
    const failed = results.filter(x => !x).length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(2); });
