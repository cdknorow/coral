import assert from 'node:assert/strict';
import test from 'node:test';

import { HoverInteractionController } from './hover_intent.js';

function clock() {
    let now = 0;
    let nextID = 1;
    const timers = new Map();
    return {
        setTimer(fn, delay) {
            const id = nextID++;
            timers.set(id, { fn, due: now + delay });
            return id;
        },
        clearTimer(id) { timers.delete(id); },
        advance(ms) {
            const target = now + ms;
            for (;;) {
                const next = [...timers].sort((a, b) => a[1].due - b[1].due)[0];
                if (!next || next[1].due > target) break;
                timers.delete(next[0]);
                now = next[1].due;
                next[1].fn();
            }
            now = target;
        },
    };
}

class Target {
    constructor() { this.listeners = new Map(); }
    addEventListener(type, fn) {
        if (!this.listeners.has(type)) this.listeners.set(type, new Set());
        this.listeners.get(type).add(fn);
    }
    removeEventListener(type, fn) { this.listeners.get(type)?.delete(fn); }
    fire(type, event = {}) {
        for (const fn of this.listeners.get(type) || []) fn(event);
    }
    count() {
        return [...this.listeners.values()].reduce((sum, listeners) => sum + listeners.size, 0);
    }
}

function element(rect) {
    return {
        isConnected: true,
        getBoundingClientRect: () => rect,
        contains(target) { return target === this; },
    };
}

function fixture() {
    const timer = clock();
    const documentTarget = new Target();
    documentTarget.documentElement = {};
    const windowTarget = new Target();
    const card = element({ left: 120, right: 320, top: 220, bottom: 360 });
    const editor = element({ left: 0, right: 600, top: 0, bottom: 500 });
    const dismissals = [];
    const replacements = [];
    const controller = new HoverInteractionController({
        onDismiss: () => {
            dismissals.push(true);
            card.isConnected = false;
        },
        onReplace: (x, y) => replacements.push([x, y]),
        documentTarget,
        windowTarget,
        MutationObserverClass: null,
        setTimer: timer.setTimer,
        clearTimer: timer.clearTimer,
    });
    controller.activate({
        anchor: { x: 100, y: 200 },
        card,
        editor,
        pointer: { x: 100, y: 200 },
    });
    return { card, controller, dismissals, documentTarget, editor, replacements, timer, windowTarget };
}

test('real pointer containment bridges the anchor-to-card gap', () => {
    const f = fixture();
    f.documentTarget.fire('pointermove', { clientX: 110, clientY: 215 });
    f.timer.advance(100);
    f.documentTarget.fire('pointermove', { clientX: 130, clientY: 230 });
    f.timer.advance(500);
    assert.equal(f.dismissals.length, 0);
});

test('leaving the card for blank editor dismisses then requests replacement', () => {
    const f = fixture();
    f.documentTarget.fire('pointermove', { clientX: 150, clientY: 250 });
    f.documentTarget.fire('pointermove', { clientX: 450, clientY: 300 });
    f.timer.advance(200);
    assert.equal(f.dismissals.length, 1);
    f.timer.advance(150);
    assert.deepEqual(f.replacements, [[450, 300]]);
});

test('leaving to surrounding UI dismisses without replacement', () => {
    const f = fixture();
    f.documentTarget.fire('pointermove', { clientX: 700, clientY: 300 });
    f.timer.advance(200);
    assert.equal(f.dismissals.length, 1);
    f.timer.advance(500);
    assert.deepEqual(f.replacements, []);
});

test('blur and pointer cancellation dismiss immediately and remove listeners', () => {
    for (const eventType of ['blur', 'pointercancel']) {
        const f = fixture();
        const target = eventType === 'blur' ? f.windowTarget : f.documentTarget;
        target.fire(eventType);
        assert.equal(f.dismissals.length, 1);
        assert.equal(f.documentTarget.count() + f.windowTarget.count(), 0);
    }
});

test('removal while flagged inside is noticed on the next real pointer event', () => {
    const f = fixture();
    f.documentTarget.fire('pointermove', { clientX: 150, clientY: 250 });
    f.card.isConnected = false;
    f.documentTarget.fire('pointermove', { clientX: 150, clientY: 250 });
    assert.equal(f.dismissals.length, 1);
    assert.equal(f.documentTarget.count() + f.windowTarget.count(), 0);
});

