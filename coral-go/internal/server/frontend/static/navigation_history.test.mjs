import test from 'node:test';
import assert from 'node:assert/strict';
import { NavigationHistory } from './navigation_history.js';

test('history caps entries and preserves navigation state', () => {
    const history = new NavigationHistory(2);
    history.record({ filepath: 'a.go', selection: { from: 1, to: 2 }, scrollTop: 3 });
    history.record({ filepath: 'b.go', selection: { from: 4, to: 5 }, scrollTop: 6 });
    history.record({ filepath: 'c.go', selection: { from: 7, to: 8 }, scrollTop: 9 });
    assert.equal(history.size, 2);
    assert.deepEqual(history.pop(), { filepath: 'c.go', selection: { from: 7, to: 8 }, scrollTop: 9 });
    assert.equal(history.pop().filepath, 'b.go');
    assert.equal(history.pop(), null);
});

test('recordHistory false does not create a recursive entry', () => {
    const history = new NavigationHistory();
    assert.equal(history.record({ filepath: 'a.go' }, { recordHistory: false }), false);
    assert.equal(history.size, 0);
});
