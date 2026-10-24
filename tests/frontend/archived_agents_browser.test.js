// Archived agents (#3030): archived agents leave the main list, team groups and
// counts and sit in one collapsed 'Archived (N)' section grouped by team; the
// kebab offers Archive / Unarchive; an archived agent's chat is readable with
// Unarchive + Wake in the composer; bulk wakes never wake it. Local and
// hub-remote agents, the popout and a phone-width viewport.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

const ARCHIVED_PLACEHOLDER = '';
const SLEEPING_PLACEHOLDER = '';

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install, select, view, reqs, clearReqs,
        firstPage, scrollToTop, settled } = await connect();

    // The sidebar as a reader sees it.
    const SIDEBAR = `((listSel = '#live-sessions-list') => {
        const list = document.querySelector(listSel);
        const items = [...list.querySelectorAll(':scope > li, :scope > * > li')];
        const summary = list.querySelector('.archived-summary-row');
        let inArchived = false;
        const main = [], archived = [], captions = [];
        let caption = null;
        for (const li of list.querySelectorAll('li')) {
            if (li.classList.contains('archived-summary-row')) { inArchived = true; continue; }
            if (li.classList.contains('archived-team-caption')) { caption = li.textContent.trim(); captions.push(caption); continue; }
            const sid = li.dataset.sessionId;
            if (!sid) continue;
            (inArchived ? archived : main).push(inArchived ? { sid, caption } : sid);
        }
        return {
            summary: summary ? summary.textContent.trim() : null,
            expanded: summary && summary.querySelector('.row-btn') ? summary.querySelector('.row-btn').getAttribute('aria-expanded') : null, // the button in the row says it (#7006)
            main, archived, captions,
            sleepingSummaries: [...list.querySelectorAll('.sleeping-summary-row:not(.archived-summary-row)')].map(x => x.textContent.trim()),
        };
    })`;
    const sidebar = (sel) => ev(`${SIDEBAR}(${sel ? JSON.stringify(sel) : ''})`);
    const render = () => ev(`T.render.renderLiveSessions(T.state.liveSessions)`);
    const menuItems = sid => ev(`(() => {
        const li = document.querySelector('#live-sessions-list li[data-session-id="${sid}"]');
        if (li) { window.closeSidebarKebabs(); li.querySelector('.sidebar-kebab-btn').click(); } // the menu is drawn when it opens (#4611)
        return li ? [...li.querySelectorAll('.sidebar-kebab-menu .overflow-menu-item')].map(b => b.textContent.replace(/\\s+/g, ' ').trim()) : null;
    })()`);
    const modalOpen = () => ev(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`);
    const agentRow = sid => ev(`(() => { const a = T.state.liveSessions.find(s => s.session_id === '${sid}'); return a && { sleeping: !!a.sleeping, archived: !!a.archived }; })()`);

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        // Start from default sidebar preferences: suites share the browser profile in a shard, and an earlier
        // one may have switched grouping (coral-group-by-team) or expanded groups.
        await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat')`);

        // team-one: awake lead, sleeping worker, two archived; team-two: one archived; no team: one archived,
        // one awake; a hub-remote archived agent in team-one.
        await ev(`(() => {
            const m = window.__m;
            m.add('t1-lead', 'L', 120, { sleeping: false, board_project: 'team-one' });
            m.add('t1-sleeper', 'S', 120, { sleeping: true, board_project: 'team-one' });
            m.add('t1-arch-a', 'A', 600, { archived: true, board_project: 'team-one' });
            m.add('t1-arch-b', 'B', 120, { archived: true, board_project: 'team-one' });
            m.add('t2-arch', 'C', 120, { archived: true, board_project: 'team-two' });
            m.add('solo-arch', 'D', 120, { archived: true });
            m.add('solo-awake', 'E', 120, { sleeping: false });
            m.add('remote-arch', 'R', 300, { archived: true, board_project: 'team-one', server: 'qa-remote-1' });
            T.render.renderLiveSessions(T.state.liveSessions);
        })()`);

        console.log('1. Sidebar: archived agents leave the main list and counts, one collapsed section');
        let sb = await sidebar();
        const archivedIds = ['t1-arch-a-id', 't1-arch-b-id', 't2-arch-id', 'solo-arch-id', 'remote-arch-id'];
        assert.ok(archivedIds.every(id => !sb.main.includes(id)), `no archived agent in the main list (${JSON.stringify(sb.main)})`);
        assert.ok(['t1-lead-id', 'solo-awake-id'].every(id => sb.main.includes(id)), 'awake agents still listed');
        assert.match(sb.summary || '', /^Archived \(5\)$/, `one 'Archived (5)' row (got ${sb.summary})`);
        assert.equal(sb.expanded, 'false', 'collapsed by default');
        assert.equal(sb.archived.length, 0, 'no archived rows while collapsed');
        assert.ok(sb.sleepingSummaries.every(t => !/\b[2-9] sleeping/.test(t)), `team-one's sleeping count excludes archived (${JSON.stringify(sb.sleepingSummaries)})`);
        assert.ok(sb.sleepingSummaries.some(t => /^1 sleeping agent$/.test(t)), `team-one shows '1 sleeping agent' (${JSON.stringify(sb.sleepingSummaries)})`);
        console.log(`   ✓ main list clean, 'Archived (5)' collapsed, team-one: 1 sleeping`);

        await ev(`document.querySelector('#live-sessions-list .archived-summary-row').click()`);
        await waitFor(`document.querySelector('#live-sessions-list .archived-summary-row .row-btn').getAttribute('aria-expanded') === 'true'`, 'section to expand');
        sb = await sidebar();
        assert.equal(await ev(`localStorage.getItem('coral-archived-expanded')`), 'true', 'expanded state persisted');
        const capLower = sb.captions.map(c => c.toLowerCase());
        assert.equal(capLower[capLower.length - 1], 'no team', `'No team' caption last (${JSON.stringify(sb.captions)})`);
        assert.ok(capLower.indexOf('team-one') >= 0 && capLower.indexOf('team-two') > capLower.indexOf('team-one'), `team captions sorted (${JSON.stringify(sb.captions)})`);
        const where = Object.fromEntries(sb.archived.map(x => [x.sid, x.caption.toLowerCase()]));
        assert.deepEqual(Object.keys(where).sort(), [...archivedIds].sort(), 'every archived agent listed once in the section');
        assert.equal(where['t1-arch-a-id'], 'team-one'); assert.equal(where['remote-arch-id'], 'team-one', 'a remote agent is grouped by its team too');
        assert.equal(where['t2-arch-id'], 'team-two'); assert.equal(where['solo-arch-id'], 'no team');
        await render();
        assert.equal((await sidebar()).expanded, 'true', 'still expanded after a re-render');
        console.log(`   ✓ expanded (persisted): ${sb.captions.join(' / ')}`);

        await ev(`window.filterLiveAgents('arch-a')`);
        sb = await sidebar();
        assert.match(sb.summary || '', /^Archived \(1\)$/, `search narrows the archived count (got ${sb.summary})`);
        assert.deepEqual(sb.archived.map(x => x.sid), ['t1-arch-a-id']);
        await ev(`window.filterLiveAgents('zzz-nothing')`);
        assert.equal((await sidebar()).summary, null, 'no archived section when nothing archived matches');
        await ev(`window.filterLiveAgents('')`);
        console.log('   ✓ search filters the archived section');

        console.log('2. Kebab menus');
        const archivedMenu = await menuItems('t1-arch-a-id');
        assert.ok(archivedMenu.some(t => /^Unarchive$/.test(t)) && archivedMenu.some(t => /^Wake/.test(t)), `archived menu has Unarchive and Wake (${JSON.stringify(archivedMenu)})`);
        assert.ok(!archivedMenu.some(t => /Restart|Rename|Move|^Archive$/.test(t)), `archived menu has no Restart/Rename/Move/Archive (${JSON.stringify(archivedMenu)})`);
        await ev(`(() => { const e = document.querySelector('#live-sessions-list .sleeping-summary-row:not(.archived-summary-row)'); if (e) e.click(); })()`);
        for (const sid of ['t1-lead-id', 't1-sleeper-id']) {
            const items = await menuItems(sid);
            assert.ok(items && items.some(t => /^Archive$/.test(t)), `${sid} menu offers Archive (${JSON.stringify(items)})`);
        }
        console.log(`   ✓ archived: ${archivedMenu.join(', ')}; awake + sleeping offer Archive`);

        console.log('3. Archive asks first; Cancel settles false and sends nothing');
        await clearReqs();
        await ev(`(() => { window.__archR = undefined; window.toggleAgentArchive('solo-awake', 'claude', 'solo-awake-id', 'archive').then(v => { window.__archR = v; }); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'confirm modal');
        assert.match(await ev(`document.getElementById('confirm-modal-message').textContent`), /stopped/i, 'archiving an awake agent says it will be stopped');
        await ev(`[...document.querySelectorAll('#confirm-modal button')].find(b => /cancel/i.test(b.textContent)).click()`);
        await waitFor(`window.__archR === false`, 'archive promise to resolve false on cancel', 2000);
        assert.equal((await reqs()).filter(r => r.kind === 'archive').length, 0, 'no POST /archive after cancel');
        console.log('   ✓ cancel: promise false, no request');

        console.log('4. Archiving the agent you are viewing switches the view at once (one re-select at most)');
        await select('solo-awake');
        await firstPage();
        assert.equal((await view()).inputDisabled, false, 'awake agent: input enabled');
        await clearReqs();
        await ev(`(() => { window.__archR = undefined; window.toggleAgentArchive('solo-awake', 'claude', 'solo-awake-id', 'archive').then(v => { window.__archR = v; }); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'confirm modal');
        await ev(`document.getElementById('confirm-modal-yes').click()`);
        await waitFor(`window.__archR === true`, 'archive to succeed', 3000);
        await waitFor(`window.__view().composer`, 'archived composer right after the POST (no feed yet)', 2000);
        let v = await view();
        assert.equal(v.composerText, 'This agent is archived');
        assert.ok(v.unarchive && v.unarchivePrimary, 'Unarchive shown as the primary action');
        assert.ok(v.wake && v.wakePrimary === false, 'Wake shown as the secondary action');
        assert.equal(v.restart, false, 'no Restart for an archived agent');
        assert.equal(v.inputDisabled, true, 'input disabled');
        assert.equal(v.placeholder, ARCHIVED_PLACEHOLDER);
        // The feed's flip arrives afterwards: still one re-select in total.
        await ev(`window.__feed('solo-awake-id', { sleeping: true, archived: true })`);
        await sleep(300);
        await settled();
        let r = await reqs();
        assert.equal(r.filter(x => x.kind === 'archive').length, 1, 'one POST /archive');
        assert.ok(await ev('window.__selCalls') <= 1, `at most one re-select for archive + feed (got ${await ev('window.__selCalls')})`);
        assert.ok(r.filter(x => x.first).length <= 1, 'at most one first-page /chat');
        sb = await sidebar();
        assert.ok(sb.archived.some(x => x.sid === 'solo-awake-id') && !sb.main.includes('solo-awake-id'), 'the row moved to the Archived section');
        assert.match(sb.summary, /^Archived \(6\)$/);
        console.log('   ✓ archived view at once, 1 POST, ≤1 re-select, row moved, count 6');

        console.log('5. An archived agent: history, autoload, Unarchive in place');
        await select('t1-arch-a');
        const first = await firstPage();
        v = await view();
        assert.ok(v.chatVisible && v.chatUncovered, 'history shown and uncovered');
        assert.equal(v.composerText, 'This agent is archived');
        assert.equal(v.placeholder, ARCHIVED_PLACEHOLDER);
        await clearReqs();
        await scrollToTop();
        await waitFor(`window.__m.requests.some(r => r.older && r.settled)`, 'older page');
        // The older page is built in slices after its reply arrives (#3866): wait for it to show.
        await waitFor(`window.__rendered().length > ${first.length}`, 'older history to show');
        assert.ok((await ev('window.__rendered()')).length > first.length, 'older history loads');
        await clearReqs();
        await ev(`document.getElementById('sleeping-unarchive-btn').click()`);
        await waitFor(`window.__view().composerText === 'This agent is sleeping'`, 'sleeping composer after Unarchive', 3000);
        v = await view();
        assert.equal(v.unarchive, false, 'Unarchive hidden');
        assert.ok(v.restart && v.wakePrimary, 'Restart back, Wake primary');
        assert.equal(v.placeholder, SLEEPING_PLACEHOLDER);
        assert.equal(v.inputDisabled, true, 'still asleep: input disabled');
        r = await reqs();
        assert.equal(r.filter(x => x.kind === 'unarchive').length, 1, 'one POST /unarchive');
        assert.equal(await ev('window.__selCalls'), 0, 'Unarchive switches the view in place (no re-select)');
        sb = await sidebar();
        assert.ok(!sb.archived.some(x => x.sid === 't1-arch-a-id'), 'row left the Archived section');
        assert.ok(sb.sleepingSummaries.some(t => /^2 sleeping agents$/.test(t)), `team-one now has 2 sleeping (${JSON.stringify(sb.sleepingSummaries)})`);
        console.log('   ✓ unarchived in place: sleeping composer, row back in team-one (2 sleeping)');

        console.log('6. Wake on an archived agent wakes and unarchives it');
        await select('t1-arch-b');
        await firstPage();
        await clearReqs();
        await ev(`document.getElementById('sleeping-wake-btn').click()`);
        await waitFor(`!document.getElementById('command-input').disabled`, 'awake after Wake', 5000);
        await sleep(200);
        r = await reqs();
        assert.equal(r.filter(x => x.kind === 'wake').length, 1, 'one POST /wake');
        assert.equal(await ev('window.__selCalls'), 1, 'one re-select');
        assert.equal((await view()).composer, false, 'composer hidden');
        sb = await sidebar();
        assert.ok(sb.main.includes('t1-arch-b-id') && !sb.archived.some(x => x.sid === 't1-arch-b-id'), 'woken agent back in the main list');
        console.log('   ✓ wake: 1 POST, 1 re-select, back in the main list');

        console.log('7. Team wake leaves archived agents archived');
        for (const how of ['team']) {
            await ev(`for (const s of T.state.liveSessions) if (!s.archived) s.sleeping = true; for (const a of Object.values(window.__m.agents)) if (!a.archived) a.sleeping = true; T.render.renderLiveSessions(T.state.liveSessions)`);
            await ev(`(() => { window.toggleTeamSleep('team-one', 'wake'); return true; })()`);
            if (await modalOpen()) await ev(`document.getElementById('confirm-modal-yes').click()`);
            await waitFor(`window.__m.requests.some(r => r.kind === 'team-wake')`, `${how} request`, 3000);
            await sleep(300);
            for (const sid of ['t2-arch-id', 'solo-arch-id', 'remote-arch-id', 'solo-awake-id']) {
                const row = await agentRow(sid);
                assert.deepEqual(row, { sleeping: true, archived: true }, `${how}: ${sid} stays archived and asleep in the UI (${JSON.stringify(row)})`);
            }
            sb = await sidebar();
            assert.ok(['t2-arch-id', 'solo-arch-id', 'remote-arch-id'].every(id => sb.archived.some(x => x.sid === id)), `${how}: archived rows stay in the section`);
            await select('solo-arch');
            await firstPage();
            v = await view();
            assert.ok(v.composer && v.inputDisabled && v.composerText === 'This agent is archived', `${how}: selecting an archived agent afterwards gives the archived view (${JSON.stringify({ composer: v.composer, input: v.inputDisabled, text: v.composerText })})`);
            await clearReqs();
        }
        console.log('   ✓ team wake: archived agents untouched');

        console.log('8. Archived flag from the feed updates the view in place');
        // Section 7's team wake woke t1-sleeper (team-one, not archived): put it back to sleep first.
        await ev(`(() => { window.__m.agents['t1-sleeper-id'].sleeping = true; const r = T.state.liveSessions.find(s => s.session_id === 't1-sleeper-id'); r.sleeping = true; return true; })()`);
        await select('t1-sleeper');
        await firstPage();
        await clearReqs();
        await ev(`window.__feed('t1-sleeper-id', { sleeping: true, archived: true })`);
        await waitFor(`window.__view().composerText === 'This agent is archived'`, 'archived via feed', 3000);
        await ev(`window.__feed('t1-sleeper-id', { sleeping: true, archived: false })`);
        await waitFor(`window.__view().composerText === 'This agent is sleeping'`, 'unarchived via feed', 3000);
        assert.equal(await ev('window.__selCalls'), 0, 'archived flips while asleep do not re-select');
        console.log('   ✓ feed archive/unarchive flip the composer in place');

        console.log('9. Hub-remote archived agent');
        await select('remote-arch', 'qa-remote-1');
        await firstPage();
        await clearReqs();
        await ev(`document.getElementById('sleeping-unarchive-btn').click()`);
        await waitFor(`window.__view().composerText === 'This agent is sleeping'`, 'remote unarchive', 3000);
        r = await reqs();
        const un = r.filter(x => x.kind === 'unarchive');
        assert.equal(un.length, 1); assert.equal(un[0].remote, 'qa-remote-1', 'unarchive goes through /api/remote/<id>');
        await clearReqs();
        await ev(`(() => { window.toggleAgentArchive('remote-arch', 'claude', 'remote-arch-id', 'archive'); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'confirm');
        assert.doesNotMatch(await ev(`document.getElementById('confirm-modal-message').textContent`), /stopped/i, 'a sleeping agent is not "stopped"');
        await ev(`document.getElementById('confirm-modal-yes').click()`);
        await waitFor(`window.__view().composerText === 'This agent is archived'`, 'remote archive', 3000);
        r = await reqs();
        assert.equal(r.filter(x => x.kind === 'archive' && x.remote === 'qa-remote-1').length, 1, 'archive goes through /api/remote/<id>');
        console.log('   ✓ remote archive/unarchive via /api/remote/qa-remote-1');

        console.log('10. Phone width: the agent list has the Archived section and it opens');
        await ev(`localStorage.setItem('coral-archived-expanded', 'false')`);
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`(() => {
            const m = window.__m;
            m.add('m-arch', 'M', 50, { archived: true, board_project: 'team-one' });
            m.add('m-awake', 'N', 50, { sleeping: false, board_project: 'team-one' });
            T.render.renderLiveSessions(T.state.liveSessions);
        })()`);
        await ev(`(async () => { const mob = await import((window.__coralStatic||'/static/')+'mobile.js'); mob.syncMobileAgentList(); })()`);
        await waitFor(`!!document.querySelector('#mobile-session-list .archived-summary-row')`, 'mobile Archived row', 3000);
        let mb = await sidebar('#mobile-session-list');
        assert.match(mb.summary || '', /^Archived \(1\)$/);
        assert.ok(!mb.main.includes('m-arch-id'), 'mobile main list excludes the archived agent');
        await ev(`document.querySelector('#mobile-session-list .archived-summary-row').click()`);
        await waitFor(`!!document.querySelector('#mobile-session-list li[data-session-id="m-arch-id"]')`, 'tapping Archived on the phone shows the archived rows', 3000);
        mb = await sidebar('#mobile-session-list');
        assert.equal(mb.expanded, 'true', 'mobile row shows expanded');
        console.log('   ✓ phone: Archived (1), tap expands');
        await Emulation.clearDeviceMetricsOverride();

        console.log('11. Popout of an archived agent: Unarchive + Wake, no Restart');
        const PID = '55555555-5555-4555-8555-555555555581';
        await Page.addScriptToEvaluateOnNewDocument({ source: `window.__qaSeed = [{ name: 'qa-pop-arch', prefix: 'P', n: 120, archived: true, session_id: '${PID}' }];` });
        await Page.navigate({ url: `${BASE}/agent/${PID}` });
        await Page.loadEventFired();
        await waitFor(`window._coralPopout && window._coralPopout.getState && window._coralPopout.getState() !== 'loading'`, 'popout to resolve', 8000);
        await install();
        await firstPage();
        v = await view();
        assert.ok(v.composer && v.unarchive && v.wake, `popout: Unarchive + Wake (${JSON.stringify({ c: v.composer, u: v.unarchive, w: v.wake })})`);
        assert.equal(v.restart, false, 'popout: no Restart');
        assert.equal(v.composerText, 'This agent is archived');
        assert.equal(v.inputDisabled, true);
        await clearReqs();
        await ev(`document.getElementById('sleeping-unarchive-btn').click()`);
        await waitFor(`window.__view().composerText === 'This agent is sleeping'`, 'popout unarchive', 3000);
        assert.equal((await reqs()).filter(x => x.kind === 'unarchive').length, 1, 'popout: one POST /unarchive');
        assert.equal((await view()).inputDisabled, true, 'popout: still asleep after unarchive');
        console.log('   ✓ popout: Unarchive + Wake, unarchive keeps it asleep');

        console.log('\nAll archived agent checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
