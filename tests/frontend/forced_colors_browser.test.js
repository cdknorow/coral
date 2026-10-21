// Windows High Contrast (forced colors, #5707): the browser removes backgrounds and box-shadows, so a control drawn with only those loses its state.
// With forced colors emulated (Chrome's forced-colors media feature), this holds that the three that did are readable again:
//   - a Settings switch: on and off differ in track, knob colour and knob position;
//   - the selected tab (Settings, a dialog's section list, Team settings) differs from the others by more than weight;
//   - the agent list's status dot: working, needs input, stuck and idle each have their own shape or fill, and so do the sidebar rail's, a machine's health dot, a Hub server's and a live job's (#5713);
//   - the Launch dialog's section list marks the selected section; count and status chips keep a border; a pressed toggle has a Highlight one, as wide pressed as not (#5735);
//   - a focused text field or list is outlined (its normal rule drops the outline and only changes the border colour).
// Without forced colors nothing of this applies (the normal look is unchanged). Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const { stubFetch } = require('./helpers/comments_stub');
const BASE = require('./test_base').BASE;
const HEX = 'd'.repeat(64);
const MD = '# Migration plan\n\n## Steps\n\n1. Add the new columns behind a flag and backfill in batches of 500 rows.\n2. Switch reads to the new columns.\n\n## Risks\n\nThe backfill takes a write lock on large databases, so it must run off-peak.\n';

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

            console.log(`7. ${mode}: the Launch dialog's section list marks the selected section, and a field in it is outlined when focused (#5735)`);
            await ev(`(async () => { await window.showLaunchModal(); window._selectLaunchType('agent'); document.getElementById('launch-agent-more-btn')?.click(); return true; })()`);
            await waitFor(`document.querySelectorAll('[id^="sd1-tab-"]').length >= 4`, 'the Launch section list', 15000);
            const lt = await tabs('.settings-tab[id^="sd1-tab-"]'); assert.ok(lt.n >= 4 && lt.selected && lt.other, 'Launch tabs found: ' + JSON.stringify(lt)); assert.notEqual(lt.selected, lt.other, 'the selected Launch section has a border the others do not: ' + JSON.stringify(lt));
            const lf = await ev(`(() => { const i = document.getElementById('launch-dir'); i.focus(); const cs = getComputedStyle(i); return [cs.outlineStyle, cs.outlineWidth]; })()`);
            assert.ok(lf[0] !== 'none' && parseFloat(lf[1]) >= 2, 'a focused field in the Launch dialog is outlined (its rule drew a box-shadow): ' + JSON.stringify(lf));
            await ev(`window.hideLaunchModal(); true`);

            console.log(`8. ${mode}: count and status chips keep a shape, and a pressed toggle is marked (#5735)`);
            const chips = await ev(`(() => { const host = document.createElement('div'); host.style.cssText = 'position:fixed;left:0;top:0'; const names = ['nav-tab-badge', 'agentic-tab-count', 'live-job-badge live-job-badge-cron', 'badge session-state-pill', 'wf-status-badge failed', 'section-count-badge', 'terminal-state-pill'];
                host.innerHTML = names.map(n => '<span class="' + n + '">3</span>').join('') + '<button class="perm-chip" aria-pressed="true">on</button><button class="perm-chip" aria-pressed="false">off</button>'; document.body.append(host);
                const out = { chips: {}, on: null, off: null }; names.forEach((n, i) => { out.chips[n] = getComputedStyle(host.children[i]).borderTopWidth; });
                out.on = getComputedStyle(host.children[names.length]).borderTopWidth + ' ' + getComputedStyle(host.children[names.length]).borderTopColor; out.off = getComputedStyle(host.children[names.length + 1]).borderTopWidth + ' ' + getComputedStyle(host.children[names.length + 1]).borderTopColor; host.remove(); return out; })()`);
            for (const [n, w] of Object.entries(chips.chips)) assert.ok(parseFloat(w) >= 1, n + ' has a border in forced colors: ' + JSON.stringify(chips));
            assert.ok(parseFloat(chips.on) >= 2 && chips.on !== chips.off, 'a pressed toggle is marked by its border colour: ' + JSON.stringify(chips));
            assert.equal(chips.on.split(' ')[0], chips.off.split(' ')[0], 'a toggle keeps its size when pressed: the border is as wide unpressed as pressed: ' + JSON.stringify(chips));

            console.log(`9. ${mode}: the selected Inbox tab is marked and none moves (#5770)`);
            await ev(`localStorage.removeItem('coral.inbox.tab'); document.getElementById('nav-tab-hub-inbox').click(); true`);
            await waitFor(`!!document.getElementById('inbox-local') && !document.getElementById('inbox-local').hidden`, 'the Inbox', 8000);
            const itabs = await ev(`(() => [...document.querySelectorAll('.inbox-tab')].map(b => { const c = getComputedStyle(b); return { sel: b.getAttribute('aria-selected') === 'true', w: c.borderBottomWidth, col: c.borderBottomColor }; }))()`);
            const isel = itabs.find(x => x.sel), iothers = itabs.filter(x => !x.sel);
            assert.ok(isel && iothers.length === 2, 'three Inbox tabs: ' + JSON.stringify(itabs));
            assert.ok(iothers.every(o => o.col !== isel.col), "the selected tab's underline is a colour of its own: " + JSON.stringify(itabs));
            assert.ok(iothers.every(o => o.w === isel.w) && parseFloat(isel.w) >= 3, 'all the underlines are as wide, so none moves: ' + JSON.stringify(itabs));
        }
        console.log('7. a comment card that is current or hovered, and an avatar, are marked in forced colors (#5732)');
        for (const dark of [true, false]) {
            const mode = dark ? 'dark' : 'light';
            await start(true, dark);
            await ev(stubFetch(HEX, MD));
            await ev(`(async () => { const v = await import((window.__coralStatic || '/static/') + 'artifact_viewer.js'); v.openArtifactViewer(${JSON.stringify(HEX)}, { filename: 'plan.md' }); return true; })()`);
            await waitFor(`document.querySelectorAll('.ac-card').length >= 3`, 'the comment cards', 10000);
            await ev(`document.querySelector('.ac-card[data-id="3"]').click(); true`);
            await waitFor(`!!document.querySelector('.ac-card[data-id="3"].ac-active')`, 'the current card', 5000);
            await ev(`document.querySelector('.ac-card[data-id="1"]').dispatchEvent(new PointerEvent('pointerenter')); true`);
            const look = await ev(`(() => { const st = e => { const c = getComputedStyle(e); return { border: c.borderTopColor, outline: c.outlineStyle + ' ' + c.outlineWidth }; };
                return { rest: st(document.querySelector('.ac-card[data-id="4"]')), active: st(document.querySelector('.ac-card[data-id="3"]')), hover: st(document.querySelector('.ac-card[data-id="1"]')), avatar: getComputedStyle(document.querySelector('.ac-avatar')).borderTopWidth }; })()`);
            assert.notEqual(look.active.border, look.rest.border, mode + ': the current card has a border of its own: ' + JSON.stringify(look));
            assert.ok(look.active.outline.startsWith('solid') && parseFloat(look.active.outline.split(' ')[1]) >= 2, mode + ': the current card has an outline: ' + JSON.stringify(look));
            assert.notEqual(look.hover.border, look.rest.border, mode + ': the hovered card has a border of its own: ' + JSON.stringify(look));
            assert.equal(look.rest.outline.split(' ')[0], 'none', mode + ': a card at rest has no outline');
            assert.ok(parseFloat(look.avatar) >= 1, mode + ': an avatar has a ring: ' + look.avatar);
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
