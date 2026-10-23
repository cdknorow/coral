// The History page's search for a person using a screen reader or only the keyboard (#6288): the advanced filters name every field and group and say which choice is on (aria-pressed, not a colour alone);
// a search result is named for the chat it is in (its title from the list, not its id) and each hit reads as who said it, when, and the excerpt, with no stray punctuation or raw timestamp; the list itself is
// not a live region (a whole list read out on every keystroke) but a status line says how many chats match, or that none do; and opening a hit leaves focus on the chat. Desktop and a 390 px phone.
// Drives Chrome over CDP through agent_view_harness.js with the census data world (helpers/census_world.js), against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { DATA_WORLD } = require('./helpers/census_world');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const typeSearch = text => ev(`(() => { const i = document.getElementById('history-search'); i.focus(); i.value = ${JSON.stringify(text)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Page.addScriptToEvaluateOnNewDocument({ source: DATA_WORLD });
        for (const [w, h, mobile, tag] of [[1280, 800, false, 'desktop'], [390, 844, true, 'phone']]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
            await ev(`window.switchNavTab('history'); true`);
            await waitFor(`document.getElementById('history-sessions-list').textContent.includes('Release notes chat')`, `${tag}: the chats`, 10000);

            // the advanced filters
            await ev(`document.getElementById('hf-adv-toggle').click(); true`);
            await waitFor(`document.getElementById('hf-advanced').style.display !== 'none'`, `${tag}: advanced filters`, 5000);
            const f = await ev(`(() => {
                const name = e => { const l = e.getAttribute('aria-labelledby'); if (l) return l.split(' ').map(i => (document.getElementById(i) || {}).textContent || '').join(' ').trim(); if (e.getAttribute('aria-label')) return e.getAttribute('aria-label'); return e.labels && e.labels.length ? [...e.labels].map(x => x.textContent).join(' ').trim() : ''; };
                const adv = document.getElementById('hf-advanced');
                const groups = [...adv.querySelectorAll('.hf-toggle-group')].map(g => ({ role: g.getAttribute('role'), name: g.getAttribute('aria-label') || '', buttons: [...g.querySelectorAll('.hf-toggle')].map(b => ({ t: b.textContent.trim(), on: b.classList.contains('active'), pressed: b.getAttribute('aria-pressed') })) }));
                const inputs = [...adv.querySelectorAll('input, select')].map(e => ({ id: e.id, name: name(e) }));
                const texts = groups.flatMap(g => g.buttons.map(b => b.t));
                return { groups, inputs, dupTexts: texts.filter((t, i) => texts.indexOf(t) !== i) };
            })()`);
            for (const g of f.groups) {
                assert.ok(g.role === 'group' && g.name, `${tag}: a filter group (${g.buttons.map(b => b.t).join('/')}) is a named group (${JSON.stringify([g.role, g.name])})`);
                for (const b of g.buttons) assert.equal(b.pressed, String(b.on), `${tag}: "${b.t}" in ${g.name} says whether it is on with aria-pressed (${b.pressed}, on ${b.on})`);
            }
            for (const i of f.inputs) assert.ok(i.name, `${tag}: ${i.id} has an accessible name`);
            assert.deepEqual(f.dupTexts, [], `${tag}: no two filter buttons read the same: ${JSON.stringify(f.dupTexts)}`);
            // choosing another source moves the pressed state with it
            await ev(`document.querySelector('[data-source="claude"]').click(); true`);
            const moved = await ev(`[...document.querySelectorAll('[data-source]')].map(b => b.getAttribute('aria-pressed') === String(b.classList.contains('active')))`);
            assert.deepEqual(moved, moved.map(() => true), `${tag}: aria-pressed follows the choice (${JSON.stringify(moved)})`);
            await ev(`document.querySelector('[data-source="claude"]').click(); document.getElementById('hf-adv-toggle').click(); true`);

            // the results
            await typeSearch('release');
            await waitFor(`document.querySelectorAll('#chat-search-results .chat-search-hit').length === 3`, `${tag}: the hits`, 10000);
            const r = await ev(`(() => {
                const box = document.getElementById('chat-search-results'), st = document.getElementById('chat-search-status');
                return { live: box.getAttribute('aria-live'), status: st ? st.textContent.trim() : null, statusRole: st ? st.getAttribute('role') : null,
                    heads: [...box.querySelectorAll('.chat-search-result-header strong')].map(e => e.textContent.trim()),
                    sub: [...box.querySelectorAll('.chat-search-result-header span')].map(e => e.textContent.trim()),
                    hits: [...box.querySelectorAll('.chat-search-hit')].map(b => ({ label: b.getAttribute('aria-label'), meta: b.querySelector('.chat-search-hit-meta').textContent.trim(), text: b.innerText.replace(/\\s+/g, ' ').trim() })) };
            })()`);
            assert.equal(r.live, null, `${tag}: the result list is not a live region (the whole list is not read out): ${r.live}`);
            assert.equal(r.statusRole, 'status', `${tag}: a status line says how many chats match`);
            assert.match(r.status, /^2 chats match/, `${tag}: the status counts the chats (${r.status})`);
            assert.deepEqual(r.heads, ['Release notes chat', 'Build fix chat'], `${tag}: each result is named for its chat, not its id (${JSON.stringify(r.heads)})`);
            for (const s of r.sub) assert.ok(!/(^|\s)·\s*$|^·/.test(s), `${tag}: no dangling separator in "${s}"`);
            for (const h of r.hits) {
                assert.ok(!/\s:|: *$/.test(h.label || ''), `${tag}: a hit's name has no stray colon (${JSON.stringify(h.label)})`);
                assert.ok(!/\d{4}-\d\d-\d\dT/.test(h.text), `${tag}: a hit shows no raw timestamp (${h.text})`);
                assert.ok(!/·\s*$/.test(h.meta), `${tag}: no dangling separator in "${h.meta}"`);
                assert.match(h.text, /^(You|Agent)\b/, `${tag}: a hit says who spoke (${h.text})`);
            }

            // nothing found
            await typeSearch('zzz');
            await waitFor(`/No matching chats/.test(document.getElementById('chat-search-results').textContent)`, `${tag}: no results`, 10000);
            assert.match(await ev(`document.getElementById('chat-search-status').textContent`), /No matching chats/, `${tag}: the status says nothing matched`);

            // opening a hit
            await typeSearch('release');
            await waitFor(`document.querySelectorAll('#chat-search-results .chat-search-hit').length === 3`, `${tag}: the hits again`, 10000);
            await ev(`document.querySelector('#chat-search-results .chat-search-hit').click(); true`);
            await waitFor(`document.getElementById('history-messages').textContent.includes('Hi there')`, `${tag}: the chat`, 10000);
            await new Promise(r => setTimeout(r, 400)); // sleep-ok: focus settles after the chat draws
            const foc = await ev(`(() => { const a = document.activeElement; return { tag: a && a.tagName, id: a && a.id, inChat: !!(a && a.closest && a.closest('#history-session-view, #history-messages, #history-session-title, .history-session')), gone: !a || a === document.body }; })()`);
            assert.ok(!foc.gone, `${tag}: focus is not dropped on the page after opening a hit (${JSON.stringify(foc)})`);
            assert.ok(foc.inChat, `${tag}: focus is in the opened chat (${JSON.stringify(foc)})`);
            console.log(`   ✓ ${tag}`);
        }
        console.log('PASS History search: filters named and pressed, results named for their chat, one status line, focus follows an opened hit');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
