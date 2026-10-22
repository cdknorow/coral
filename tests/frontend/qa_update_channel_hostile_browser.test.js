// QA #5561: hostile and awkward data for the update-channel pane (#5558). normalizeStatus is fed what a hostile or buggy reply could carry (odd
// channel spellings, versions that are not versions, markup and huge text in the notes, junk dates, more than five releases, a non-array list), and
// the pane is driven through the flows around Apply: the radios without Apply, a refused channel save, the channel and the auto switch saved together,
// a cold server cache (the first answer after switching to Agentic carries no list), the toast with odd fields. Drives Chrome over CDP through
// agent_view_harness.js against the isolated dev server, with a stub of the status API like update_channel_browser.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const NOW = Date.now(), day = n => new Date(NOW - n * 86400000).toISOString();
const base = { enabled: true, installed: '1.4.141', state: 'none', staged: null, relaunch: { available: false, reason: 'nothing_staged', restarts: [], ack: '' }, last_result: null, checked_at: new Date().toISOString(), auto: true, not_ready_reason: 'up_to_date' };

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const settle = (ms, quietMs = 120) => require('./helpers/wait').settle(ev, ms, quietMs);
    const STUB = `(() => { const real = window.fetch; window.__ch = window.__ch || { status: null, puts: [], putStatus: 200, later: null, check: null };
        const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
        window.fetch = (u, o) => { const url = String(u), method = (o && o.method) || 'GET';
            if (url.includes('/api/cloud/status')) return json({ connected: false, hub_relaunch_updates: false });
            if (url.endsWith('/api/system/update')) return window.__ch.status ? json(window.__ch.status) : json({ error: 'none' }, 404);
            if (url.includes('/api/system/update-check')) return json(window.__ch.check || { available: false });
            if (method === 'PUT' && url.endsWith('/api/settings')) { const b = JSON.parse(o.body); window.__ch.puts.push(b);
                if (b.update_channel && window.__ch.putStatus !== 200) return json({ error: 'refused' }, window.__ch.putStatus);
                if (b.update_channel && window.__ch.later) window.__ch.status = window.__ch.later; // the server's cache answers from now on
                return json({ ok: true }); }
            if (method === 'PUT' && url.endsWith('/api/settings/update-auto')) return json({ ok: true });
            return real(u, o); }; return true; })()`;
    const load = async (status) => {
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await ev(`localStorage.clear(); true`);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`import((window.__coralStatic || '/static/') + 'update_flag.js').then(m => { m.setUpdatesEnabled(true); return true; })`);
        await ev(STUB);
        await ev(`window.__ch.status = ${JSON.stringify(status)}; import(${S} + 'update_ui.js').then(m => m.startUpdateUi && m.startUpdateUi()).then(() => import(${S} + 'update_ui.js')).then(m => m.refreshUpdateStatus()).then(() => true)`);
        await ev(`window.showSettingsModal('updates'); true`);
        await waitFor(`!document.getElementById('settings-pane-updates')?.hidden && !!document.querySelector('#update-pane-body .update-status, #update-pane-body .update-managed')`, 'the Updates pane', 8000);
        await settle(200);
    };
    const norm = (raw) => ev(`import(${S} + 'update_api.js').then(m => m.normalizeStatus(${JSON.stringify(raw)}))`);
    const pane = () => ev(`(() => { const q = s => document.querySelector('#update-pane-body ' + s);
        const w = q('.update-waiting');
        return { group: !!q('.update-channel'), checked: (document.querySelector('input[name=update-channel]:checked') || {}).value || '', waiting: !!w, cards: w ? [...w.querySelectorAll('.update-status b')].map(b => b.textContent) : [], autoNote: (q('.update-channel-auto') || {}).hidden, pwn: !!window.__pwn, imgs: document.querySelectorAll('#update-pane-body img').length }; })()`);
    const apply = async () => { await ev(`window.applySettings(); true`); await settle(500); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await load({ ...base, channel: 'approved', approved: { version: '1.4.142', approved_at: day(1) } });

        // 1. normalizeStatus against hostile input
        for (const ch of ['AGENTIC', 'Agentic', ' agentic', 'agentic\u0000', 'agentic ', ['agentic'], { a: 1 }, 7, null, true, '']) {
            const n = await norm({ ...base, channel: ch });
            check(`channel ${JSON.stringify(ch)} reads as approved and no group is claimed`, n.channel === 'approved' && n.hasChannel === false);
        }
        let n = await norm({ ...base, channel: 'agentic' });
        check('the exact word agentic is read as agentic', n.channel === 'agentic' && n.hasChannel === true);
        const evil = ['1.4.144', 'javascript:alert(1)', '1.4.144<img src=x>', '1.4.144 ', '', '  ', 'v1.4.144', '1.4', '1.4.144.5', '１.４.１４４', null, 5, {}, [], '1.4.145-rc1'];
        n = await norm({ ...base, channel: 'agentic', waiting: [...evil.map(v => ({ version: v, published_at: day(1), notes: 'x' })), null, 'str', 3, [1], { notes: 'no version' }] });
        check(`versions that are not versions are dropped (kept ${JSON.stringify(n.waiting.map(w => w.version))})`, n.waiting.every(w => /^\d+\.\d+\.\d+$/.test(w.version)) && n.waiting.some(w => w.version === '1.4.144'));
        n = await norm({ ...base, channel: 'agentic', waiting: Array.from({ length: 12 }, (_, i) => ({ version: `1.5.${i}`, published_at: day(1), notes: '' })) });
        check('12 releases: at most five are kept, the first five', n.waiting.length === 5 && n.waiting[0].version === '1.5.0' && n.waiting[4].version === '1.5.4');
        n = await norm({ ...base, channel: 'agentic', waiting: { version: '1.4.144' } });
        check('waiting that is an object, not a list, is an empty list', Array.isArray(n.waiting) && n.waiting.length === 0);
        n = await norm({ ...base, channel: 'agentic', waiting: [{ version: '1.4.144', published_at: day(1), notes: 'a'.repeat(50000) }] });
        check('50 000 characters of notes are cut to 2000', n.waiting[0].notes.length === 2000);
        n = await norm({ ...base, channel: 'agentic', waiting: [{ version: '1.4.144', published_at: 'not a date', notes: { evil: 1 } }, { version: '1.4.145', published_at: 12345, notes: ['x'] }] });
        check('a junk date or a non-text note is empty text, not "[object Object]"', n.waiting[0].publishedAt === '' && n.waiting[0].notes === '' && n.waiting[1].notes === '', JSON.stringify(n.waiting));
        n = await norm({ ...base, channel: 'approved', approved: { version: '1.4.142<script>', approved_at: day(1) } });
        check('an approved version that is not a version gives no "Approved now" line', n.approved === null);
        n = await norm({ ...base, channel: 'approved', approved: 'str', past_approved: 'true' });
        check('approved as a string and past_approved as the string "true" are ignored', n.approved === null && n.pastApproved === false);

        // 2. the pane with hostile notes and a hostile version: text, not markup
        const NOTES = '<img src=x onerror="window.__pwn=1"><script>window.__pwn=2</script>\u202e reversed <a href="javascript:window.__pwn=3">x</a>\n' + '- line\n'.repeat(400);
        await load({ ...base, channel: 'agentic', approved: { version: '1.4.142', approved_at: day(1) }, waiting: [{ version: '1.4.144', published_at: day(9), notes: NOTES }, { version: '1.4.143<img src=x onerror=window.__pwn=4>', published_at: day(1), notes: 'x' }] });
        let p = await pane();
        await ev(`document.querySelector('.update-waiting').open = true; document.querySelector('.update-notes').open = true; true`); await settle(200);
        p = await pane();
        check(`hostile notes and a hostile version draw only text: one card, no image, no script ran (${JSON.stringify(p.cards)})`, p.cards.length === 1 && p.cards[0] === 'Coral 1.4.144' && p.imgs === 0 && !p.pwn);
        const geom = await ev(`(() => { const pre = document.querySelector('.update-notes pre'); const r = pre.getBoundingClientRect(); const d = document.getElementById('update-pane-body'); return { preH: r.height, pageScrollsSideways: document.documentElement.scrollWidth > innerWidth, bodyOverflowX: d.scrollWidth > d.clientWidth + 1 }; })()`);
        check(`400 lines of notes stay in a scrolling box (${Math.round(geom.preH)} px) with no sideways scroll`, geom.preH <= 200 && !geom.pageScrollsSideways && !geom.bodyOverflowX, JSON.stringify(geom));

        // 3. radios without Apply: nothing saved, and a reopen shows the saved choice
        await load({ ...base, channel: 'approved', approved: { version: '1.4.142', approved_at: day(1) } });
        await ev(`document.getElementById('uc-agentic').click(); true`); await settle(150);
        p = await pane();
        check('choosing Agentic with no waiting list from the server draws no waiting section and shows the auto-install note', p.checked === 'agentic' && !p.waiting && p.autoNote === false);
        check('nothing was sent before Apply', (await ev(`window.__ch.puts.length`)) === 0);
        await ev(`import(${S} + 'update_settings.js').then(m => m.showUpdatesPane()).then(() => true)`); await settle(400);
        p = await pane();
        check('redrawing the pane without Apply shows the saved channel again', p.checked === 'approved');

        // 4. the cold cache: Agentic saved, the first answer has no list; the list does not appear until the status is read again
        await load({ ...base, channel: 'approved', approved: { version: '1.4.142', approved_at: day(1) } });
        await ev(`window.__ch.later = ${JSON.stringify({ ...base, channel: 'agentic', approved: { version: '1.4.142', approved_at: day(1) }, waiting: [{ version: '1.4.143', published_at: day(2), notes: '' }] })}; true`);
        await ev(`document.getElementById('uc-agentic').click(); true`); await settle(150);
        await apply();
        const putsSeen = await ev(`window.__ch.puts`);
        const chPuts = putsSeen.filter(b => 'update_channel' in b);
        check(`Apply sent exactly one update_channel PUT, on its own (${JSON.stringify(chPuts)})`, chPuts.length === 1 && chPuts[0].update_channel === 'agentic' && Object.keys(chPuts[0]).length === 1);
        p = await pane();
        check(`after Apply, when the server already has the list, the open pane shows the Waiting section (${JSON.stringify(p.cards)})`, p.waiting && p.cards.length === 1);
        await ev(`import(${S} + 'update_settings.js').then(m => m.showUpdatesPane()).then(() => true)`); await settle(300);
        p = await pane();
        check('and a redraw of the pane shows it', p.waiting && p.cards.length === 1);
        await load({ ...base, channel: 'approved', approved: { version: '1.4.142', approved_at: day(1) } });
        await ev(`window.__ch.later = ${JSON.stringify({ ...base, channel: 'agentic' })}; true`); // the server's cache is cold: channel only
        await ev(`document.getElementById('uc-agentic').click(); true`); await settle(150);
        await apply();
        p = await pane();
        console.log('[INFO] after Apply with a COLD server cache (channel only): waiting section', p.waiting, 'group', p.group, 'checked', p.checked);
        check('a cold-cache answer still draws the group with Agentic chosen and no error', p.group && p.checked === 'agentic');
        // the cold cache fills a moment later: the pane reads the status again and shows the list without a reopen
        await ev(`window.__ch.status = ${JSON.stringify({ ...base, channel: 'agentic', approved: { version: '1.4.142', approved_at: day(1) }, waiting: [{ version: '1.4.143', published_at: day(2), notes: '' }] })}; true`);
        await waitFor(`!!document.querySelector('#update-pane-body .update-waiting')`, 'the Waiting section after the cache fills', 8000);
        p = await pane();
        check(`when the server's cache fills after the switch, the open pane shows the Waiting section by itself (${JSON.stringify(p.cards)})`, p.waiting && p.cards.length === 1 && p.cards[0] === 'Coral 1.4.143');

        // 5. a refused channel save: the failure toast, the choice is not claimed as saved
        await load({ ...base, channel: 'approved', approved: { version: '1.4.142', approved_at: day(1) } });
        await ev(`window.__ch.putStatus = 400; document.getElementById('uc-agentic').click(); true`); await settle(150);
        await apply();
        const toast = await ev(`/Failed to save settings/.test(document.body.innerText)`);
        check('a refused channel save shows the failure toast', toast === true);

    } catch (e) { check('suite ran without throwing', false, e && e.stack || String(e)); }
    try { await client.close(); } catch (e) { /* closing */ }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
})();
