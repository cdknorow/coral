/*
 * Behavioural coverage for the navigation back stack.
 *
 * navigation_history.test.mjs (Lead-owned) covers the cap and the
 * recordHistory:false guard. This file covers the rest of what the back button
 * depends on: multi-hop ordering, selection/scroll restoration, the empty-stack
 * path, eviction direction at the real 50-entry limit, and the back-navigation
 * round trip that must not re-push what it just restored.
 */

import test from 'node:test';
import assert from 'node:assert/strict';
import { NavigationHistory } from './navigation_history.js';

// entryFor mirrors the shape changed_files.js records on openWorkspaceFile.
function entryFor(filepath, from = 0, to = 0, scrollTop = 0) {
    return { filepath, mode: 'edit', selection: { from, to }, scrollTop };
}

test('pop returns entries newest first across several hops', () => {
    const history = new NavigationHistory();
    history.record(entryFor('a.go'));
    history.record(entryFor('b.go'));
    history.record(entryFor('c.go'));

    assert.equal(history.size, 3);
    assert.equal(history.pop().filepath, 'c.go');
    assert.equal(history.pop().filepath, 'b.go');
    assert.equal(history.pop().filepath, 'a.go');
    assert.equal(history.size, 0);
});

test('selection and scroll position survive the round trip', () => {
    const history = new NavigationHistory();
    history.record(entryFor('pkg/store/db.go', 128, 143, 640));

    const restored = history.pop();
    assert.equal(restored.filepath, 'pkg/store/db.go');
    assert.equal(restored.mode, 'edit');
    assert.deepEqual(restored.selection, { from: 128, to: 143 },
        'the caret range must come back exactly, or back navigation lands in the wrong place');
    assert.equal(restored.scrollTop, 640);
});

test('a null selection is preserved rather than coerced', () => {
    // changed_files.js records selection: null when there is no editor view.
    const history = new NavigationHistory();
    history.record({ filepath: 'a.go', mode: 'preview', selection: null, scrollTop: 0 });
    assert.equal(history.pop().selection, null);
});

test('popping an empty history returns null and leaves it usable', () => {
    const history = new NavigationHistory();
    assert.equal(history.pop(), null);
    assert.equal(history.size, 0);

    // The back button is enabled off `size`, so this must stay coherent.
    history.record(entryFor('a.go'));
    assert.equal(history.size, 1);
    assert.equal(history.pop().filepath, 'a.go');
    assert.equal(history.pop(), null);
});

test('back navigation does not re-push the entry it just restored', () => {
    // navigateWorkspaceBack pops, then reopens with {...previous, recordHistory:false}.
    const history = new NavigationHistory();
    history.record(entryFor('a.go'));
    history.record(entryFor('b.go'));

    const previous = history.pop();
    history.record(entryFor('b.go'), { ...previous, recordHistory: false });

    assert.equal(history.size, 1, 'going back must not grow the stack');
    assert.equal(history.pop().filepath, 'a.go',
        'a second back must reach the entry before it, not bounce between two files');
});

test('omitted or unrelated options still record', () => {
    const history = new NavigationHistory();
    assert.equal(history.record(entryFor('a.go')), true);
    assert.equal(history.record(entryFor('b.go'), {}), true);
    assert.equal(history.record(entryFor('c.go'), { recordHistory: true }), true);
    assert.equal(history.record(entryFor('d.go'), { mode: 'edit' }), true,
        'an unrelated option must not suppress recording');
    assert.equal(history.size, 4);
});

test('a missing entry is rejected without corrupting the stack', () => {
    const history = new NavigationHistory();
    history.record(entryFor('a.go'));

    assert.equal(history.record(null), false);
    assert.equal(history.record(undefined), false);
    assert.equal(history.size, 1);
    assert.equal(history.pop().filepath, 'a.go');
});

test('the default stack caps at 50, evicting oldest first', () => {
    const history = new NavigationHistory();
    for (let i = 0; i < 55; i++) history.record(entryFor(`file${i}.go`));

    assert.equal(history.size, 50, 'the documented 50-entry cap must hold');
    assert.equal(history.pop().filepath, 'file54.go', 'the newest entry must survive');

    const remaining = [];
    while (history.size) remaining.push(history.pop().filepath);
    assert.equal(remaining[remaining.length - 1], 'file5.go',
        'eviction must drop the oldest entries, not the newest');
    for (const evicted of ['file0.go', 'file4.go']) {
        assert.ok(!remaining.includes(evicted), `${evicted} should have been evicted`);
    }
});

test('recorded entries are decoupled from later caller mutations', () => {
    const history = new NavigationHistory();
    const entry = entryFor('a.go', 1, 2, 10);
    history.record(entry);

    entry.filepath = 'mutated.go';
    entry.scrollTop = 999;

    const restored = history.pop();
    assert.equal(restored.filepath, 'a.go', 'the stored entry must not follow later mutations');
    assert.equal(restored.scrollTop, 10);
});
