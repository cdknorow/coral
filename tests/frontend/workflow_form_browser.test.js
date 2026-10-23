// The New workflow form (#6540 mock, #6578): Name (with the allowed characters beside its label), then Steps with one empty step already showing, numbered from 1; Description, Repository path and
// Time limit behind "More options"; the limit is a number with a unit. Every control of a step has its own name; errors are in words and on the field. 1280 and 390 px, both themes for contrast.
// The save is stood in at the page's fetch.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

const STUB = `(() => {
    const real = window.fetch; window.__saved = [];
    const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
    window.fetch = (u, o = {}) => { const p = new URL(String(u), location.origin).pathname, m = (o.method || 'GET').toUpperCase();
        if (p === '/api/workflows' && m === 'POST') { window.__saved.push(JSON.parse(o.body)); return json({ id: 5, name: 'x' }); }
        return real(u, o); };
    return true; })()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const type = async (sel, text) => { await ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); e.focus(); e.select && e.select(); return true; })()`); await Input.insertText({ text }); };
    const vis = sel => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && e.checkVisibility(); })()`;
    const click = sel => ev(`document.querySelector(${JSON.stringify(sel)}).click(); true`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            const at = `at ${w}px: `;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`); await ev(STUB);
            await ev(`window.switchNavTab('workflows'); window.showWorkflowCreateModal(); true`);
            await waitFor(`document.getElementById('wf-create-modal').style.display === 'flex'`, 'the dialog', 8000).catch(async e => { throw new Error(e.message + ' ' + await ev(`String(typeof window.showWorkflowCreateModal)`)); });
            await installAudit(ev);

            // 1. first screen: Name, Steps with one step showing; the rest behind More options
            assert.equal(await ev(vis('#wf-input-name')), true, at + 'Name');
            const pat = await ev(`(() => { const m = document.querySelector('#wf-create-modal .modal-content').getBoundingClientRect(), x = document.querySelector('#wf-create-modal .modal-close-btn').getBoundingClientRect(), f = document.getElementById('wf-modal-save').getBoundingClientRect(); return { xRight: Math.round(m.right - x.right), xTop: Math.round(x.top - m.top), btnRight: Math.round(m.right - f.right) }; })()`);
            assert.ok(pat.xRight >= 0 && pat.xRight <= 40 && pat.xTop <= 40, at + 'the X is at the top right: ' + JSON.stringify(pat));
            assert.ok(pat.btnRight <= 40, at + 'the buttons are at the bottom right, as in New job: ' + JSON.stringify(pat));
            assert.equal(await ev(`document.querySelectorAll('#wf-steps-editor .wf-step-editor-item').length`), 1, at + 'one empty step is already there');
            for (const id of ['#wf-input-desc', '#wf-input-repo', '#wf-input-max-dur']) assert.equal(await ev(vis(id)), false, at + id + ' is behind More options');
            assert.equal(await ev(`document.getElementById('wf-more').open`), false);
            assert.equal(await ev(`document.querySelector('label[for="wf-input-name"]').textContent.replace(/\\s+/g, ' ').trim()`), 'Name (required)');
            assert.match(await ev(`document.getElementById('wf-name-help').textContent`), /Letters, numbers, hyphens and underscores/);
            assert.equal(await ev(`document.getElementById('wf-input-name').getAttribute('aria-describedby')`), 'wf-name-help');
            assert.equal(await ev(`document.querySelector('.wf-step-num').textContent`), 'Step 1', at + 'steps are numbered from 1');
            const names = await ev(`[...document.querySelectorAll('#wf-steps-editor .wf-step-editor-item [aria-label]')].map(e => e.getAttribute('aria-label'))`);
            assert.deepEqual(names, ['Step 1 type', 'Step 1 name', 'Remove step 1', 'Step 1 command'], at + 'each control of a step has its own name: ' + JSON.stringify(names));
            // the Name label sits right above its field, and Steps starts within the first screen
            const gap = await ev(`(() => { const l = document.querySelector('label[for="wf-input-name"]').getBoundingClientRect(), i = document.getElementById('wf-input-name').getBoundingClientRect(); return Math.round(i.top - l.bottom); })()`);
            assert.ok(gap <= 12, at + 'the field sits right under its label (the hint is below the field): ' + gap);
            const stepsTop = await ev(`Math.round(document.getElementById('wf-steps-editor').getBoundingClientRect().top)`);
            assert.ok(stepsTop < h - 200, at + 'the steps start on the first screen: ' + stepsTop);
            await shot(`wf-form-${w}`);

            // 2. contrast, both themes
            for (const theme of ['dark', 'light']) {
                await setTheme(ev, theme);
                const c = await auditRegion(ev, ['#wf-create-modal .modal-content']);
                assert.ok(c.checked && !c.fails.length, at + theme + ' contrast: ' + JSON.stringify(c.fails));
            }
            await setTheme(ev, 'dark');
            assert.equal(await ev(`document.documentElement.scrollWidth <= innerWidth`), true, at + 'no sideways scroll');
            if (mobile) {
                const edge = await ev(`Math.round(document.getElementById('wf-input-name').getBoundingClientRect().left)`);
                assert.ok(edge <= 28, at + 'the fields start close to the edge: ' + edge);
                const nameRow = await ev(`(() => { const n = document.querySelector('.wf-step-name-input').getBoundingClientRect(), s = document.querySelector('.wf-step-type-select').getBoundingClientRect(); return { sameRow: Math.abs(n.top - s.top) < 4, h: Math.round(n.height) }; })()`);
                assert.equal(nameRow.sameRow, false, at + "a step's name takes its own row on a phone");
                assert.ok(nameRow.h >= 44, at + "the step's name is a 44 px target: " + nameRow.h);
            }

            // 3. steps: add one, numbered; errors in words, on the field
            await click('.wf-add-step-btn');
            assert.deepEqual(await ev(`[...document.querySelectorAll('.wf-step-num')].map(e => e.textContent)`), ['Step 1', 'Step 2']);
            await click('#wf-modal-save');
            assert.equal(await ev(`document.activeElement.classList.contains('wf-step-name-input')`), true, at + 'focus is on the step that needs a name');
            assert.match(await ev(`document.getElementById('wf-modal-error').textContent`), /^Step 1 needs a name\.$/, at + 'the first gap, in words');
            await type('.wf-step-editor-item:nth-child(1) .wf-step-name-input', 'Install');
            await click('#wf-modal-save');
            assert.match(await ev(`document.getElementById('wf-modal-error').textContent`), /^Step 1 \(Install\) needs a command\.$/, at);
            await type('.wf-step-editor-item:nth-child(1) .wf-step-content', 'npm ci');
            await type('.wf-step-editor-item:nth-child(2) .wf-step-name-input', 'Test');
            await type('.wf-step-editor-item:nth-child(2) .wf-step-content', 'npm test');
            await click('#wf-modal-save');
            assert.match(await ev(`document.getElementById('wf-modal-error').textContent`), /^Give the workflow a name\.$/, at);
            assert.equal(await ev(`document.activeElement.id`), 'wf-input-name');
            await type('#wf-input-name', 'nightly-build');

            // 4. More options: the time limit in words
            await click('#wf-more > summary');
            assert.equal(await ev(vis('#wf-input-max-dur')), true);
            assert.equal(await ev(`document.getElementById('wf-input-max-dur').value + ' ' + document.getElementById('wf-input-max-dur-unit').selectedOptions[0].textContent`), '1 hours');
            await type('#wf-input-max-dur', '25');
            assert.match(await ev(`document.getElementById('wf-limit-msg').textContent`), /at most 24 hours/, at + 'over the ceiling, in words');
            await shot(`wf-form-options-${w}`);
            await type('#wf-input-max-dur', '0');
            assert.match(await ev(`document.getElementById('wf-limit-msg').textContent`), /whole number of one or more/);
            await type('#wf-input-max-dur', '90');
            await ev(`(() => { const u = document.getElementById('wf-input-max-dur-unit'); u.value = '60'; u.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            assert.equal(await ev(`document.getElementById('wf-limit-msg').hidden`), true, at + 'a good limit clears the sentence');
            await type('#wf-input-desc', 'Build and test');

            // 5. Save sends steps and the limit in seconds
            await click('#wf-modal-save');
            await waitFor(`window.__saved.length === 1`, 'the save', 8000);
            const body = await ev(`window.__saved[0]`);
            assert.equal(body.name, 'nightly-build'); assert.equal(body.max_duration_s, 5400, at + '90 minutes is 5400 seconds'); assert.equal(body.description, 'Build and test');
            assert.deepEqual(body.steps.map(s => [s.name, s.type, s.command]), [['Install', 'shell', 'npm ci'], ['Test', 'shell', 'npm test']]);

            // 6. a fresh form shows no mark from the last, More options closed again, one step
            await ev(`window.showWorkflowCreateModal(); true`);
            assert.equal(await ev(`document.getElementById('wf-more').open`), false, at + 'More options starts closed again');
            assert.equal(await ev(`document.querySelectorAll('#wf-steps-editor .wf-step-editor-item').length`), 1);
            assert.equal(await ev(`document.querySelectorAll('#wf-create-modal [aria-invalid]').length`), 0, at + 'no mark is left over');
        }
        // the Workflows page behind the dialog: no count, no empty pill after the title
        await ev(`document.querySelector('#wf-create-modal .modal-close-btn').click(); window.switchNavTab('workflows'); true`);
        await waitFor(`document.getElementById('wf-title').textContent === 'Workflows'`, 'the Workflows page', 8000);
        assert.equal(await ev(`(() => { const b = document.getElementById('wf-count-badge'); return !b.textContent.trim() && !b.checkVisibility(); })()`), true, 'with no count the badge is not drawn');
        console.log('PASS workflow form: Name, Steps first, the rest behind More options; errors in words');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
