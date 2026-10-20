// A multi-select question in the "Claude is asking" card (#5232; operator: "Multiselect is broken. There is no submit
// button."). Claude Code draws its boxes ("[✔] label") and one unnumbered Submit row under them; the card shows the
// options as checkboxes whose marks are the terminal's, and a Submit button. A box that is clicked is toggled by the
// server, which answers with the screen it read: the card shows that, never a mark flipped here; Submit sends no
// option, only {submit: true}, and the card goes when the prompt does. A single-select question is unchanged.
// A stand-in terminal lives in the page's fetch (the server's side is in prompt_answer_multi_test.go). Desktop and phone.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { waitStable } = require('./helpers/wait');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');

const BASE = require('./test_base').BASE;
const LABELS = ['Feels like a slideshow', "Doesn't show the game", 'Tone/story is off', 'Looks cheap/amateur', 'Type something'];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select, firstPage } = await connect();
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };

    // The stand-in terminal: the chat reply carries its screen; /answer-prompt toggles a box (unless it is told to ignore
    // the key, as a terminal that did not take it) or takes Submit, and records every body it was sent.
    const standIn = () => ev(`(() => {
        const inner = window.__innerFetch || (window.__innerFetch = window.fetch);
        const labels = ${JSON.stringify(LABELS)};
        window.__term = { checked: [true, true, false, true, false], gone: false, ignore: false, bodies: [], multi: true };
        const screen = () => {
            const t = window.__term;
            if (t.gone) return null;
            const options = labels.map((label, i) => ({ n: i + 1, label, selected: i === 0, action: 'select', checked: t.checked[i] || undefined }));
            if (!t.multi) return { question: 'Pick a color', options: [{ n: 1, label: 'Red', selected: true, action: 'select' }, { n: 2, label: 'Blue', action: 'select' }] };
            return { question: "What's not working about the trailer? (Pick everything that applies.)", options, multi: true, submit: { label: 'Submit', focused: false } };
        };
        const json = (status, body) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } }));
        window.fetch = (url, opts = {}) => {
            const u = String(url);
            if (/\\/api\\/sessions\\/live\\/[^/?]+\\/chat\\?/.test(u)) return inner(url, opts).then(async r => {
                const d = await r.clone().json().catch(() => null);
                if (!d) return r;
                const s = screen();
                if (s) d.prompt_options = s;
                d.pending = window.__term.pending || null;
                return new Response(JSON.stringify(d), { status: r.status, headers: { 'content-type': 'application/json' } });
            });
            if (!/\\/api\\/sessions\\/live\\/[^/?]+\\/answer-prompt(?:\\?|$)/.test(u)) return inner(url, opts);
            const body = JSON.parse(opts.body);
            window.__term.bodies.push(body);
            const t = window.__term;
            if (body.submit) { t.gone = true; return json(200, { ok: true, answered: 'Submit' }); }
            if (!t.ignore) t.checked[body.n - 1] = !t.checked[body.n - 1];
            return t.ignore ? json(409, { code: 'prompt_still_open', error: "The box didn't change. Check the terminal." })
                : json(200, { ok: true, toggled: body.label, checked: t.checked[body.n - 1], prompt_options: screen() });
        };
        return true;
    })()`);
    const WAITING = { waiting_for_input: true, waiting_summary: 'Notification: Claude is asking' };
    const setWaiting = on => ev(`import((window.__coralStatic || '/static/') + 'state.js').then(st => {
        const row = st.state.liveSessions.find(s => s.session_id === 'qa-multi-id');
        Object.assign(row, ${JSON.stringify(on ? WAITING : { waiting_for_input: false, waiting_summary: null })});
        window.__term.pending = ${on ? JSON.stringify({ tool_name: 'AskUserQuestion', input: { questions: [{ question: 'x', multiSelect: true, options: LABELS.map(label => ({ label, description: '' })) }] } }) : 'null'};
        return true; })`);
    const card = () => ev(`(() => {
        const c = document.querySelector('#live-history-messages > .chat-needs-input');
        if (!c) return null;
        const boxes = [...c.querySelectorAll('.cni-check')];
        const submit = c.querySelector('.cni-submit');
        const r = c.getBoundingClientRect();
        const wide = [...c.querySelectorAll('button')].filter(b => b.getBoundingClientRect().right > innerWidth + 1).length;
        return { boxes: boxes.map(b => ({ n: Number(b.dataset.n), label: b.querySelector('.cni-answer-label').textContent, checked: b.getAttribute('aria-checked'), role: b.getAttribute('role'), disabled: b.disabled })),
            submit: submit ? { text: submit.textContent.trim(), disabled: submit.disabled, h: submit.getBoundingClientRect().height, w: submit.getBoundingClientRect().width } : null,
            plain: c.querySelectorAll('.cni-answer:not(.cni-check):not(.cni-submit):not(.cni-link)').length, wide, inView: r.right <= innerWidth + 1 && r.left >= -1 };
    })()`);
    const bodies = () => ev('window.__term.bodies');
    const click = sel => ev(`document.querySelector('#live-history-messages > .chat-needs-input ${sel}').click(); true`);

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [width, label] of [[null, 'desktop'], [390, 'phone']]) {
            for (const theme of ['dark', 'light']) {
                console.log(`${label}, ${theme}`);
                if (width) await Emulation.setDeviceMetricsOverride({ width, height: 844, deviceScaleFactor: 2, mobile: true });
                await Page.navigate({ url: BASE });
                await Page.loadEventFired();
                await install();
                await installAudit(ev); await setTheme(ev, theme);
                await ev(`localStorage.setItem('coral-live-view-mode', 'chat')`);
                await ev(`window.__m.add('qa-multi', 'M', 5, { sleeping: false, board_project: 'team-multi' })`);
                await select('qa-multi');
                await firstPage();
                await waitStable(ev, `[T.state.currentSession && T.state.currentSession.session_id, window.__m.requests.length].join('|')`, `${label}: the agent settles`);
                await standIn();
                await setWaiting(true);
                await waitFor(`document.querySelectorAll('#live-history-messages > .chat-needs-input .cni-check').length === 5`, `${label} ${theme}: the five checkboxes`);
                let c = await card();
                assert.deepEqual(c.boxes.map(b => b.label), LABELS, 'the labels are what follows the box');
                assert.deepEqual(c.boxes.map(b => b.checked), ['true', 'true', 'false', 'true', 'false'], 'the marks are the terminal\'s');
                assert.ok(c.boxes.every(b => b.role === 'checkbox'), 'each is a checkbox for a screen reader');
                assert.ok(c.submit, `${label} ${theme}: there is a Submit button`);
                assert.equal(c.submit.text, 'Submit');
                assert.ok(c.submit.h >= (width ? 32 : 24), `a button, not a sliver (${c.submit.h}px)`);
                assert.equal(c.wide, 0, 'nothing runs off the screen'); assert.equal(c.inView, true);
                const audit = await auditRegion(ev, ['#live-history-messages > .chat-needs-input']);
                assert.deepEqual(audit.fails, [], `${label} ${theme}: contrast (4.5:1): ` + JSON.stringify(audit.fails));
                await shot(`multiselect-${label}-${theme}-card`);

                // a box is toggled by the terminal: the mark changes only after the server's reply
                await click('.cni-check[data-n="3"]');
                await waitFor(`document.querySelector('#live-history-messages > .chat-needs-input .cni-check[data-n="3"]').getAttribute('aria-checked') === 'true'`, `${label} ${theme}: box 3 is ticked`);
                c = await card();
                assert.deepEqual(c.boxes.map(b => b.checked), ['true', 'true', 'true', 'true', 'false']);
                assert.ok(c.boxes.every(b => !b.disabled), 'the boxes are usable again');
                assert.deepEqual((await bodies()).map(b => [b.n, b.label]), [[3, 'Tone/story is off']], 'one request for the box, no Submit with it');
                // and again: it is unticked
                await click('.cni-check[data-n="3"]');
                await waitFor(`document.querySelector('#live-history-messages > .chat-needs-input .cni-check[data-n="3"]').getAttribute('aria-checked') === 'false'`, `${label} ${theme}: box 3 is unticked`);
                // "Type something" is a box too
                await click('.cni-check[data-n="5"]');
                await waitFor(`document.querySelector('#live-history-messages > .chat-needs-input .cni-check[data-n="5"]').getAttribute('aria-checked') === 'true'`, `${label} ${theme}: the Type something box`);
                await click('.cni-check[data-n="5"]');
                await waitFor(`document.querySelector('#live-history-messages > .chat-needs-input .cni-check[data-n="5"]').getAttribute('aria-checked') === 'false'`, 'unticked again');

                // a terminal that did not take the key: the mark does not move
                await ev(`window.__term.ignore = true; true`);
                await click('.cni-check[data-n="2"]');
                await waitFor(`!document.querySelector('#live-history-messages > .chat-needs-input .cni-check[data-n="2"]').disabled`, `${label} ${theme}: the box is usable after the refusal`);
                assert.equal((await card()).boxes[1].checked, 'true', 'a mark is never flipped on the card alone');
                await ev(`window.__term.ignore = false; true`);

                // Submit: one request, {submit: true}; the card goes with the prompt
                const before = (await bodies()).length;
                await click('.cni-submit');
                await waitFor(`window.__term.bodies.length > ${before}`, 'Submit was requested');
                await setWaiting(false); // the feed's next poll: the agent no longer waits
                await waitFor(`!document.querySelector('#live-history-messages > .chat-needs-input')`, `${label} ${theme}: the card goes after Submit`);
                const sent = (await bodies()).slice(before);
                assert.equal(sent.length, 1, 'one request');
                assert.deepEqual(sent[0], { session_id: 'qa-multi-id', agent_type: sent[0].agent_type, submit: true }, 'Submit sends no option');

                // single-select is unchanged: no boxes, no Submit button
                await ev(`window.__term.multi = false; window.__term.gone = false; true`);
                await setWaiting(false); await setWaiting(true);
                await waitFor(`document.querySelectorAll('#live-history-messages > .chat-needs-input .cni-answer').length >= 2`, `${label} ${theme}: the single-select card`);
                c = await card();
                assert.equal(c.boxes.length, 0, 'no checkboxes'); assert.equal(c.submit, null, 'no Submit button'); assert.equal(c.plain, 2, 'the two options as before');
                await setWaiting(false);
            }
        }
        console.log('multiselect_prompt: ok');
        await client.close();
        process.exit(0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        await client.close().catch(() => {});
        process.exit(1);
    }
})();
