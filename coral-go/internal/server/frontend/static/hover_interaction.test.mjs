/*
 * Interaction-level tests for the hover card.
 *
 * hover_intent.test.mjs covers HoverIntent in isolation with a clock that
 * fires every pending timer at once. That cannot distinguish the 200ms
 * dismissal from the 350ms replacement, and the ordering between those two is
 * the whole behaviour: the card must survive the gap crossing, yet still be
 * replaced when the pointer settles on a different symbol.
 *
 * These tests drive the real HoverIntent through a time-ordered virtual clock,
 * wired to a harness that mirrors the editor handlers in changed_files.js
 * (mousemove/mouseleave/card enter/leave, _hoverTimer) and its two teardown
 * paths: _dismissLSPHoverCard for the grace-period dismissal, which must leave
 * a scheduled replacement alone, and _hideLSPHover for explicit teardown
 * (Escape, doc change, editor destruction), which must also cancel it.
 */

import assert from 'node:assert/strict';
import test from 'node:test';

import { HoverIntent, isWithinHoverAnchor } from './hover_intent.js';

// Virtual clock that fires timers in due-time order, so a 200ms timer set at
// t=0 runs before a 350ms timer set at t=0.
function orderedClock() {
    let now = 0;
    let nextID = 1;
    const timers = new Map();
    return {
        now: () => now,
        setTimer(fn, delay = 0) {
            const id = nextID++;
            timers.set(id, { fn, due: now + delay });
            return id;
        },
        clearTimer(id) { timers.delete(id); },
        advance(ms) {
            const target = now + ms;
            for (;;) {
                let next = null;
                for (const [id, timer] of timers) {
                    if (timer.due <= target && (next === null || timer.due < next[1].due)) {
                        next = [id, timer];
                    }
                }
                if (!next) break;
                const [id, timer] = next;
                timers.delete(id);
                now = timer.due;
                timer.fn();
            }
            now = target;
        },
        pending: () => timers.size,
    };
}

const DISMISS_DELAY = 200;
const REPLACE_DELAY = 350;

// hoverHarness mirrors the editor wiring in changed_files.js. Only the DOM and
// the LSP round trip are stubbed; the intent state machine is the real one.
function hoverHarness() {
    const clock = orderedClock();
    const shown = [];      // symbols a hover card was opened for
    const dismissals = [];  // times the card was torn down
    let card = null;        // stand-in for #lsp-hover-card
    let hoverTimer = null;  // the module's _hoverTimer
    let anchor = null;      // the module's _hoverAnchor

    // Mirrors the card-only grace-period teardown.
    function dismissHoverCard() {
        anchor = null;
        intent.reset();
        card = null;
        dismissals.push(clock.now());
    }

    // Mirrors explicit teardown such as Escape/editor destruction.
    function hideHover() {
        clock.clearTimer(hoverTimer);
        hoverTimer = null;
        dismissHoverCard();
    }

    const intent = new HoverIntent(dismissHoverCard, {
        delay: DISMISS_DELAY,
        setTimer: clock.setTimer,
        clearTimer: clock.clearTimer,
    });

    // Mirrors _showLSPHover() after the awaited response resolves.
    function showHover(symbol, x, y) {
        anchor = { x, y };
        card = { symbol };
        shown.push(symbol);
    }

    function scheduleShow(symbol, x, y) {
        hoverTimer = clock.setTimer(() => showHover(symbol, x, y), REPLACE_DELAY);
    }

    return {
        clock,
        shown,
        dismissals,
        cardOpen: () => card !== null,
        cardSymbol: () => card?.symbol ?? null,

        // Mirrors the mousemove domEventHandler.
        mouseMove(symbol, x, y) {
            if (card) {
                clock.clearTimer(hoverTimer);
                hoverTimer = null;
                if (isWithinHoverAnchor(anchor, x, y)) {
                    intent.enterAnchor();
                } else {
                    intent.leaveAnchor();
                    scheduleShow(symbol, x, y);
                }
                return;
            }
            intent.enterAnchor();
            clock.clearTimer(hoverTimer);
            scheduleShow(symbol, x, y);
        },

        // Mirrors the editor mouseleave handler.
        mouseLeaveEditor() {
            clock.clearTimer(hoverTimer);
            hoverTimer = null;
            intent.leaveAnchor();
        },

        // Mirrors the card's mouseenter/mouseleave listeners. The card is a
        // body-level element overlaying the editor, so the browser fires the
        // editor's mouseleave first; reproduce that ordering.
        enterCard() {
            clock.clearTimer(hoverTimer);
            intent.leaveAnchor();
            hoverTimer = null;
            intent.enterCard();
        },
        leaveCard() { intent.leaveCard(); },

        // Mirrors the Escape keydown handler.
        pressEscape() { hideHover(); },
    };
}