test('editor scrolling dismisses, but scrolling inside the card remains usable', () => {
    const cardScroll = fixture();
    cardScroll.documentTarget.fire('scroll', { target: cardScroll.card });
    assert.equal(cardScroll.dismissals.length, 0);

    const editorScroll = fixture();
    editorScroll.documentTarget.fire('scroll', { target: editorScroll.editor });
    assert.equal(editorScroll.dismissals.length, 1);
});

test('explicit teardown cancels replacement and prevents resurrection', () => {
    const f = fixture();
    f.documentTarget.fire('pointermove', { clientX: 450, clientY: 300 });
    f.controller.dismissNow();
    f.timer.advance(1000);
    assert.equal(f.dismissals.length, 1);
    assert.deepEqual(f.replacements, []);
});

test('leaving the browser window dismisses immediately', () => {
    const f = fixture();
    f.windowTarget.fire('pointerout', { relatedTarget: null });
    assert.equal(f.dismissals.length, 1);
});

test('unknown pointer position on activation does not presume anchor containment', () => {
    const timer = clock();
    const documentTarget = new Target();
    documentTarget.documentElement = {};
    const windowTarget = new Target();
    const dismissals = [];
    const controller = new HoverInteractionController({
        onDismiss: () => dismissals.push(true),
        onReplace: () => {},
        documentTarget,
        windowTarget,
        MutationObserverClass: null,
        setTimer: timer.setTimer,
        clearTimer: timer.clearTimer,
    });
    controller.activate({
        anchor: { x: 100, y: 200 },
        card: element({ left: 120, right: 320, top: 220, bottom: 360 }),
        editor: element({ left: 0, right: 600, top: 0, bottom: 500 }),
    });
    timer.advance(200);
    assert.equal(dismissals.length, 1);
});

test('pending tracking preserves a stationary pointer on the anchor', () => {
    const f = fixture();
    f.controller.dismissNow();
    f.card.isConnected = true;
    const token = f.controller.beginPending({ x: 100, y: 200 }, () => {});
    const pointer = f.controller.finishPending(token);
    f.controller.activate({
        anchor: { x: 100, y: 200 }, card: f.card, editor: f.editor, pointer,
    });
    f.timer.advance(1000);
    assert.equal(f.dismissals.length, 1, 'only the fixture reset should dismiss');
});

test('pending tracking catches movement elsewhere before the card response', () => {
    const f = fixture();
    f.controller.dismissNow();
    f.card.isConnected = true;
    const token = f.controller.beginPending({ x: 100, y: 200 }, () => {});
    f.documentTarget.fire('pointermove', { clientX: 700, clientY: 300 });
    const pointer = f.controller.finishPending(token);
    f.controller.activate({
        anchor: { x: 100, y: 200 }, card: f.card, editor: f.editor, pointer,
    });
    f.timer.advance(200);
    assert.equal(f.dismissals.length, 2);
    assert.equal(f.documentTarget.count() + f.windowTarget.count(), 0);
});

test('pending tracking recognizes movement into the eventual card bounds', () => {
    const f = fixture();
    f.controller.dismissNow();
    f.card.isConnected = true;
    const token = f.controller.beginPending({ x: 100, y: 200 }, () => {});
    f.documentTarget.fire('pointermove', { clientX: 150, clientY: 250 });
    const pointer = f.controller.finishPending(token);
    f.controller.activate({
        anchor: { x: 100, y: 200 }, card: f.card, editor: f.editor, pointer,
    });
    f.timer.advance(1000);
    assert.equal(f.dismissals.length, 1, 'only the fixture reset should dismiss');
});

test('stale pending tokens cannot tear down newer transient listeners', () => {
    const f = fixture();
    f.controller.dismissNow();
    const stale = f.controller.beginPending({ x: 100, y: 200 }, () => {});
    const current = f.controller.beginPending({ x: 101, y: 201 }, () => {});
    assert.equal(f.controller.finishPending(stale), null);
    assert.ok(f.documentTarget.count() + f.windowTarget.count() > 0);
    assert.deepEqual(f.controller.finishPending(current), { x: 101, y: 201 });
    assert.equal(f.documentTarget.count() + f.windowTarget.count(), 0);
});
