// Sleeping agents (#3029): the transcript stays readable (scrollable, older
// pages autoload, nothing covers it), the composer is disabled and offers Wake
// and Restart… instead of sending, and waking re-enables it with one re-select.
// Local and hub-remote agents, the dashboard, a phone-width viewport and the
// popout.
//
// The agents' endpoints (/chat, /wake, /send, /keys) are answered by an in-page
// mock; everything else goes to the isolated test server.
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

const SLEEPING_PLACEHOLDER = 'Agent is sleeping — wake it to send commands';

(async () => {
    const { client, Page, Runtime, Emulation, ev, waitFor, sleep, PRELOAD, install, select, view, reqs, clearReqs,
        expectRange, firstPage, SCROLL_DIAG, scrollToTop, settled } = await connect();
    // Condition waits (#3118). quiet(): no new mocked request for 300 ms (a follow-up request, such as a second
    // re-select or a duplicate send, would have started by then), then everything in flight has settled.
    const { waitStable, nextFrame } = require('./helpers/wait').bind(ev);
    const quiet = async () => { await waitStable(`window.__m.requests.length`, 'the mocked requests to stop', 300).catch(() => {}); await settled(); };

    // The sleeping-agent checks shared by the local and hub-remote agents (A1–A4, B).
    async function sleepingAgentBasics(label, name, prefix, server) {
        await ev(`window.__m.add('${name}', '${prefix}', 600, { sleeping: true, server: '${server}' })`);
        await ev(`localStorage.setItem('coral-live-view-mode', 'terminal')`); // sleeping overrides a persisted Terminal
        await clearReqs();
        await select(name, server);
        const ids = await firstPage();
        const pageSize = ids.length;
        expectRange(ids, prefix, 600 - pageSize, 600, `${label}: history`);
        let v = await view();
        assert.ok(v.chatVisible, `${label}: the chat pane is shown for a sleeping agent`);
        assert.equal(v.overlayInDom, false, `${label}: no sleeping overlay`);
        assert.ok(v.chatUncovered, `${label}: nothing covers the history (hit ${v.hitDesc})`);
        assert.equal(await ev(`localStorage.getItem('coral-live-view-mode')`), 'terminal', `${label}: the persisted view mode is left alone`);
        let r = await reqs();
        if (server !== 'local') assert.ok(r.filter(x => x.kind === 'chat').every(x => x.remote === server), `${label}: chat goes through /api/remote/${server}`);
        console.log(`   ✓ ${label}: history shown (${pageSize} msgs), uncovered, mode kept`);

        await clearReqs();
        await scrollToTop();
        await waitFor(`window.__m.requests.some(r => r.older && r.settled)`, `${label}: older page`, 5000, SCROLL_DIAG);
        await settled();
        // The page is built in slices after its reply arrives (#3866): wait for its rows.
        await waitFor(`window.__rendered().length > ${pageSize}`, `${label}: the older page's rows to show`, 8000);
        await sleep(100);
        r = await reqs();
        assert.equal(r.filter(x => x.older).length, 1, `${label}: one older page per scroll-to-top`);
        const all = await ev('window.__rendered()');
        expectRange(all, prefix, 600 - all.length, 600, `${label}: after an older page`);
        assert.ok(all.length > pageSize, `${label}: older messages were prepended`);
        console.log(`   ✓ ${label}: scroll-up autoload (${all.length} msgs)`);

        v = await view();
        assert.ok(v.composer && v.wake && v.restart, `${label}: sleeping composer with Wake and Restart (${JSON.stringify(v)})`);
        assert.ok(v.paneSleeping, `${label}: #command-pane.agent-sleeping`);
        assert.equal(v.toolbarVisible, false, `${label}: toolbar (quick actions, send) hidden`);
        assert.equal(v.sendBtnsVisible, false, `${label}: send buttons and attachments hidden`);
        assert.equal(v.inputDisabled, true, `${label}: input disabled`);
        assert.equal(v.placeholder, SLEEPING_PLACEHOLDER, `${label}: sleeping placeholder`);
        console.log(`   ✓ ${label}: composer shows Wake + Restart, send controls hidden, input disabled`);

        // Nothing reaches a sleeping agent, whichever send path is used.
        await clearReqs();
        await ev(`(async () => {
            const input = document.getElementById('command-input');
            input.value = 'should not be sent';
            input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
            await window.sendCommand?.();
            window.sendQuickCommand?.('/compact');
            await window.sendRawKeys?.(['Enter'], { silent: true });
        })()`);
        await quiet();
        r = await reqs();
        const sent = r.filter(x => x.kind === 'send' || x.kind === 'keys');
        assert.equal(sent.length, 0, `${label}: nothing is sent to a sleeping agent (got ${JSON.stringify(sent.map(x => x.kind + ' ' + x.body))})`);
        await ev(`document.getElementById('command-input').value = 'my draft'`);
        console.log(`   ✓ ${label}: Enter / sendCommand / quick command / keys send nothing`);
        return pageSize;
    }

    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();

        console.log('A. Local dashboard');
        await sleepingAgentBasics('local', 'qa-sleep-a', 'A', 'local');

        console.log('A4. Wake: one POST, one re-select, input back with the awake placeholder and the draft');
        await ev(`window.__m.wakeDelay = 300`);
        // Chat stays the view after waking, so the one re-select shows the kept page and asks only for what came after (#3726).
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat')`);
        await clearReqs();
        await ev(`(() => { const b = document.getElementById('sleeping-wake-btn'); b.click(); b.click(); })()`);
        await waitFor(`document.getElementById('sleeping-wake-btn').textContent.includes('Waking')`, 'Waking… label', 1000);
        assert.equal(await ev(`document.getElementById('sleeping-wake-btn').disabled`), true, 'Wake is disabled while waking');
        await ev(`document.getElementById('sleeping-wake-btn').click()`);
        await waitFor(`!document.getElementById('command-input').disabled`, 'input re-enabled after wake', 5000);
        await quiet();
        await settled();
        let r = await reqs();
        assert.equal(r.filter(x => x.kind === 'wake').length, 1, 'one POST /wake for three clicks');
        assert.equal(await ev('window.__selCalls'), 1, 'exactly one re-select after wake');
        assert.equal(r.filter(x => x.first).length, 0, `no first-page /chat after wake: the kept page is shown (got ${r.filter(x => x.first).length})`);
        assert.ok(r.filter(x => x.kind === 'chat' && x.after > 0).length >= 1, 'it asks only for what came after the kept page');
        let v = await view();
        assert.equal(v.composer, false, 'sleeping composer hidden after wake');
        assert.equal(v.paneSleeping, false, 'agent-sleeping class removed');
        assert.notEqual(v.placeholder, SLEEPING_PLACEHOLDER, 'awake placeholder restored');
        assert.ok(v.placeholder.length > 0, 'an awake placeholder is shown');
        assert.equal(v.inputValue, 'my draft', 'the draft survives waking');
        assert.equal(v.wakeText, 'Wake', 'Wake button label restored');
        const afterWake = await ev('window.__rendered()');
        assert.equal(new Set(afterWake).size, afterWake.length, 'history not duplicated by the wake');
        console.log('   ✓ 1 POST /wake, the kept page and no first-page /chat, input + draft back');
        await ev(`window.__m.wakeDelay = 0`);

        console.log('A5. Wake failure keeps the agent asleep');
        for (const mode of ['notok', 'network']) {
            await ev(`window.__m.add('qa-sleep-f-${mode}', 'F', 50, { sleeping: true })`);
            await select(`qa-sleep-f-${mode}`);
            await firstPage();
            await ev(`window.__m.wakeResult = '${mode}'`);
            await clearReqs();
            await ev(`document.getElementById('sleeping-wake-btn').click()`);
            await waitFor(`window.__m.requests.some(r => r.kind === 'wake' && r.settled)`, 'wake attempt');
            await quiet(); await nextFrame();
            v = await view();
            assert.ok(v.composer && v.inputDisabled, `${mode}: still sleeping after a failed wake`);
            assert.equal(v.wakeText, 'Wake', `${mode}: Wake button restored`);
            assert.equal(v.wakeDisabled, false, `${mode}: Wake can be tried again`);
            assert.equal((await reqs()).filter(x => x.first).length, 0, `${mode}: no re-select after a failed wake`);
            console.log(`   ✓ wake ${mode}: stays asleep, button usable`);
        }
        await ev(`window.__m.wakeResult = 'ok'`);

        console.log('A6. Restart… opens the restart dialog');
        await ev(`window.__m.add('qa-sleep-r', 'R', 50, { sleeping: true })`);
        await select('qa-sleep-r');
        await firstPage();
        await ev(`document.getElementById('sleeping-restart-btn').click()`);
        await waitFor(`getComputedStyle(document.getElementById('restart-modal')).display !== 'none'`, 'restart modal', 2000);
        assert.match(await ev(`document.getElementById('restart-modal-name').textContent`), /qa-sleep-r/);
        await ev(`window.hideRestartModal ? window.hideRestartModal() : (document.getElementById('restart-modal').style.display = 'none')`);
        console.log('   ✓ restart modal opens for the sleeping agent');

        console.log('A7. Switching between sleeping and awake agents');
        await ev(`window.__m.add('qa-awake-b', 'B', 50, { sleeping: false })`);
        await ev(`window.__m.add('qa-sleep-c', 'C', 50, { sleeping: true })`);
        await select('qa-sleep-c');
        await firstPage();
        await select('qa-awake-b');
        v = await view();
        assert.equal(v.inputDisabled, false, 'awake agent: input enabled');
        assert.equal(v.composer, false, 'awake agent: no sleeping composer');
        assert.notEqual(v.placeholder, SLEEPING_PLACEHOLDER, 'awake agent: awake placeholder');
        await select('qa-sleep-c');
        v = await view();
        assert.ok(v.inputDisabled && v.composer, 'back to the sleeping agent: disabled + composer');
        // An input that was already disabled is not the sleeping view's to enable again.
        await select('qa-awake-b');
        await ev(`document.getElementById('command-input').disabled = true`);
        await select('qa-sleep-c');
        await select('qa-awake-b');
        assert.equal(await ev(`document.getElementById('command-input').disabled`), true, 'leaving a sleeping agent does not enable an input that was disabled before');
        await ev(`document.getElementById('command-input').disabled = false`);
        console.log('   ✓ composer and input follow the selected agent; an earlier disable is left alone');

        console.log('A8. Sleep and wake from elsewhere while viewing');
        await select('qa-awake-b');
        await firstPage();
        await clearReqs();
        await ev(`window.__feed('qa-awake-b-id', { sleeping: true })`);
        await waitFor(`window.__view().composer && window.__view().inputDisabled`, 'composer after a sleep from elsewhere', 3000);
        await ev(`window.__feed('qa-awake-b-id', { sleeping: false })`);
        await waitFor(`!window.__view().composer && !window.__view().inputDisabled`, 'awake after a wake from elsewhere', 3000);
        await quiet();
        // Wake clicked and the feed's flip arriving together: still one re-select.
        await ev(`window.__feed('qa-awake-b-id', { sleeping: true })`);
        await waitFor(`window.__view().composer`, 'sleeping again', 3000);
        await quiet();
        await settled();
        await clearReqs();
        await ev(`window.__m.wakeDelay = 150`);
        await ev(`(() => {
            document.getElementById('sleeping-wake-btn').click();
            setTimeout(() => window.__feed('qa-awake-b-id', { sleeping: false }), 50);
        })()`);
        await waitFor(`!window.__view().inputDisabled`, 'awake after wake + feed', 3000);
        await quiet();
        await settled();
        r = await reqs();
        assert.equal(await ev('window.__selCalls'), 1, `wake + feed flip together: one re-select (got ${await ev('window.__selCalls')})`);
        assert.equal(r.filter(x => x.first).length, 0, `wake + feed flip together: no first-page /chat, the kept page is shown (got ${r.filter(x => x.first).length})`);
        const ids = await ev('window.__rendered()');
        assert.equal(new Set(ids).size, ids.length, 'history not duplicated');
        await ev(`window.__m.wakeDelay = 0`);
        console.log('   ✓ view follows the feed; wake + feed = one re-select');

        console.log('A9. An agent opens at its newest message even after reading back in the previous one');
        for (let i = 0; i < 3; i++) {
            await ev(`window.__m.add('qa-read-x${i}', 'X', 600, { sleeping: true })`);
            await ev(`window.__m.add('qa-read-y${i}', 'Y', 600, { sleeping: true })`);
            await select(`qa-read-x${i}`);
            await firstPage();
            // The reader scrolls up a little (not to the sentinel): autoScroll goes false.
            await ev(`(() => { const c = document.getElementById('live-history-messages'); c.scrollTop = Math.max(c.scrollHeight - c.clientHeight - 1500, 400); c.dispatchEvent(new Event('scroll')); })()`);
            await waitFor(`T.state.autoScroll === false`, 'autoScroll off after reading back');
            await clearReqs();
            await select(`qa-read-y${i}`);
            await firstPage();
            await waitStable(`(c => c.scrollTop + ':' + c.scrollHeight)(document.getElementById('live-history-messages'))`, 'the history scroll position', 200).catch(() => {});
            const at = await ev(`(() => { const c = document.getElementById('live-history-messages'); return { bottomGap: Math.round(c.scrollHeight - c.scrollTop - c.clientHeight), autoScroll: T.state.autoScroll }; })()`);
            const older = (await reqs()).filter(x => x.older);
            assert.ok(at.bottomGap < 60, `switch ${i}: the next agent opens at the bottom (gap ${at.bottomGap}px, autoScroll ${at.autoScroll})`);
            assert.equal(older.length, 0, `switch ${i}: no older page is fetched on opening (got ${older.length})`);
        }
        console.log('   ✓ next agent opens at the bottom, no older fetch (3 switches)');

        console.log('B. Hub-remote sleeping agent');
        await sleepingAgentBasics('remote', 'qa-sleep-remote', 'H', 'qa-remote-1');
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat')`);
        await clearReqs();
        await ev(`document.getElementById('sleeping-wake-btn').click()`);
        await waitFor(`!document.getElementById('command-input').disabled`, 'remote wake', 5000);
        await quiet();
        r = await reqs();
        const wakes = r.filter(x => x.kind === 'wake');
        assert.equal(wakes.length, 1, 'one remote POST /wake');
        assert.equal(wakes[0].remote, 'qa-remote-1', 'wake goes through /api/remote/<id>');
        assert.equal(await ev('window.__selCalls'), 1, 'one re-select after the remote wake');
        assert.equal(r.filter(x => x.first).length, 0, 'no first-page /chat after the remote wake: the kept page is shown (#3726)');
        console.log('   ✓ remote wake via /api/remote/qa-remote-1, one re-select');

        console.log('C. Phone-width viewport');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`window.__m.add('qa-sleep-m', 'M', 300, { sleeping: true })`);
        await select('qa-sleep-m');
        await firstPage();
        await waitStable(`JSON.stringify(window.__view())`, 'the phone view', 200).catch(() => {});
        v = await view();
        assert.ok(v.chatVisible, 'phone: history visible');
        assert.ok(v.chatUncovered, `phone: nothing covers the history (hit ${v.hitDesc})`);
        const wakeCover = v.wakeTappable ? null : await ev(`(() => { const w = document.getElementById('sleeping-wake-btn'), b = w.getBoundingClientRect(), t = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2); const d = e => e ? e.tagName + (e.id ? '#' + e.id : '') + '.' + String(e.className).slice(0, 60) + ' "' + e.textContent.trim().slice(0, 60) + '"' : 'nothing'; const cs = t ? getComputedStyle(t) : null; return { wake: [Math.round(b.left), Math.round(b.top), Math.round(b.width), Math.round(b.height)], vh: innerHeight, covering: d(t), position: cs && cs.position, zIndex: cs && cs.zIndex, coverRect: t ? (r => [Math.round(r.left), Math.round(r.top), Math.round(r.width), Math.round(r.height)])(t.getBoundingClientRect()) : null }; })()`);
        assert.ok(v.wakeTappable, `phone: Wake on screen and not covered (${JSON.stringify(v)}; covered by ${JSON.stringify(wakeCover)})`);
        assert.ok(v.restartTappable, 'phone: Restart on screen and not covered');
        assert.ok(v.inputDisabled, 'phone: input disabled');
        assert.equal(v.sendBtnsVisible, false, 'phone: +T / send hidden');
        assert.equal(await ev(`document.documentElement.scrollWidth <= innerWidth + 1`), true, 'phone: no horizontal page scroll');
        await clearReqs();
        await scrollToTop();
        await waitFor(`window.__m.requests.some(r => r.older && r.settled)`, 'phone: older page');
        console.log('   ✓ phone: history + autoload, Wake/Restart tappable, send hidden');
        await Emulation.clearDeviceMetricsOverride();

        console.log('D. Popout of a sleeping agent');
        const PID = '44444444-4444-4444-8444-555555555571';
        await Page.addScriptToEvaluateOnNewDocument({ source: `window.__qaSeed = [{ name: 'qa-pop', prefix: 'P', n: 300, sleeping: true, session_id: '${PID}' }];` });
        await Page.navigate({ url: `${BASE}/agent/${PID}` });
        await Page.loadEventFired();
        await waitFor(`window._coralPopout && window._coralPopout.getState && window._coralPopout.getState() !== 'loading'`, 'popout to resolve', 8000);
        await install();
        await firstPage();
        v = await view();
        assert.ok(v.chatVisible && v.chatUncovered, `popout: history shown and uncovered (hit ${v.hitDesc})`);
        assert.ok(v.composer && v.wake, 'popout: sleeping composer with Wake');
        assert.equal(v.restart, false, 'popout: no Restart (no destructive controls in the popout)');
        assert.equal(v.inputDisabled, true, 'popout: input disabled');
        assert.equal(v.placeholder, SLEEPING_PLACEHOLDER, 'popout: sleeping placeholder');
        assert.equal(await ev(`window.__wsAttempts.filter(u => /\\/ws\\/terminal/.test(u)).length`), 0, 'popout: no terminal attach while sleeping');
        await clearReqs();
        await ev(`document.getElementById('sleeping-wake-btn').click()`);
        await waitFor(`!window.__view().composer`, 'popout: composer hidden after wake', 8000);
        await waitFor(`!document.getElementById('command-input').disabled`, 'popout: input enabled after wake', 8000);
        r = await reqs();
        assert.equal(r.filter(x => x.kind === 'wake').length, 1, 'popout: one POST /wake');
        // Input re-enables on the wake; the terminal socket opens after it, so wait for the attach itself.
        await waitFor(`window.__wsAttempts.some(u => /\\/ws\\/terminal/.test(u))`, 'popout: the terminal attaches after waking', 8000);
        console.log('   ✓ popout: history, Wake only, wake re-enables input and attaches');
        // Ended while asleep: the composer goes with it.
        await ev(`window.__feed('${PID}', { sleeping: true })`);
        await waitFor(`window.__view().composer`, 'popout: asleep again', 3000);
        await ev(`(() => { window.__m.agents['${PID}'].ended = true; window._coralHandleWsMessage({ type: 'coral_diff', changed: [], removed: ['${PID}'] }); })()`);
        await waitFor(`window._coralPopout.getState() === 'ended'`, 'popout: ended', 5000);
        v = await view();
        assert.equal(v.composer, false, 'popout: ended hides the sleeping composer');
        assert.equal(v.inputDisabled, true, 'popout: ended keeps the input disabled');
        console.log('   ✓ popout: ended hides the composer');

        console.log('\nAll sleeping agent view checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
