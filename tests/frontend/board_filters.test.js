// The message board's filters (#4771), on their own: what is accepted, how they become the search route's parameters, the chips, the results line.
//   node board_filters.test.js
const assert = require('node:assert/strict');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

(async () => {
    const m = await import(pathToFileURL(path.resolve(__dirname, '../../coral-go/pkg/frontend/static/board_filters.js')).href);

    // clean: strings are cut and stripped of control characters; dates are real days and put the right way round; mentions is a real boolean.
    assert.deepEqual(m.clean(undefined), m.EMPTY);
    assert.deepEqual(m.clean({ q: 5, from: {}, mentions: 'yes', since: '2026-13-40', until: 'soon' }), m.EMPTY);
    assert.equal(m.clean({ q: 'a\u0000b\nc ' + 'x'.repeat(300) }).q.length, 200);
    assert.equal(m.clean({ from: ' Dev\tOne ' }).from, 'Dev One');
    assert.deepEqual(m.clean({ since: '2026-10-09', until: '2026-10-05' }), { ...m.EMPTY, since: '2026-10-05', until: '2026-10-09' });
    assert.equal(m.clean({ mentions: true }).mentions, true);

    // isFiltered: anything at all narrows; with nothing there is no search.
    assert.equal(m.isFiltered(m.EMPTY), false);
    for (const [k, v] of [['q', 'x'], ['from', 'Dev'], ['mentions', true], ['since', '2026-10-05'], ['until', '2026-10-05']]) assert.equal(m.isFiltered({ ...m.EMPTY, [k]: v }), true, k);

    // toParams: the server's names; newest first; Mentions me is the operator's name; the end day is exclusive (the next local midnight); a cursor only when given.
    assert.deepEqual(m.toParams(m.EMPTY), [['sort', 'newest'], ['limit', '50']]);
    const obj = pairs => Object.fromEntries(pairs);
    const full = obj(m.toParams({ q: 'plan', from: 'Dev', mentions: true, since: '2026-10-05', until: '2026-10-05' }, { me: 'Operator', limit: 20, cursor: 'c1' }));
    assert.deepEqual([full.q, full.from, full.mentions, full.sort, full.limit, full.cursor], ['plan', 'Dev', 'Operator', 'newest', '20', 'c1']);
    assert.match(full.since, /^\d{4}-\d{2}-\d{2}T/);
    assert.equal(Date.parse(full.until) - Date.parse(full.since) >= 23 * 3600e3 && Date.parse(full.until) - Date.parse(full.since) <= 25 * 3600e3, true, 'until is the day after since for one day');
    assert.equal('mentions' in obj(m.toParams({ ...m.EMPTY, mentions: true })), false, 'no operator name: no mentions filter');
    assert.equal('cursor' in obj(m.toParams({ ...m.EMPTY, q: 'x' })), false);

    // chips: no chip for the search text (it is in the box); the dates are one chip; clearing one.
    assert.deepEqual(m.chips(m.EMPTY), []);
    assert.deepEqual(m.chips({ ...m.EMPTY, q: 'plan' }), []);
    assert.deepEqual(m.chips({ q: 'plan', from: 'Dev', mentions: true, since: '2026-10-05', until: '2026-10-09' }).map(c => c.label), ['From: Dev', 'Mentions me', '2026-10-05 to 2026-10-09']);
    assert.deepEqual(m.chips({ ...m.EMPTY, since: '2026-10-05' }).map(c => c.label), ['Since 2026-10-05']);
    assert.deepEqual(m.chips({ ...m.EMPTY, until: '2026-10-05' }).map(c => c.label), ['Until 2026-10-05']);
    assert.deepEqual(m.without({ ...m.EMPTY, since: 'a', until: 'b', from: 'Dev' }, 'dates'), { ...m.EMPTY, from: 'Dev' });
    assert.deepEqual(m.without({ ...m.EMPTY, mentions: true }, 'mentions'), m.EMPTY);
    assert.deepEqual(m.without({ ...m.EMPTY, from: 'Dev', q: 'x' }, 'from'), { ...m.EMPTY, q: 'x' });

    // the results line: the match on screen of all of them.
    assert.equal(m.resultsText(0, 74), '1 of 74 matches');
    assert.equal(m.resultsText(2, 3), '3 of 3 matches');
    assert.equal(m.resultsText(0, 1), '1 of 1 match');
    assert.equal(m.resultsText(0, 0), 'No messages match');
    console.log('board_filters: ok');
})().catch(e => { console.error(e); process.exit(1); });
