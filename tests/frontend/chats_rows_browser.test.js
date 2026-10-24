// The Chats list and the Groups in it (#6961, UI Accessibility's #6960): a chat row says WHEN as text for today and yesterday ("today 14:32", "yesterday 17:20"), an older day by its date, one format with the
// Group row (laid out like a chat row: its name and Group badge, then one line of facts with its messages, members and when), the Saved items and the Inbox's tasks; a long title never pushes the agent badge off
// the row; two untitled chats are told apart; a chat with no timestamp still has its facts line; the count is said once ("Chats 9" and not also "9 sessions"). 1280 px, and 390 px at 200% text. The history rows
// are the list's own renderer given stubbed sessions. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile, font] of [[1280, 900, false, ''], [390, 844, true, '32px']]) {
            const tag = `${w} px${font ? ', 200% text' : ''}`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled(mobile ? { enabled: true, maxTouchPoints: 5 } : { enabled: false });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`${font ? `document.documentElement.style.fontSize = '${font}';` : ''} window.switchNavTab('history'); true`);
            await ev(`(async () => { const m = await import(${S} + 'history_list.js'); const at = (dd, hh, mm) => { const d = new Date(); d.setDate(d.getDate() - dd); d.setHours(hh, mm, 0, 0); return d.toISOString(); };
                const rows = [
                    { session_id: 'aaaaaa1', summary_title: 'Fix the flaky test', source_type: 'claude', display_name: 'Ada', last_timestamp: at(0, 14, 32), duration_sec: 21 * 60 },
                    { session_id: 'aaaaaa2', summary_title: 'Review the plan', source_type: 'claude', display_name: 'Ada', last_timestamp: at(0, 9, 10), duration_sec: 5 * 60 },
                    { session_id: 'aaaaaa3', summary_title: 'Yesterday afternoon', source_type: 'claude', display_name: 'Ada', last_timestamp: at(1, 17, 20), duration_sec: 60 },
                    { session_id: 'aaaaaa4', summary_title: 'Last week', source_type: 'claude', display_name: 'Ada', last_timestamp: at(10, 11, 0), duration_sec: 15 * 60 },
                    { session_id: 'bbbbbb1', source_type: 'claude', display_name: 'Ada', last_timestamp: at(2, 8, 0) },
                    { session_id: 'cccccc2', source_type: 'claude', display_name: 'Ada', last_timestamp: at(3, 8, 0) },
                    { session_id: 'dddddd1', summary_title: 'A chat whose indexing never kept its time', source_type: 'claude', display_name: 'Ada' },
                    { session_id: 'eeeeee1', summary_title: 'An extremely long chat title that goes on and on and on, well past what one row of the sidebar can hold at any size at all', source_type: 'codex', display_name: 'A-long-agent-name' },
                    { type: 'group', session_id: 'board:team-a', title: 'team-a', updated_at: at(0, 13, 5), message_count: 23, subscriber_count: 4 } ];
                const w = await import(${S} + 'when_text.js'); window.__exp = { fix: w.whenText(rows[0].last_timestamp), rev: w.whenText(rows[1].last_timestamp), yest: w.whenText(rows[2].last_timestamp), grp: w.whenText(rows[8].updated_at) };
                m.renderHistorySessions(rows, rows.length, 1, 50); return true; })()`);
            await waitFor(`document.querySelectorAll('#history-sessions-list li[data-session-id], #history-sessions-list li[data-board]').length === 9`, 'the nine rows', 8000);
            await sleep(300); // sleep-ok: the list lays out
            const rows = await ev(`[...document.querySelectorAll('#history-sessions-list li')].map(li => { const lr = li.getBoundingClientRect(); const bd = li.querySelector('.badge'); const br = bd && bd.getBoundingClientRect();
                return { title: (li.querySelector('.session-label') || {}).textContent, facts: (li.querySelector('.chat-row-facts') || {}).textContent || '', top: !!li.querySelector('.session-row-top'), badge: bd ? bd.textContent : '', badgeIn: br ? br.right <= lr.right + 0.5 && br.left >= lr.left - 0.5 && br.width > 8 : null, geo: br ? [Math.round(lr.left), Math.round(lr.right), Math.round(br.left), Math.round(br.right), Math.round((li.querySelector('.session-label') || li).getBoundingClientRect().width), getComputedStyle(li.querySelector('.chat-row-title') || li).display] : null, label: li.querySelector('.session-time time') ? li.querySelector('.session-time time').textContent : '' }; })`);
            const by = t => rows.find(r => r.title && r.title.startsWith(t));
            const exp = await ev(`window.__exp`);
            check(`${tag}: each chat says when as text, in the one format (the same words as when_text.js gives its stamp: a time of day in the viewer's clock for today and yesterday, "N min ago" under an hour), and two chats of one day are told apart`, by('Fix the flaky').facts.includes(exp.fix) && by('Review the plan').facts.includes(exp.rev) && by('Yesterday afternoon').facts.includes(exp.yest) && /yesterday \d{1,2}:\d\d( [AP]M)?/.test(exp.yest) && exp.fix !== exp.rev, JSON.stringify([exp, by('Fix the flaky').facts, by('Review the plan').facts, by('Yesterday afternoon').facts]));
            check(`${tag}: how long it ran still follows ("ran 21 min") and an older day is its date alone (no time)`, new RegExp(exp.fix.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + ', ran 21 min').test(by('Fix the flaky').facts) && /^Ada · [A-Z][a-z]{2} \d{1,2}, ran 15 min$/.test(by('Last week').facts) && !/\d\d:\d\d/.test(by('Last week').facts), by('Last week').facts);
            const grp = by('team-a');
            check(`${tag}: the Group row is laid out as a chat row: its name and Group badge, then one line of facts (messages, members, when) in the same words, no row of its own for the date`, grp && grp.badge === 'Group' && !grp.top && /23 msgs/.test(grp.facts) && /4 members/.test(grp.facts) && grp.facts.includes(exp.grp), JSON.stringify(grp));
            const long = by('An extremely long');
            check(`${tag}: a long title never pushes the agent badge off the row (the Codex badge is whole and inside it)`, long.badge === 'codex' && long.badgeIn === true, JSON.stringify(long));
            const unt = rows.filter(r => /^Chat /.test(r.title || ''));
            check(`${tag}: untitled chats are told apart (their titles differ)`, unt.length === 2 && unt[0].title !== unt[1].title, JSON.stringify(unt.map(r => r.title)));
            check(`${tag}: a chat with no timestamp still has its facts line`, by('A chat whose indexing').facts.length > 0, JSON.stringify(by('A chat whose indexing').facts));
            check(`${tag}: the count is said once: the footer does not repeat "9 sessions"`, await ev(`!/sessions?$/.test(((document.getElementById('history-pagination') || {}).textContent || '').trim())`), await ev(`((document.getElementById('history-pagination') || {}).textContent || '').trim()`));
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} chats row checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
