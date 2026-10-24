// The New job form (#6540 mock, #6578): Name, Workflow and When first; the schedule preview is one sentence; Description, Time zone and Time limit sit behind "More options"; the time limit is
// a number with a unit; an agent job's branch, flags and cleanup are behind More options too; with no workflow the field says so and offers to make one. Errors are in words and on the field.
// 1280 and 390 px, both themes for contrast. The workflow list and the save are stood in at the page's fetch; the schedule check is the real server's.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

const STUB = `(() => {
    const real = window.fetch; window.__saved = []; window.__wf = [{ id: 7, name: 'nightly-build', description: 'Build and test' }];
    const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
    window.fetch = (u, o = {}) => { const p = new URL(String(u), location.origin).pathname, m = (o.method || 'GET').toUpperCase();
        if (p === '/api/workflows' && m === 'GET') return json({ workflows: window.__wf });
        if (p === '/api/scheduled/jobs' && m === 'POST') { window.__saved.push(JSON.parse(o.body)); return json({ id: 99 }); }
        return real(u, o); };
    return true; })()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`); fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const type = async (sel, text) => { await ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); e.focus(); e.select && e.select(); return true; })()`); await Input.insertText({ text }); };
    const vis = sel => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && e.checkVisibility(); })()`; // checkVisibility: a closed disclosure's contents count as not shown
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            const at = `at ${w}px: `;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`); await ev(STUB);
            await ev(`window.showJobModal(); true`);
            await waitFor(`document.querySelectorAll('#job-modal-workflow option').length === 2`, 'the workflow list', 8000);
            await waitFor(`/Runs every hour/.test(document.getElementById('cron-preview').textContent)`, 'the preview', 8000);
            await installAudit(ev);

            // 1. what is on the form first
            assert.equal(await ev(vis('#job-modal-name')), true, at + 'Name');
            assert.equal(await ev(vis('#job-modal-workflow')), true, at + 'Workflow');
            for (const id of ['#job-modal-description', '#job-modal-timezone', '#job-modal-timeout', '#job-modal-cleanup', '#job-modal-cron']) assert.equal(await ev(vis(id)), false, at + id + ' is not on the first screen');
            const labels = await ev(`[...document.querySelectorAll('#job-modal label.job-field, #job-modal #sched-label')].filter(e => e.checkVisibility()).map(e => e.firstChild.textContent.trim() + '|' + (e.querySelector('.job-required') ? 'req' : ''))`);
            assert.deepEqual(labels, ['Name|req', 'Workflow|req', 'When|'], at + 'labels, in order, with "(required)" in words: ' + JSON.stringify(labels));
            assert.deepEqual(await ev(`[...document.querySelectorAll('.sched-preset')].map(b => b.textContent.trim())`), ['Every hour', 'Every 6 hours', 'Every day at 02:00', 'Mondays at 09:00', 'Custom'], at + 'presets say the time');
            const prev = await ev(`document.getElementById('cron-preview').textContent`);
            assert.match(prev, /^Runs every hour \(UTC\)\. Next: (today|tomorrow) \d\d:\d\d, your time\.$/, at + 'the preview is one sentence: ' + prev);
            assert.equal(await ev(`document.getElementById('cron-preview').getAttribute('role')`), 'status');
            assert.equal(await ev(`document.getElementById('job-modal-more').open`), false, at + 'More options starts closed');
            await ev(`document.querySelector('.sched-preset:nth-child(3)').click(); true`);
            // only the chosen preset looks chosen, right after the click (no fade from the one before), and a hover or focus is not the chosen look
            const looks = await ev(`[...document.querySelectorAll('.sched-preset')].map(b => getComputedStyle(b).backgroundColor)`);
            const chosen = looks[2];
            assert.deepEqual(looks.map((c, i) => c === chosen && i !== 2), [false, false, false, false, false], at + 'no other preset has the chosen preset\'s fill: ' + JSON.stringify(looks));
            assert.deepEqual(await ev(`[...document.querySelectorAll('.sched-preset')].map(b => b.getAttribute('aria-pressed'))`), ['false', 'false', 'true', 'false', 'false'], at + 'and only one is pressed');
            // the dialog's pattern: a Close X at the top right, the buttons at the bottom right
            const pat = await ev(`(() => { const m = document.querySelector('#job-modal .modal-content').getBoundingClientRect(), x = document.querySelector('#job-modal .modal-close-btn'), f = document.getElementById('job-modal-submit').getBoundingClientRect(), c = x ? x.getBoundingClientRect() : null; return { hasX: !!x, xRight: c ? Math.round(m.right - c.right) : -1, xTop: c ? Math.round(c.top - m.top) : -1, name: x && x.getAttribute('aria-label'), btnRight: Math.round(m.right - f.right) }; })()`);
            assert.equal(pat.hasX, true, at + 'New job has a Close X'); assert.equal(pat.name, 'Close dialog');
            assert.ok(pat.xRight >= 0 && pat.xRight <= 40 && pat.xTop <= 40, at + 'the X is at the top right: ' + JSON.stringify(pat));
            assert.ok(pat.btnRight <= 40, at + 'the buttons are at the bottom right: ' + JSON.stringify(pat));
            await waitFor(`/every day at 02:00/.test(document.getElementById('cron-preview').textContent)`, 'daily preview', 8000);
            await shot(`job-form-${w}`);

            // 2. contrast, both themes
            for (const theme of ['dark', 'light']) {
                await setTheme(ev, theme);
                const c = await auditRegion(ev, ['#job-modal .modal-content']);
                assert.ok(c.checked && !c.fails.length, at + theme + ' contrast: ' + JSON.stringify(c.fails));
            }
            await setTheme(ev, 'dark');

            // 3. phone: no sideways scroll; the field's left edge is the 16 px gutter of the dialog, not a wide margin
            assert.equal(await ev(`document.documentElement.scrollWidth <= innerWidth`), true, at + 'no sideways scroll');
            if (mobile) {
                const edge = await ev(`Math.round(document.getElementById('job-modal-name').getBoundingClientRect().left)`);
                assert.ok(edge <= 28, at + 'the fields start close to the edge (a 16 px gutter plus the dialog border): left edge ' + edge);
            }

            // 4. Custom draws the cron field, with its words
            await ev(`document.querySelector('.sched-preset[data-cron=""]').click(); true`);
            assert.equal(await ev(vis('#job-modal-cron')), true, at + 'Custom shows the cron field');
            assert.equal(await ev(`document.getElementById('job-modal-cron').closest('label').firstChild.textContent.trim()`), 'Cron expression');
            await type('#job-modal-cron', 'nonsense');
            await waitFor(`/not a schedule Coral can read/.test(document.getElementById('cron-preview').textContent)`, 'a bad schedule in words', 8000);
            await shot(`job-form-custom-${w}`);
            await type('#job-modal-cron', '0 5 * * 1,3');
            await waitFor(`/Runs at 05:00, Mondays and Wednesdays \\(UTC\\)\\./.test(document.getElementById('cron-preview').textContent)`, 'a less common shape is said in words (#6956)', 8000);

            // 5. More options: time limit and zone in words
            await ev(`document.querySelector('#job-modal-more > summary').click(); true`);
            assert.equal(await ev(vis('#job-modal-timeout')), true, at + 'More options shows the time limit');
            assert.equal(await ev(`document.getElementById('job-modal-timeout').value + ' ' + document.getElementById('job-modal-timeout-unit').selectedOptions[0].textContent`), '1 hours', at + 'the default is 1 hour');
            assert.equal(await ev(vis('#job-modal-cleanup')), false, at + 'a workflow job has no cleanup choice');
            await type('#job-modal-timeout', '0');
            assert.match(await ev(`document.getElementById('job-modal-limit-msg').textContent`), /whole number of one or more/, at + 'zero, in words');
            await ev(`(() => { const u = document.getElementById('job-modal-timeout-unit'); u.value = '1'; u.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            await type('#job-modal-timeout', '30');
            assert.match(await ev(`document.getElementById('job-modal-limit-msg').textContent`), /at least 1 minute/, at + '30 seconds, in words');
            assert.equal(await ev(`document.getElementById('job-modal-timeout').getAttribute('aria-invalid')`), 'true');
            await type('#job-modal-timeout', '90');
            await ev(`(() => { const u = document.getElementById('job-modal-timeout-unit'); u.value = '60'; u.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            assert.equal(await ev(`document.getElementById('job-modal-limit-msg').hidden`), true, at + 'a good limit clears the sentence');
            await type('#job-modal-timezone', 'Mars/Olympus');
            await waitFor(`!document.getElementById('job-modal-zone-msg').hidden`, 'the zone sentence', 8000);
            assert.match(await ev(`document.getElementById('job-modal-zone-msg').textContent`), /"Mars\/Olympus" is not a time zone name\. Use one like UTC or Europe\/Berlin\./, at);
            await shot(`job-form-options-${w}`);
            await type('#job-modal-timezone', 'Europe/Berlin');
            await waitFor(`document.getElementById('job-modal-zone-msg').hidden`, 'the zone sentence clears', 8000);

            // 6. Save: Name and Workflow are asked for in words, then the job goes with its time limit in seconds
            await ev(`document.getElementById('job-modal-submit').click(); true`);
            assert.equal(await ev(`document.activeElement.id`), 'job-modal-name', at + 'Save with no name puts focus on Name');
            await type('#job-modal-name', 'nightly-tests');
            await ev(`document.getElementById('job-modal-submit').click(); true`);
            assert.equal(await ev(`document.activeElement.id`), 'job-modal-workflow', at + 'then on Workflow');
            await ev(`(() => { const s = document.getElementById('job-modal-workflow'); s.value = '7'; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            await ev(`document.getElementById('job-modal-submit').click(); true`);
            await waitFor(`window.__saved.length === 1`, 'the save', 8000);
            const body = await ev(`window.__saved[0]`);
            assert.equal(body.name, 'nightly-tests'); assert.equal(body.workflow_id, 7); assert.equal(body.job_type, 'workflow');
            assert.equal(body.max_duration_s, 5400, at + '90 minutes is 5400 seconds');
            assert.equal(body.timezone, 'Europe/Berlin'); assert.equal(body.cron_expr, '0 5 * * 1,3');

            // 7. an agent job: Repository, Agent and Prompt first; Base branch, Flags and Cleanup behind More options
            await ev(`window.showJobModal(); document.querySelector('.job-type-btn[data-type="prompt"]').click(); true`);
            assert.equal(await ev(vis('#job-modal-repo')) && await ev(vis('#job-modal-agent')) && await ev(vis('#job-modal-prompt')), true, at + 'an agent job shows Repository, Agent and Prompt');
            for (const id of ['#job-modal-branch', '#job-modal-flags', '#job-modal-cleanup']) assert.equal(await ev(vis(id)), false, at + id + ' is behind More options');
            await ev(`document.querySelector('#job-modal-more > summary').click(); true`);
            for (const id of ['#job-modal-branch', '#job-modal-flags', '#job-modal-cleanup']) assert.equal(await ev(vis(id)), true, at + id + ' shows in More options');
            await shot(`job-form-agent-${w}`);

            // 8. no workflows yet: the field says so and offers to make one
            await ev(`window.__wf = []; window.showJobModal(); true`);
            await waitFor(`!document.getElementById('job-modal-no-workflows').hidden`, 'the no-workflow line', 8000);
            assert.match(await ev(`document.getElementById('job-modal-no-workflows').textContent.replace(/\\s+/g, ' ').trim()`), /^No workflows yet\. A job runs a workflow on a schedule\. Create a workflow$/, at);
            await shot(`job-form-no-workflows-${w}`);
            await ev(`document.querySelector('#job-modal-no-workflows button').click(); true`);
            await waitFor(`document.getElementById('wf-create-modal') && getComputedStyle(document.getElementById('wf-create-modal')).display !== 'none'`, 'New workflow opens', 8000);
            assert.equal(await ev(`getComputedStyle(document.getElementById('job-modal')).display`), 'none', at + 'the job form closes');
            await ev(`document.querySelector('#wf-create-modal .modal-close-btn').click(); window.showJobModal(); true`);
            await ev(`document.querySelector('#job-modal .modal-close-btn').click(); true`);
            assert.equal(await ev(`getComputedStyle(document.getElementById('job-modal')).display`), 'none', at + 'the X closes New job');
        }
        console.log('PASS job form: Name, Workflow and When first; the rest behind More options; errors in words');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
