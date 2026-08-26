import assert from 'node:assert/strict';
import test from 'node:test';

import { HoverIntent, isWithinHoverAnchor } from './hover_intent.js';

function fakeClock() {
    let nextID = 1;
    const timers = new Map();
    return {
        setTimer(fn) {
            const id = nextID++;
            timers.set(id, fn);
            return id;
        },
        clearTimer(id) { timers.delete(id); },
        flush() {
            const pending = [...timers.values()];
            timers.clear();
            pending.forEach(fn => fn());
        },
    };
}

function fixture() {
    const clock = fakeClock();
    let dismissals = 0;
    const intent = new HoverIntent(() => { dismissals++; }, {
        delay: 200,
        setTimer: clock.setTimer,
        clearTimer: clock.clearTimer,
    });
    return { clock, intent, dismissals: () => dismissals };
}

test('crossing from the editor anchor into the card cancels dismissal', () => {
    const { clock, intent, dismissals } = fixture();
    intent.enterAnchor();
    intent.leaveAnchor();
    intent.enterCard();
    clock.flush();
    assert.equal(dismissals(), 0);
});

test('re-entering the editor during the grace period cancels dismissal', () => {
    const { clock, intent, dismissals } = fixture();
    intent.enterAnchor();
    intent.leaveAnchor();
    intent.enterAnchor();
    clock.flush();
    assert.equal(dismissals(), 0);
});

test('leaving both the anchor and card dismisses after the grace period', () => {
    const { clock, intent, dismissals } = fixture();
    intent.enterCard();
    intent.leaveCard();
    assert.equal(dismissals(), 0);
    clock.flush();
    assert.equal(dismissals(), 1);
});

test('Escape-style immediate dismissal bypasses the grace period', () => {
    const { clock, intent, dismissals } = fixture();
    intent.enterCard();
    intent.dismissNow();
    clock.flush();
    assert.equal(dismissals(), 1);
});

test('cleanup resets pending dismissal without firing it later', () => {
    const { clock, intent, dismissals } = fixture();
    intent.enterAnchor();
    intent.leaveAnchor();
    intent.reset();
    clock.flush();
    assert.equal(dismissals(), 0);
});

test('the editor anchor is bounded instead of making the whole editor sticky', () => {
    const anchor = { x: 100, y: 200 };
    assert.equal(isWithinHoverAnchor(anchor, 112, 185), true);
    assert.equal(isWithinHoverAnchor(anchor, 119, 200), false);
    assert.equal(isWithinHoverAnchor(null, 100, 200), false);
});
