// The message board's search (#4771), against the real isolated server (the search route is #4745), on a board of 700 messages:
//   - the tools row under the header has the search, Filters, First and Latest; the results line shows only while there is a search;
//   - a search finds every match of the whole board ("1 of 100 matches": 1 is the latest), takes the board to it with the match marked, and the arrows step to an older or a newer one
//     (one older than the loaded page loads the pages between; each message once); the marks pass 4.5:1 in both themes;
//   - From, Mentions me and a search together narrow it; First loads everything above and lands on the board's first message; Latest is the bottom; Clear and a search with no match;
//   - the board's windowed rendering and its prepend are as they were: the rows stay unique and the jump leaves the focus on the match.
//
// Expects CORAL_TEST_URL + CDP_PORT from the runner. Exit 0 / 1.
'use strict';
const CDP = require('chrome-remote-interface');
const { themeJs } = require('./helpers/theme');
const BASE = require('./test_base').BASE;
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

const results = [];
function check(label, cond, detail) {
    const v = cond ? 'PASS' : 'FAIL';
    results.push(v);
    console.log(`[${v}] ${label}${detail !== undefined ? ' — ' + detail : ''}`);
}

const BOARD = 'bsearch-' + Date.now().toString(36);
const N = 700;
// The messages are made by one rule (message(i)); what each search must find is counted by the same rule here, so the numbers are not guesses.
const message = i => {
    const content = i % 13 === 0 ? 'code ' + i + '\n```go\nfunc retry() error { return nil }\n```' : (i % 7 === 0 ? 'Status ' + i + ': finished the review of retry budget' : (i % 11 === 0 ? '@Operator can you look at item ' + i + '?' : 'Update ' + i + ': still working, no blockers'));
    return { from: 'dev-' + (i % 5), content: i % 7 === 0 && i % 13 !== 0 && i % 11 === 0 ? content + ' @Operator' : content };
};
const ALL = Array.from({ length: N }, (_, i) => message(i));
const EXPECT = { retry: ALL.filter(m => /retry/i.test(m.content)).length, mentions: ALL.filter(m => /@Operator/.test(m.content)).length,
    retryDev2: ALL.filter(m => /retry/i.test(m.content) && m.from === 'dev-2').length };

async function run() {
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime, Input } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    const exceptions = [];
    Runtime.exceptionThrown(e => exceptions.push((((e.exceptionDetails || {}).exception || {}).description || 'exception').slice(0, 200)));
    const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 400)); return r.result.value; };
    const { waitFor } = require('./helpers/wait').bind(ev);
