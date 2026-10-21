// Windows High Contrast (forced colors, #5707): the browser removes backgrounds and box-shadows, so a control drawn with only those loses its state.
// With forced colors emulated (Chrome's forced-colors media feature), this holds that the three that did are readable again:
//   - a Settings switch: on and off differ in track, knob colour and knob position;
//   - the selected tab (Settings, a dialog's section list, Team settings) differs from the others by more than weight;
//   - the agent list's status dot: working, needs input, stuck and idle each have their own shape or fill, and so do the sidebar rail's, a machine's health dot, a Hub server's and a live job's (#5713);
//   - a focused text field or list is outlined (its normal rule drops the outline and only changes the border colour).
// Without forced colors nothing of this applies (the normal look is unchanged). Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const TEAM = 'forced-' + Date.now().toString(36);
    const media = (forced, dark = true) => Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: dark ? 'dark' : 'light' }, ...(forced ? [{ name: 'forced-colors', value: 'active' }] : [])] });
    const start = async (forced, dark) => {
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false }); await media(forced, dark);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
    };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await start(true, true);
        assert.ok(await ev(`fetch('/api/board/${TEAM}/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session' }) }).then(r => r.status < 300)`), 'a team');
        const sw = id => ev(`(() => { const e = document.getElementById('${id}'); const cs = getComputedStyle(e), k = getComputedStyle(e, '::before'); return { track: cs.backgroundColor, border: cs.borderTopColor, knob: k.backgroundColor, x: k.transform, on: e.checked }; })()`);
        const tabs = sel => ev(`(() => { const all = [...document.querySelectorAll('${sel}')].filter(e => e.getClientRects().length); const s = all.find(e => e.getAttribute('aria-selected') === 'true'), o = all.find(e => e.getAttribute('aria-selected') !== 'true'); const c = e => { const cs = getComputedStyle(e); return [cs.borderLeftWidth, cs.borderTopWidth].join(' '); }; return { n: all.length, selected: s && c(s), other: o && c(o), page: getComputedStyle(document.body).backgroundColor }; })()`);

        for (const dark of [true, false]) {
            const mode = dark ? 'dark' : 'light';
            console.log(`1. ${mode}: a Settings switch, on and off, in forced colors`);
            await start(true, dark);
            await ev(`(async () => { await window.showSettingsModal('general'); return true; })()`);
            await waitFor(`!!document.getElementById('settings-group-by-team')`, 'the switch');
            await ev(`{ const e = document.getElementById('settings-group-by-team'); e.checked = false; } true`); await waitFor(`getComputedStyle(document.getElementById('settings-group-by-team'), '::before').transform === 'none'`, 'the knob to settle at the left'); const off = await sw('settings-group-by-team');
            await ev(`{ const e = document.getElementById('settings-group-by-team'); e.checked = true; } true`); await waitFor(`getComputedStyle(document.getElementById('settings-group-by-team'), '::before').transform !== 'none' && !getComputedStyle(document.getElementById('settings-group-by-team'), '::before').transform.endsWith(', 0, 0)')`, 'the knob to settle at the right'); const on = await sw('settings-group-by-team');
            assert.notEqual(on.track, off.track, 'the track differs on and off: ' + JSON.stringify({ on, off }));
            assert.notEqual(on.knob, off.knob, 'the knob colour differs: ' + JSON.stringify({ on, off }));
            assert.notEqual(on.x, off.x, 'the knob moves: ' + JSON.stringify({ on, off }));
            assert.notEqual(off.track, 'rgba(0, 0, 0, 0)', 'an off track has a colour of its own');

            console.log(`2. ${mode}: the selected tab has a border the others do not`);
            const st = await tabs('#settings-modal .settings-tab'); assert.ok(st.n >= 5 && st.selected && st.other, 'Settings tabs found: ' + JSON.stringify(st));  assert.notEqual(st.selected, st.other, 'the selected Settings tab differs from the others: ' + JSON.stringify(st));
            await ev(`window.hideSettingsModal(); import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode('${TEAM}', { workspace: false })); true`);
            await waitFor(`!!document.querySelector('.team-settings-tabs [role=tab]')`, 'the Team settings tabs', 15000);
            const tt = await tabs('.team-settings-tabs [role=tab]'); assert.ok(tt.n >= 3 && tt.selected && tt.other, 'Team settings tabs found: ' + JSON.stringify(tt)); assert.notEqual(tt.selected, tt.other, 'the selected Team settings tab differs: ' + JSON.stringify(tt));
            await ev(`document.getElementById('team-working-mode-dialog')?.close?.(); true`);

            console.log(`3. ${mode}: the status dots each have their own look`);
            const dots = await ev(`(() => { const host = document.createElement('div'); host.className = 'session-group-item'; host.style.cssText = 'position:fixed;left:0;top:0'; const names = ['working', 'waiting', 'stuck', 'stale']; host.innerHTML = names.map(n => '<span class="session-dot ' + n + '"></span>').join(''); document.body.append(host);
                const out = {}; for (const n of names) { const cs = getComputedStyle(host.querySelector('.' + n)); out[n] = [cs.backgroundColor, cs.borderTopWidth, cs.borderTopLeftRadius, cs.transform].join(' | '); } host.remove(); return out; })()`);
             assert.equal(new Set(Object.values(dots)).size, 4, 'working, needs input, stuck and idle are four different dots: ' + JSON.stringify(dots));
            const fill = n => dots[n].split(' | ')[0]; assert.notEqual(fill('working'), fill('stale'), 'a working dot is filled and an idle one is a ring (the page background): ' + JSON.stringify(dots)); assert.equal(fill('working'), fill('waiting')); assert.equal(fill('working'), fill('stuck'));

            console.log(`5. ${mode}: the other status dots each have their own look (#5713)`);
            const groups = { 'avatar-status-dot': ['working', 'waiting', 'stuck', 'stale'], 'machine-health-dot': ['machine-health-ok', 'machine-health-busy', 'machine-health-stalled', 'machine-health-unknown'], 'cs-dot': ['online', 'offline'] };
            for (const [base, names] of Object.entries(groups)) {
                const shapes = await ev(`(() => { const host = document.createElement('div'); host.style.cssText = 'position:fixed;left:0;top:0'; host.innerHTML = ${JSON.stringify(names)}.map(n => '<span class="${base} ' + n + '"></span>').join(''); document.body.append(host);
                    const out = {}; for (const n of ${JSON.stringify(names)}) { const cs = getComputedStyle(host.querySelector('.' + n)); out[n] = [cs.backgroundColor, cs.borderTopLeftRadius, cs.transform].join(' | '); } host.remove(); return out; })()`);
                assert.equal(new Set(Object.values(shapes)).size, names.length, base + ' states are different dots: ' + JSON.stringify(shapes));
            }
            const jobs = await ev(`(() => { const host = document.createElement('div'); host.innerHTML = '<span class="status-dot status-dot-working"></span><span class="status-dot status-dot-stale"></span>'; document.body.append(host); const [a, b] = host.children; const o = [getComputedStyle(a).backgroundColor, getComputedStyle(b).backgroundColor]; host.remove(); return o; })()`);
            assert.notEqual(jobs[0], jobs[1], 'a running job dot is filled and a pending one a ring: ' + JSON.stringify(jobs));

            console.log(`6. ${mode}: a focused text field has an outline`);
            const focus = await ev(`(() => { const i = document.createElement('input'); i.type = 'text'; i.className = 'sidebar-search-input'; document.body.append(i); i.focus(); const cs = getComputedStyle(i); const o = [cs.outlineStyle, cs.outlineWidth]; i.remove(); return o; })()`);
            assert.ok(focus[0] !== 'none' && parseFloat(focus[1]) >= 2, 'a focused search field is outlined: ' + JSON.stringify(focus));
        }
        console.log('4. without forced colors the normal look stays');
        await start(false, true);
        await ev(`(async () => { await window.showSettingsModal('general'); return true; })()`);
        await waitFor(`!!document.getElementById('settings-group-by-team')`, 'the switch');
        await ev(`document.getElementById('settings-group-by-team').checked = true; true`);
        const normal = await sw('settings-group-by-team');
        assert.notEqual(normal.track, 'rgb(0, 95, 204)', 'sanity'); assert.ok(!/^rgb\(0, 0, 255\)$/.test(normal.track), 'the track is the theme\'s accent, not a system colour: ' + normal.track);
        const dotN = await ev(`(() => { const host = document.createElement('div'); host.className = 'session-group-item'; host.innerHTML = '<span class="session-dot working"></span>'; document.body.append(host); const r = getComputedStyle(host.firstChild).borderTopWidth; host.remove(); return r; })()`);
        assert.equal(dotN, '0px', 'a dot has no border outside forced colors');
        console.log('   ✓');
    } finally { await Emulation.setEmulatedMedia({ features: [] }).catch(() => {}); await removeBoards(ev, [TEAM]); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
