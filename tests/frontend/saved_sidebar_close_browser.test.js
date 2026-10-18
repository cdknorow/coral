// The Saved for later sidebar closes (#4742, the operator's report: on the home view the top bar's bookmark opened a right-hand sidebar with only the Saved list and
// no way to close it). Real isolated server. On the home view and from an agent's page: the bookmark is a toggle (pressed while the sidebar shows Saved), Escape in the
// sidebar closes it with the focus back on the bookmark, the X in its header closes it, and the top bar's panel icon says whether it is open. From an agent's page the
// dock is left as it was: open on the tab it had, or shut. A phone's overlay opens and closes with the bookmark too.
//
// Expects CORAL_TEST_URL + CDP_PORT from the runner. Exit 0 / 1.
'use strict';
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

const results = [];
function check(label, cond, detail) {
    const v = cond ? 'PASS' : 'FAIL';
    results.push(v);
    console.log(`[${v}] ${label}${detail !== undefined ? ' — ' + detail : ''}`);
}
async function httpJson(method, p, body) {
    const r = await fetch(BASE + p, { method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
    let data = null; try { data = await r.json(); } catch { /* no body */ }
    return { status: r.status, data };
}

async function run() {
    const launched = [];
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime, Emulation, Input } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expression, what, timeout = 8000) => waitFor(expression, what, { soft: true, timeout });
    const open = () => ev(`(() => { const d = document.getElementById('agentic-state'); return window.innerWidth <= 767 ? d.classList.contains('mobile-panel-overlay') : !d.classList.contains('collapsed'); })()`);
    const state = () => ev(`(() => ({ saved: document.getElementById('agentic-tab-saved').classList.contains('active'), tab: (document.querySelector('#agentic-block-top .agentic-tab.active') || {}).dataset.tab, pressed: document.getElementById('saved-btn').getAttribute('aria-pressed'), panelBtn: document.getElementById('panel-toggle-btn').getAttribute('aria-pressed'), focus: document.activeElement && (document.activeElement.id || document.activeElement.tagName) }))()`);
    const clickSaved = () => ev(`document.getElementById('saved-btn').click(); true`);
    const esc = () => Input.dispatchKeyEvent({ type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 }).then(() => Input.dispatchKeyEvent({ type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 }));
    const closed = async what => check(what, await until(`(() => { const d = document.getElementById('agentic-state'); return window.innerWidth <= 767 ? !d.classList.contains('mobile-panel-overlay') : d.classList.contains('collapsed'); })()`, what));
    const opened = async what => check(what, await until(`(() => { const d = document.getElementById('agentic-state'); const o = window.innerWidth <= 767 ? d.classList.contains('mobile-panel-overlay') : !d.classList.contains('collapsed'); return o && document.getElementById('agentic-tab-saved').classList.contains('active'); })()`, what));
    try {
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' });
        await coralReady();
        await ev(`localStorage.removeItem('coral-agentic-collapsed'); true`);

        console.log('Home view');
        check('the Saved bookmark starts not pressed and the panel icon says closed', (await state()).pressed === 'false' && (await state()).panelBtn !== 'true', JSON.stringify(await state()));
        await clickSaved(); await opened('the bookmark opens the sidebar on Saved');
        let s = await state(); check('the bookmark is pressed and so is the panel icon', s.pressed === 'true' && s.panelBtn === 'true', JSON.stringify(s));
        await clickSaved(); await closed('the bookmark again closes it');
        s = await state(); check('not pressed again, and the focus is on the bookmark', s.pressed === 'false' && s.panelBtn === 'false' && s.focus === 'saved-btn', JSON.stringify(s));
        await clickSaved(); await opened('it opens again');
        check('the X is visible in the sidebar header', await ev(`(() => { const b = document.getElementById('saved-close'); const r = b.getBoundingClientRect(); return getComputedStyle(b).display !== 'none' && r.width >= 24 && r.height >= 24; })()`));
        await ev(`document.getElementById('saved-close').click(); true`); await closed('the X closes it');
        check('and the focus is on the bookmark', (await state()).focus === 'saved-btn', JSON.stringify(await state()));
        await clickSaved(); await opened('opened again');
        await esc(); await closed('Escape closes it (the focus is inside the sidebar after opening)');
        check('with the focus on the bookmark', (await state()).focus === 'saved-btn', JSON.stringify(await state()));
        await clickSaved(); await opened('opened once more');
        await ev(`document.getElementById('panel-toggle-btn').click(); true`); await closed('the panel icon closes it too');
        check('and the bookmark is not pressed', (await state()).pressed === 'false', JSON.stringify(await state()));

        console.log('Escape is left alone where something else has it');
        await clickSaved(); await opened('opened');
        await ev(`const i = document.createElement('input'); i.id = 'qa-field'; document.body.appendChild(i); i.focus(); true`);
        await esc(); await new Promise(r => setTimeout(r, 300)); // sleep-ok: proves a negative, a close would come at once
        check('Escape in a text field does not close the sidebar', await open());
        await ev(`document.getElementById('qa-field').remove(); document.getElementById('saved-close').click(); true`); await closed('closed');

        console.log('From an agent\'s page');
        const r = await httpJson('POST', '/api/sessions/launch', { working_dir: '/tmp', agent_type: 'terminal', display_name: 'qa-saved-dock' });
        if (!r.data || !r.data.session_id) throw new Error(`launch: ${r.status} ${JSON.stringify(r.data)}`);
        launched.push(r.data);
        await until(`(window._coralGetLiveSessions ? window._coralGetLiveSessions() : []).some(x => x.session_id === ${JSON.stringify(r.data.session_id)})`, 'the agent listed', 15000);
        await ev(`window.selectLiveSession(${JSON.stringify(r.data.session_name)}, 'terminal', ${JSON.stringify(r.data.session_id)}, ''); true`);
        await until(`document.getElementById('live-session-view') && getComputedStyle(document.getElementById('live-session-view')).display !== 'none'`, 'the agent view');
        // The agent's dock open on Files: Saved shows, and closing gives Files back with the dock still open.
        await ev(`window.toggleAgenticPanel(true); window.switchAgenticTab('files', 'top'); true`);
        await until(`(document.querySelector('#agentic-block-top .agentic-tab.active') || {}).dataset && document.querySelector('#agentic-block-top .agentic-tab.active').dataset.tab === 'files'`, 'Files active');
        await clickSaved(); await opened('the bookmark shows Saved in the agent\'s open dock');
        await clickSaved();
        check('the bookmark again gives the agent\'s tab back and leaves the dock open', await until(`(() => { const d = document.getElementById('agentic-state'); return !d.classList.contains('collapsed') && document.querySelector('#agentic-block-top .agentic-tab.active').dataset.tab === 'files'; })()`, 'Files back'), JSON.stringify(await state()));
        check('the bookmark is not pressed', (await state()).pressed === 'false');
        // The dock shut: Saved opens it, the X shuts it again.
        await ev(`window.toggleAgenticPanel(false); true`); await closed('the agent\'s dock shut');
        await clickSaved(); await opened('the bookmark opens it on Saved');
        await ev(`document.getElementById('saved-close').click(); true`); await closed('the X shuts it again (it was shut before)');
        await clickSaved(); await opened('opened'); await esc(); await closed('Escape shuts it again');

        console.log('A phone');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 800, deviceScaleFactor: 2, mobile: true });
        await Page.navigate({ url: BASE + '/' }); await coralReady();
        await clickSaved(); await opened('the bookmark opens the overlay');
        check('the phone has its own close button and not the desktop X', await ev(`getComputedStyle(document.getElementById('agentic-mobile-close-btn')).display !== 'none' && getComputedStyle(document.getElementById('saved-close')).display === 'none'`));
        await clickSaved(); await closed('the bookmark again closes the overlay');
    } finally {
        for (const s of launched) await httpJson('POST', '/api/sessions/live/' + encodeURIComponent(s.session_name) + '/kill', { session_id: s.session_id, agent_type: 'terminal' }).catch(() => {});
        await client.close();
    }
    const failed = results.filter(r => r === 'FAIL').length;
    console.log(failed ? `\n${failed} FAILED` : '\nALL PASS');
    process.exit(failed ? 1 : 0);
}
run().catch(e => { console.error(e); process.exit(1); });
