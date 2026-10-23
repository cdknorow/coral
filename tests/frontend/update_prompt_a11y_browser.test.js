// The update prompt for a person using a screen reader or only the keyboard (#6315, #6292 and #6306): the "ready" notice is said once and its buttons say what they do; Later leaves focus on the update button
// in the top bar (not on the page); Review opens a named alert dialog with focus on the safe answer (Later) when agents would restart, the agent list reads as words ("release-agent working, starts again
// afterwards") and the consent line ("Nothing is installed until you choose Install") is part of what the dialog is described by; Escape closes it and focus returns to the update button; the
// switch that lets Coral Hub relaunch this Coral is described by the sentence under it. Desktop and a 390 px phone. Drives Chrome over CDP through agent_view_harness.js with the census data world.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { DATA_WORLD, UPDATE_STAGED, CLOUD_LINKED } = require('./helpers/census_world');
const { showUpdateReady, UPDATE_DIALOG_UP } = require('./helpers/phone_targets');
const { watch, clear, log, said } = require('./helpers/announce_census');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const key = async (k, code, vk) => { await Input.dispatchKeyEvent({ type: 'keyDown', key: k, code, windowsVirtualKeyCode: vk }); await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code, windowsVirtualKeyCode: vk }); };
    const active = () => ev(`(() => { const a = document.activeElement; return a ? { id: a.id, cls: typeof a.className === 'string' ? a.className : '', text: (a.textContent || '').trim().slice(0, 30) } : null; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Page.addScriptToEvaluateOnNewDocument({ source: DATA_WORLD });
        let version = 160;
        for (const [w, h, mobile, tag] of [[1280, 800, false, 'desktop'], [390, 844, true, 'phone']]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`localStorage.clear(); true`);
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
            await ev(`window.switchNavTab('agents'); true`);
            const status = () => ({ ...UPDATE_STAGED, staged: { ...UPDATE_STAGED.staged, version: `1.4.${++version}` } }); // the notice is said once per version

            // the notice
            await watch(ev); await clear(ev);
            await showUpdateReady(ev, waitFor, status());
            await new Promise(r => setTimeout(r, 1200)); // sleep-ok: an announcement is timed; there is no event to wait for
            const said1 = said(await log(ev), /update/i);
            assert.equal(said1.length, 1, `${tag}: the notice is said once (${JSON.stringify(said1.map(e => e.text))})`);
            const n = await ev(`(() => { const b = document.getElementById('update-ready-notice'); return { role: b.getAttribute('role'), buttons: [...b.querySelectorAll('button')].map(x => (x.getAttribute('aria-label') || x.textContent).trim()), text: b.textContent.replace(/\\s+/g, ' ').trim() }; })()`);
            assert.equal(n.role, 'status', `${tag}: the notice is a status`);
            assert.ok(n.buttons.every(b => b.length > 0) && /Review/.test(n.buttons[0]), `${tag}: its buttons are named (${JSON.stringify(n.buttons)})`);
            assert.ok(/Nothing is installed/.test(n.text), `${tag}: the notice says nothing is installed yet (${n.text})`);

            // Later leaves focus on the update button, not on the page
            await ev(`document.querySelector('.update-ready-later').focus(); document.querySelector('.update-ready-later').click(); true`);
            assert.equal((await active()).id, 'update-pill', `${tag}: after Later, focus is on the update button in the top bar (${JSON.stringify(await active())})`);

            // Review: the dialog
            await showUpdateReady(ev, waitFor, status());
            await ev(`document.querySelector('.update-ready-review').focus(); document.querySelector('.update-ready-review').click(); true`);
            await waitFor(UPDATE_DIALOG_UP, `${tag}: the confirmation`, 5000);
            await new Promise(r => setTimeout(r, 300)); // sleep-ok: focus settles as the dialog opens
            const d = await ev(`(() => {
                const dlg = document.getElementById('update-confirm-modal'), id = x => document.getElementById(x);
                const names = x => x.split(' ').map(i => (id(i) || {}).textContent || '').join(' ').replace(/\\s+/g, ' ').trim();
                const a = document.activeElement;
                return { role: dlg.getAttribute('role'), modal: dlg.getAttribute('aria-modal'), title: names(dlg.getAttribute('aria-labelledby') || ''), desc: names(dlg.getAttribute('aria-describedby') || ''),
                    focus: a ? (a.className || '').toString() : null, inside: dlg.contains(a),
                    list: (() => { const u = dlg.querySelector('ul.update-agents'); return u ? { name: u.getAttribute('aria-label'), items: [...u.children].map(li => li.textContent.replace(/\\s+/g, ' ').trim()), plain: [...u.children].map(li => li.textContent.trim()), shown: [...u.children].map(li => li.innerText.replace(/\\s+/g, ' ').trim()), spoken: [...u.children].map(li => [...li.children].map(c => c.textContent.trim()).join(' ')) } : null; })(),
                    buttons: [...dlg.querySelectorAll('button')].map(b => b.textContent.trim()) };
            })()`);
            assert.equal(d.role, 'alertdialog', `${tag}: the confirmation is an alert dialog`);
            assert.equal(d.modal, 'true', `${tag}: and modal`);
            assert.match(d.title, /^Install Coral 1\.4\.\d+ and relaunch\?$/, `${tag}: it is named by its question (${d.title})`);
            assert.match(d.desc, /Nothing is installed until you choose Install/, `${tag}: it is described by its body, which has the consent line (${d.desc})`);
            assert.ok(d.inside && /update-later/.test(d.focus), `${tag}: focus is on Later, the safe answer, because agents would restart (${d.focus})`);
            assert.equal(d.list && d.list.name, 'Agents that restart', `${tag}: the agent list is named`);
            assert.deepEqual(d.list.spoken, ['release-agent working starts again afterwards', 'docs-agent waiting for you starts a new conversation'], `${tag}: each agent reads as words with spaces between its parts (${JSON.stringify(d.list.items)})`);
            assert.deepEqual(d.list.plain.filter(t => /[a-z]working|[a-z]waiting|you(starts|a new)|afterwards[a-z]/.test(t)), [], `${tag}: the words of an agent's row are not run together in the page's text (${JSON.stringify(d.list.plain)}; shown as ${JSON.stringify(d.list.shown)})`);
            assert.ok(d.buttons.includes('Later') && d.buttons.some(b => /^Install and restart 2 agents$/.test(b)), `${tag}: the buttons say what they do (${JSON.stringify(d.buttons)})`);

            // Escape closes it and focus returns to the update button
            await key('Escape', 'Escape', 27);
            await new Promise(r => setTimeout(r, 400)); // sleep-ok: the close transition
            assert.ok(!(await ev(UPDATE_DIALOG_UP)), `${tag}: Escape closes the confirmation`);
            assert.equal((await active()).id, 'update-pill', `${tag}: focus returns to the update button (${JSON.stringify(await active())})`);

            // the switch for Coral Hub's relaunch
            await ev(`window.__cloudStatus = ${JSON.stringify(CLOUD_LINKED)}; (async () => { await window.showSettingsModal('network'); return true; })()`);
            await waitFor(`!!document.getElementById('settings-cloud-hub-relaunch-state') && document.getElementById('settings-cloud-hub-relaunch-state').getClientRects().length > 0`, `${tag}: the relaunch sentence`, 10000);
            const sw = await ev(`(() => { const b = document.getElementById('settings-cloud-hub-relaunch'); const names = x => (x || '').split(' ').map(i => (document.getElementById(i) || {}).textContent || '').join(' ').replace(/\\s+/g, ' ').trim(); return { checked: b.checked, label: [...b.labels].map(l => l.textContent.replace(/\\s+/g, ' ').trim()).join(' ') || b.getAttribute('aria-label'), desc: names(b.getAttribute('aria-describedby')) }; })()`);
            assert.ok(sw.label && /relaunch/i.test(sw.label), `${tag}: the switch has its name (${sw.label})`);
            assert.match(sw.desc, sw.checked ? /^.*On: / : /Off: /, `${tag}: it is described by the sentence for its position (${sw.desc})`);
            await ev(`window.hideSettingsModal && window.hideSettingsModal(); window.__cloudStatus = null; true`);
            console.log(`   ✓ ${tag}`);
        }
        console.log('PASS the update prompt: the notice said once, Later and Escape return focus, the dialog is named and described, its agent list reads as words, the Hub switch is described');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
