// The agent list's attention states are quiet (#5123; operator: the "Needs input · Permission" row was "a bit too loud", an amber wash across the row and an amber pill with an
// amber ring). No amber (or red) wash across a row that needs the Operator; the state pill has the neutral pill's surface and hairline with only its text warm, and no expanding
// ring (the row's flashing dot is the motion, and stops with reduced motion); on a phone the card has no amber border and the banner and chip are quiet the same way; the text of
// every pill is at least 4.5:1 in both themes; the words, not the colour, say which state it is. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const base = o => Object.assign({ name: 'coral-go', display_name: '', agent_type: 'claude', summary: 'Goal text', context_pct: 10, working: false, waiting_for_input: false, not_started: false, stuck: false, sleeping: false, awaiting_user: false, done: false, board_unread: 0, working_directory: '/repo/coral', type: 'live', status: 'idle' }, o);
    const ROWS = [
        base({ session_id: 'r1', display_name: 'Worker', working: true }),
        base({ session_id: 'r2', display_name: 'Asker permission', waiting_for_input: true, waiting_kind: 'permission', context_pct: 87, context_window: 1000000 }),
        base({ session_id: 'r3', display_name: 'Asker question', waiting_for_input: true, waiting_kind: 'question' }),
        base({ session_id: 'r4', display_name: 'Fresh', not_started: true }),
        base({ session_id: 'r5', display_name: 'Jammed', stuck: true }),
        base({ session_id: 'r6', display_name: 'Idler' }),
    ];
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const v of [
            { name: 'desktop, dark', w: 1280, h: 700, mobile: false, theme: 'dark' }, { name: 'desktop, light', w: 1280, h: 700, mobile: false, theme: 'light' },
            { name: 'phone 390, dark', w: 390, h: 844, mobile: true, theme: 'dark' }, { name: 'phone 390, light', w: 390, h: 844, mobile: true, theme: 'light' },
        ]) {
            console.log(`${v.name}: no row wash, quiet pills, no ring, the dot still flashes, 4.5:1`);
            await Emulation.setDeviceMetricsOverride({ width: v.w, height: v.h, deviceScaleFactor: 1, mobile: v.mobile });
            await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-reduced-motion', value: 'no-preference' }] });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await installAudit(ev); await setTheme(ev, v.theme);
            await ev(`(async () => { T.state.liveSessions = ${JSON.stringify(ROWS)}; T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
            const root = v.mobile ? '#mobile-session-list' : '#live-sessions-list';
            await ev(`document.getElementById('__notrans')?.remove(); true`); // setTheme switches animations off for the audit: the dot's own flash is checked with them on
            await waitFor(`document.querySelectorAll('${root} .session-group-item').length >= 6`, `${v.name}: the rows`, 5000);
            const s = await ev(`(() => {
                const root = document.querySelector('${root}');
                const probe = c => { const i = document.createElement('i'); i.style.color = c; document.body.appendChild(i); const v = getComputedStyle(i).color; i.remove(); return v; };
                const li = id => root.querySelector('[data-session-id="' + id + '"]');
                const cs = (e, p) => getComputedStyle(e, p);
                const row = id => { const e = li(id); return e ? { cls: e.className, bg: cs(e).backgroundColor, border: cs(e).borderTopColor, shadow: cs(e).boxShadow } : null; };
                const pill = id => { const e = li(id) && li(id).querySelector('.session-state-pill'); return e ? { text: e.textContent.trim(), weight: cs(e).fontWeight, bg: cs(e).backgroundColor, shadow: cs(e).boxShadow, ring: cs(e, '::after').content, shown: cs(e).display !== 'none' } : null; };
                const chip = id => { const e = li(id) && li(id).querySelector('.session-status-chip'); return e ? { text: e.textContent.trim(), bg: cs(e).backgroundColor } : null; };
                const banner = id => { const e = li(id) && li(id).querySelector('.session-mobile-banner'); return e ? { bg: cs(e).backgroundColor, border: cs(e).borderTopColor } : null; };
                const dot = id => { const e = li(id).querySelector('.session-dot'); return { name: cs(e, '::after').animationName }; };
                return { tertiary: probe(getComputedStyle(document.documentElement).getPropertyValue('--bg-tertiary').trim()), borderVar: probe(getComputedStyle(document.documentElement).getPropertyValue('--border').trim()),
                    rows: Object.fromEntries(['r1', 'r2', 'r3', 'r4', 'r5', 'r6'].map(id => [id, row(id)])), pills: Object.fromEntries(['r2', 'r3', 'r4', 'r5'].map(id => [id, pill(id)])),
                    chips: Object.fromEntries(['r2', 'r5'].map(id => [id, chip(id)])), banners: Object.fromEntries(['r2'].map(id => [id, banner(id)])), dots: { r2: dot('r2'), r5: dot('r5') } };
            })()`);
            const transparent = 'rgba(0, 0, 0, 0)';
            for (const id of ['r2', 'r3', 'r4', 'r5']) {
                assert.ok(s.rows[id].cls.includes('needs-attention'), `${v.name}: ${id} is an attention row (${s.rows[id].cls})`);
                if (!v.mobile) assert.equal(s.rows[id].bg, transparent, `${v.name}: the ${id} row has no wash (${s.rows[id].bg})`);
                else assert.equal(s.rows[id].border, s.rows['r6'].border, `${v.name}: the ${id} card has the border of a quiet card, no amber one (${s.rows[id].border} against ${s.rows['r6'].border})`);
            }
            if (!v.mobile) {
                for (const id of ['r2', 'r3', 'r4', 'r5']) {
                    const p = s.pills[id];
                    assert.ok(p && p.shown, `${v.name}: ${id} has its state pill`);
                    assert.equal(p.bg, s.tertiary, `${v.name}: the ${id} pill has the neutral surface (${p.bg})`);
                    assert.equal(p.weight, '500', `${v.name}: and the neutral weight`);
                    assert.ok(p.shadow.includes(s.borderVar), `${v.name}: and only the neutral hairline, no amber or red ring (${p.shadow})`);
                    assert.equal(p.ring, 'none', `${v.name}: no expanding ring around the pill (${p.ring})`);
                }
                assert.deepEqual(['r2', 'r3', 'r4', 'r5'].map(id => s.pills[id].text), ['Needs input · Permission', 'Needs input · Question', 'Check terminal', 'Stuck'], 'the words say which state it is');
            } else {
                assert.equal(s.chips.r2.bg, s.tertiary, `${v.name}: the Needs input chip is on the neutral surface (${s.chips.r2.bg})`);
                assert.equal(s.chips.r5.bg, s.tertiary, `${v.name}: the error chip too`);
                assert.equal(s.banners.r2.bg, s.tertiary, `${v.name}: the banner is on the neutral surface (${s.banners.r2.bg})`);
                assert.equal(s.banners.r2.border, s.borderVar, `${v.name}: with the neutral hairline (${s.banners.r2.border})`);
            }
            // The row's flashing dot stays: it animates with no preference, and stops with reduced motion.
            assert.notEqual(s.dots.r2.name, 'none', `${v.name}: the waiting dot flashes (${s.dots.r2.name})`);
            assert.notEqual(s.dots.r5.name, 'none', `${v.name}: the stuck dot flashes (${s.dots.r5.name})`);
            await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-reduced-motion', value: 'reduce' }] });
            const still = await ev(`(() => { const e = document.querySelector('${root} [data-session-id="r2"] .session-dot'); const c = getComputedStyle(e, '::after'); return c.animationName === 'none' || c.opacity === '0'; })()`);
            assert.equal(still, true, `${v.name}: the dot stops with reduced motion`);
            // Every pill's text is readable: 4.5:1 in this theme.
            const sel = v.mobile ? ['.session-status-chip', '.session-mobile-banner'] : ['.session-state-pill'];
            const audit = await auditRegion(ev, sel.map(x => root + ' ' + x));
            assert.deepEqual(audit.fails, [], `${v.name}: contrast of the state pills: ` + JSON.stringify(audit.fails));
            assert.ok(audit.checked >= 3, `${v.name}: the audit looked at the pills (${audit.checked})`);
        }
        console.log('attention_rows_quiet: ok');
        await client.close();
        process.exit(0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        await client.close().catch(() => {});
        process.exit(1);
    }
})();
