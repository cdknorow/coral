// A polite live region that is shown and given its words in the same turn says nothing to a screen reader (#6659; live_region_lint.test.js finds it in the static JS for the regions the templates declare, this finds it
// at runtime, for any region, the 20 or so that JS makes included). helpers/announce_census.js marks such a change `silent` (it is not an announcement, and problems() names it unless the region is on the
// KNOWN_SILENT list, the open offenders of #6656). This suite is the rule's own test: a region JS makes and fills at once is found; one shown first and filled a frame later is announced and not flagged; the held-messages pattern (a hidden region, its words in a child) is found and named unless the list holds it. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { watch, clear, log, problems, said, KNOWN_SILENT } = require('./helpers/announce_census');
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const wait = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: announcements are timed; there is no event to wait for
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.__m.add('rep', 'R', 4, { sleeping: false }); window._coralSetLiveSessions && window._coralSetLiveSessions(Object.values(window.__m.agents).map(a => window.__m.entry(a))); localStorage.setItem('coral-live-view-mode', 'chat'); true`);
        await select('rep');
        await waitFor(`window.__rendered().length >= 4`, 'the chat to render', 15000);
        await waitFor(`!!document.getElementById('composer-held-notice') && document.getElementById('composer-held-notice').parentElement.getClientRects().length > 0`, 'the composer', 10000);
        await watch(ev);
        // two regions JS makes, hidden, in the page before anything happens
        await ev(`(() => { for (const id of ['js-silent', 'js-fixed']) { const el = document.createElement('div'); el.id = id; el.setAttribute('role', 'status'); el.hidden = true; document.body.append(el); } return true; })()`);
        await wait(300); // sleep-ok: the observer has taken the empty hidden regions in

        await clear(ev);
        await ev(`(() => { const el = document.getElementById('js-silent'); el.hidden = false; el.textContent = 'Saved the file'; return true; })()`);
        await wait(300); // sleep-ok: the batch of changes is delivered
        let entries = await log(ev);
        assert.ok(entries.some(e => e.silent && e.region === '#js-silent'), 'a region JS makes, shown and filled in one turn, is marked silent: ' + JSON.stringify(entries));
        assert.equal(said(entries, /Saved the file/).length, 0, 'and it is not counted as an announcement');
        assert.ok(problems('js region', entries).some(p => /#js-silent was shown and given its words in the same turn/.test(p)), 'problems() names it: ' + JSON.stringify(problems('js region', entries)));

        await clear(ev);
        await ev(`document.getElementById('js-fixed').hidden = false; true`);
        await wait(120); // sleep-ok: a few frames pass: the region is in the page
        await ev(`document.getElementById('js-fixed').textContent = 'Saved the file'; true`);
        await wait(300); // sleep-ok: the change is delivered
        entries = await log(ev);
        assert.equal(entries.filter(e => e.silent).length, 0, 'shown first, filled later: not silent: ' + JSON.stringify(entries));
        assert.equal(said(entries, /Saved the file/).length, 1, 'and announced once');
        assert.deepEqual(problems('fixed region', entries), [], 'and no problem');

        // the held-messages line as it was built before #6656 (a region hidden, its words in a child, both set in one call), made here so the test does not depend on any offender being in the product
        await ev(`(() => { const p = document.createElement('p'); p.id = 'js-held'; p.setAttribute('role', 'status'); p.hidden = true; const t = document.createElement('span'); t.id = 'js-held-text'; p.append(t); document.body.append(p); return true; })()`);
        await wait(300); // sleep-ok: the observer has taken the hidden region in
        await clear(ev);
        await ev(`(() => { const el = document.getElementById('js-held'); el.hidden = false; document.getElementById('js-held-text').textContent = '2 messages waiting: Coral cannot see the input box'; return true; })()`);
        await wait(300); // sleep-ok: the change is delivered
        entries = await log(ev);
        assert.ok(entries.some(e => e.silent && e.region.startsWith('#js-held')), 'the region shown with its child filled in one call is found: ' + JSON.stringify(entries));
        assert.ok(problems('held line', entries).some(p => /#js-held\S* was shown and given its words in the same turn/.test(p)), 'and named by problems() when it is not on the list: ' + JSON.stringify(problems('held line', entries)));
        assert.deepEqual(problems('held line', entries, { allow: ['#js-held'] }), [], 'and not named when the list holds it');
        // the allowed list is the lint's own list, one copy
        const { KNOWN } = require('./helpers/live_region_known');
        assert.deepEqual(KNOWN_SILENT, Object.keys(KNOWN).map(k => '#' + k.split(':')[1]), 'the census reads the lint\'s list');
        console.log('PASS announce_silent: a polite region shown and filled in one turn is found, one shown first is not, a real offender is named unless listed');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