const { removeBoards } = require('./helpers/board_cleanup');
    const settle = (cond, label) => waitFor(cond, label, { timeout: 10000 });
    // The mark is scrolled into view a moment after it is marked (and later on a loaded runner): a read waits, up to 3 s, for the current match to be in view, so a check of "visible" does not race the scroll.
    const IN_VIEW = `(() => { const c = document.getElementById('mb-messages'); const cur = c && c.querySelector(':scope > .mb-message-current'); if (!cur) return true; const a = cur.getBoundingClientRect(), b = c.getBoundingClientRect(); return a.top >= b.top - 2 && a.bottom <= b.bottom + 2; })()`;
    const snap = async () => { await waitFor(IN_VIEW, 'the current match in view', { timeout: 3000 }).catch(() => {}); return snap0(); };
    const snap0 = () => ev(`(() => { const c = document.getElementById('mb-messages'); const cur = c.querySelector(':scope > .mb-message-current'); const bar = document.getElementById('mb-tools'); const st = bar.querySelector('.mb-search-status');
        const rows = [...c.querySelectorAll(':scope > .mb-message')]; const ids = rows.map(r => r.dataset.messageId);
        return { controls: [...bar.querySelectorAll('.list-bar-controls button')].map(b => b.textContent.trim().replace(/\\s*\\(\\d+\\)$/, '')), results: st && !st.hidden ? bar.querySelector('.mb-search-results').textContent : null, chips: [...bar.querySelectorAll('.list-bar-chip-text')].map(e => e.textContent),
            rows: rows.length, unique: new Set(ids).size === ids.length, marks: c.querySelectorAll('mark.mb-hit').length, matchRows: c.querySelectorAll('.mb-message-match, .mb-message-current').length, current: cur && Number(cur.dataset.messageId),
            curVisible: cur ? (() => { const a = cur.getBoundingClientRect(), b = c.getBoundingClientRect(); return a.top >= b.top - 2 && a.bottom <= b.bottom + 2; })() : null, focusOnCurrent: !!cur && document.activeElement === cur,
            steps: [...bar.querySelectorAll('button[data-step]')].map(b => b.dataset.step + (b.disabled ? ':off' : ':on')).join(), firstId: rows.length ? Number(rows[0].dataset.messageId) : null, atTop: c.scrollTop < 4, atBottom: c.scrollHeight - c.scrollTop - c.clientHeight < 6 }; })()`);
    const onScreen = () => settle(`(() => { const c = document.getElementById('mb-messages'); const m = c.querySelector(':scope > .mb-message-current'); if (!m) return false; const a = m.getBoundingClientRect(), b = c.getBoundingClientRect(); return a.top >= b.top - 2 && a.bottom <= b.bottom + 2 && document.activeElement === m; })()`, 'the match on screen');
    const type = async (sel, text) => { await ev(`document.querySelector(${JSON.stringify(sel)}).focus(); true`); await Input.insertText({ text }); };
    const click = sel => ev(`document.querySelector(${JSON.stringify(sel)}).click(); true`);
    const ratio = theme => ev(`(() => { ${themeJs(theme)};
        const px = c => { const x = document.createElement('canvas').getContext('2d'); x.fillStyle = '#000'; x.fillStyle = c; x.fillRect(0, 0, 1, 1); return [...x.getImageData(0, 0, 1, 1).data].slice(0, 3); };
        const L = ([r, g, bl]) => [r, g, bl].map(v => v / 255).map(v => v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4).reduce((a, v, i) => a + v * [0.2126, 0.7152, 0.0722][i], 0);
        const worst = [...document.querySelectorAll('#mb-messages mark.mb-hit')].slice(0, 6).map(m => { const cs = getComputedStyle(m); const a = L(px(cs.color)), b = L(px(cs.backgroundColor)); return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05); });
        document.documentElement.removeAttribute('data-theme'); return worst.length ? Math.min(...worst) : 0; })()`);

    try {
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window._coralSetLiveSessions === 'function'`, 'the app hooks', { timeout: 10000 });
        await ev(`(async () => {
            const msgs = ${JSON.stringify(ALL)};
            for (let i = 0; i < msgs.length; i++) {
                const r = await fetch('/api/board/${BOARD}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: msgs[i].from, content: msgs[i].content }) });
                if (!r.ok) throw new Error('seed ' + i + ': ' + await r.text());
            }
            return true; })()`);
        await ev(`window.selectBoardProject(${JSON.stringify(BOARD)}); true`);
        await settle(`document.querySelectorAll('#mb-messages > .mb-message').length >= 50`, 'the latest page');

        let s = await snap();
        check('the tools row has Filters, First and Latest; no results line yet', s.controls.join() === 'Filters,First,Latest' && s.results === null, JSON.stringify([s.controls, s.results]));
        // The retry total: messages with the word retry in any form (the code messages hold it too).
        await type('#bm-search', 'retry');
        await settle(`document.querySelector('#mb-tools .mb-search-results')?.textContent.startsWith('1 of ')`, 'the first result');
        s = await snap();
        check('a search finds every match of the whole board and is on the latest (1 of N)', s.results === `1 of ${EXPECT.retry} matches`, s.results + ' expected ' + EXPECT.retry);
        check('the match is marked, visible and holds the focus; matching rows are marked too', s.marks > 0 && s.curVisible === true && s.focusOnCurrent && s.matchRows >= 1, JSON.stringify([s.marks, s.curVisible, s.focusOnCurrent, s.matchRows]));
        check('there is no chip for the search text; the newer arrow is off at 1', s.chips.length === 0 && /newer:off/.test(s.steps) && /older:on/.test(s.steps), JSON.stringify([s.chips, s.steps]));
        const first = s.current;
        check('the marks read at 4.5:1 or more in the dark and the light theme', (await ratio('dark')) >= 4.5 && (await ratio('light')) >= 4.5, JSON.stringify([await ratio('dark'), await ratio('light')]));

        await click('.mb-search-step[data-step=older]');
        await settle(`document.querySelector('#mb-tools .mb-search-results').textContent.startsWith('2 of ')`, 'the second match');
        await onScreen(); s = await snap();
        check('the up arrow goes to the next older match (2 of N)', s.current < first && s.curVisible === true && s.focusOnCurrent, JSON.stringify([first, s.current]));
        await click('.mb-search-step[data-step=newer]');
        await settle(`document.querySelector('#mb-tools .mb-search-results').textContent.startsWith('1 of ')`, 'back to the first');
        s = await snap();
        check('the down arrow goes back to the newer one', s.current === first, JSON.stringify([first, s.current]));

        // Walk older until a match older than the loaded page: the pages between are loaded.
        const rowsBefore = s.rows;
        for (let i = 0; i < 20; i++) { await click('.mb-search-step[data-step=older]'); await settle(`document.querySelector('#mb-tools .mb-search-results').textContent.startsWith('${i + 2} of ')`, 'match ' + (i + 2)); }
        await onScreen(); s = await snap();
        check('a match older than the loaded page loads the pages between, each message once, and lands on it', s.rows > rowsBefore && s.unique && s.curVisible === true && s.focusOnCurrent, JSON.stringify([rowsBefore, s.rows, s.unique, s.curVisible]));

        // From and the search together
        await click('#mb-tools .list-bar-filter-btn');
        await type('#bm-from', 'dev-2');
        await settle(`document.querySelector('#mb-tools .mb-search-results')?.textContent === '1 of ${EXPECT.retryDev2} matches'`, 'retry from dev-2');
        s = await snap();
        check('retry from dev-2: the matches of both, with a From chip', s.chips.join() === 'From: dev-2' && s.current !== null, JSON.stringify([s.results, s.chips]));
        // Mentions me alone (clear the text and the sender first)
        await click('#mb-tools .list-bar-clear-all');
        await settle(`document.querySelector('#mb-tools .mb-search-status').hidden`, 'cleared by Clear all');
        await click('#mb-tools .list-bar-filter-btn');
        await ev(`(() => { const k = document.getElementById('bm-mentions'); k.checked = true; k.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        await settle(`document.querySelector('#mb-tools .mb-search-results')?.textContent === '1 of ${EXPECT.mentions} matches'`, 'mentions me');
        await onScreen(); s = await snap();
        check('Mentions me alone: every message that tags the operator, a chip, the rows marked, no text marks', s.chips.join() === 'Mentions me' && s.marks === 0 && s.matchRows >= 1 && s.curVisible === true, JSON.stringify([s.results, s.chips, s.marks, s.matchRows]));
        await click('.mb-search-clear');
        await settle(`document.querySelector('#mb-tools .mb-search-status').hidden`, 'cleared');
        s = await snap();
        check('Clear takes the search away: no results line, no marks, no marked rows', s.results === null && s.marks === 0 && s.matchRows === 0 && s.chips.length === 0, JSON.stringify([s.results, s.marks, s.matchRows]));

        // First and Latest
        await click('#mb-tools .mb-tool-btn');
        await settle(`document.getElementById('mb-messages').scrollTop < 4 && document.querySelectorAll('#mb-messages > .mb-message').length >= ${N}`, 'First: everything loaded, at the top');
        s = await snap();
        const ids = await ev(`[...document.querySelectorAll('#mb-messages > .mb-message')].map(r => Number(r.dataset.messageId))`);
        check('First loads every earlier page and lands on the first message of the board, each once', s.atTop && s.unique && ids[0] === Math.min(...ids) && s.rows >= N, JSON.stringify([s.rows, ids[0], s.atTop]));
        await ev(`[...document.querySelectorAll('#mb-tools .mb-tool-btn')].find(b => b.textContent === 'Latest').click(); true`);
        await settle(`(() => { const c = document.getElementById('mb-messages'); return c.scrollHeight - c.scrollTop - c.clientHeight < 6; })()`, 'Latest: the bottom');
        check('Latest is the bottom of the board', (await snap()).atBottom);

        // Nothing matching
        await type('#bm-search', 'zzzq-no-such');
        await settle(`document.querySelector('#mb-tools .mb-search-results')?.textContent === 'No messages match'`, 'no match');
        s = await snap();
        check('a search with no match says so and both arrows are off', /older:off,newer:off/.test(s.steps) && s.marks === 0, JSON.stringify([s.results, s.steps]));
        await click('.mb-search-clear');
        check('no page exceptions', exceptions.length === 0, exceptions.join(' | '));
    } catch (e) {
        check('test ran', false, e.stack || String(e));
    } finally {
        await removeBoards(ev, [BOARD]);
        await client.close();
    }
    const failed = results.filter(r => r === 'FAIL').length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
}

run().catch(e => { console.error(e); process.exit(1); });