// openCardOn settles the pointer on a symbol and lets the card appear.
function openCardOn(harness, symbol, x, y) {
    harness.mouseMove(symbol, x, y);
    harness.clock.advance(REPLACE_DELAY);
    assert.equal(harness.cardSymbol(), symbol, `card for ${symbol} should be open`);
}

test('the card survives the pointer crossing the gap into it', () => {
    const harness = hoverHarness();
    openCardOn(harness, 'Greet', 100, 200);

    // Pointer leaves the anchor heading for the card, then arrives mid-grace.
    harness.mouseMove('other', 100, 240);
    harness.clock.advance(100);
    harness.enterCard();
    harness.clock.advance(1000);

    assert.equal(harness.cardOpen(), true, 'the card must stay open under the pointer');
    assert.equal(harness.cardSymbol(), 'Greet');
    assert.deepEqual(harness.dismissals, [], 'no dismissal should have fired');
});

test('leaving the card dismisses it after the grace period, not before', () => {
    const harness = hoverHarness();
    openCardOn(harness, 'Greet', 100, 200);
    harness.enterCard();
    harness.leaveCard();

    harness.clock.advance(DISMISS_DELAY - 1);
    assert.equal(harness.cardOpen(), true, 'dismissal must not fire early');
    harness.clock.advance(1);
    assert.equal(harness.cardOpen(), false, 'dismissal must fire at the grace interval');
});

test('rapid editor-card-editor movement neither flickers nor refetches', () => {
    const harness = hoverHarness();
    openCardOn(harness, 'Greet', 100, 200);
    const shownAfterOpen = harness.shown.length;

    for (let i = 0; i < 5; i++) {
        harness.mouseMove('gap', 100, 240);   // heading out toward the card
        harness.clock.advance(20);
        harness.enterCard();                  // arrives on the card
        harness.clock.advance(20);
        harness.leaveCard();                  // heads back toward the anchor
        harness.clock.advance(20);
        harness.mouseMove('Greet', 100, 205); // back inside the anchor
        harness.clock.advance(20);
    }
    harness.clock.advance(1000);

    assert.equal(harness.cardOpen(), true, 'the card flickered out during the shuttle');
    assert.deepEqual(harness.dismissals, [], 'no dismissal should have fired');
    assert.equal(harness.shown.length, shownAfterOpen,
        'no replacement hover request should have been issued');
});

test('settling on a different symbol replaces the card without extra pointer movement', () => {
    // This is the reported behaviour: move deliberately off the anchor, stop,
    // and the hover for the new symbol must appear on its own.
    const harness = hoverHarness();
    openCardOn(harness, 'Greet', 100, 200);

    harness.mouseMove('Farewell', 400, 200); // deliberate move elsewhere, then stop
    harness.clock.advance(2000);             // no further pointer events

    assert.equal(harness.cardSymbol(), 'Farewell',
        'the card should have been replaced by the new symbol without another mousemove');
});

test('the old card is torn down before the replacement appears', () => {
    const harness = hoverHarness();
    openCardOn(harness, 'Greet', 100, 200);

    harness.mouseMove('Farewell', 400, 200);
    harness.clock.advance(DISMISS_DELAY);
    assert.equal(harness.cardOpen(), false, 'the stale card must go at the grace interval');

    harness.clock.advance(2000);
    assert.equal(harness.cardSymbol(), 'Farewell', 'the replacement must still arrive');
});

test('Escape dismisses immediately and cancels any pending replacement', () => {
    const harness = hoverHarness();
    openCardOn(harness, 'Greet', 100, 200);

    harness.mouseMove('Farewell', 400, 200); // replacement now pending
    harness.pressEscape();

    assert.equal(harness.cardOpen(), false, 'Escape must close the card at once');
    harness.clock.advance(2000);
    assert.equal(harness.cardOpen(), false,
        'a pending replacement must not resurrect the card after Escape');
});

test('leaving the editor entirely dismisses the card and cancels the pending hover', () => {
    const harness = hoverHarness();
    openCardOn(harness, 'Greet', 100, 200);

    harness.mouseMove('Farewell', 400, 200);
    harness.mouseLeaveEditor();
    harness.clock.advance(2000);

    assert.equal(harness.cardOpen(), false, 'the card must not persist after leaving the editor');
});
