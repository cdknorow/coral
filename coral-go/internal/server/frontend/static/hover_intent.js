/**
 * Coordinates pointer intent between an editor anchor and a floating hover
 * card. Dismissal is delayed so the pointer can cross the small gap between
 * them, but only while it remains over one of those two regions.
 */
export class HoverIntent {
    constructor(onDismiss, {
        delay = 200,
        setTimer = setTimeout,
        clearTimer = clearTimeout,
    } = {}) {
        this.onDismiss = onDismiss;
        this.delay = delay;
        this.setTimer = setTimer;
        this.clearTimer = clearTimer;
        this.timer = null;
        this.overAnchor = false;
        this.overCard = false;
    }

    enterAnchor() {
        this.overAnchor = true;
        this.cancelDismiss();
    }

    leaveAnchor() {
        this.overAnchor = false;
        this.scheduleDismiss();
    }

    enterCard() {
        this.overCard = true;
        this.cancelDismiss();
    }

    leaveCard() {
        this.overCard = false;
        this.scheduleDismiss();
    }

    updateContainment(overAnchor, overCard) {
        this.overAnchor = overAnchor;
        this.overCard = overCard;
        if (overAnchor || overCard) this.cancelDismiss();
        else this.scheduleDismiss();
    }

    scheduleDismiss() {
        if (this.overAnchor || this.overCard || this.timer !== null) return;
        this.timer = this.setTimer(() => {
            this.timer = null;
            if (!this.overAnchor && !this.overCard) this.onDismiss();
        }, this.delay);
    }

    cancelDismiss() {
        if (this.timer === null) return;
        this.clearTimer(this.timer);
        this.timer = null;
    }

    dismissNow() {
        this.cancelDismiss();
        this.overAnchor = false;
        this.overCard = false;
        this.onDismiss();
    }

    reset() {
        this.cancelDismiss();
        this.overAnchor = false;
        this.overCard = false;
    }
}

export function isWithinHoverAnchor(anchor, x, y, radius = 18) {
    if (!anchor) return false;
    return Math.abs(x - anchor.x) <= radius && Math.abs(y - anchor.y) <= radius;
}

function containsPoint(rect, x, y) {
    return !!rect && x >= rect.left && x <= rect.right && y >= rect.top && y <= rect.bottom;
}

/**
 * Tracks the real pointer while a body-level hover card is active. Listener
 * lifetime is exactly the card lifetime, so stale enter/leave events cannot
 * leave either containment flag stuck.
 */
export class HoverInteractionController {
    constructor({
        onDismiss,
        onReplace,
        dismissDelay = 200,
        replaceDelay = 350,
        documentTarget = globalThis.document,
        windowTarget = globalThis.window,
        MutationObserverClass = globalThis.MutationObserver,
        setTimer = setTimeout,
        clearTimer = clearTimeout,
    }) {
        this.onDismiss = onDismiss;
        this.onReplace = onReplace;
        this.replaceDelay = replaceDelay;
        this.documentTarget = documentTarget;
        this.windowTarget = windowTarget;
        this.MutationObserverClass = MutationObserverClass;
        this.setTimer = setTimer;
        this.clearTimer = clearTimer;
        this.replaceTimer = null;
        this.active = null;
        this.observer = null;
        this.pending = null;
        this.pendingSequence = 0;
        this.intent = new HoverIntent(() => this._graceDismiss(), {
            delay: dismissDelay, setTimer, clearTimer,
        });
        this._pointerMove = event => this.updatePointer(event.clientX, event.clientY);
        this._pointerCancel = () => this.dismissNow();
        this._windowOut = event => {
            if (!event.relatedTarget) this.dismissNow();
        };
        this._scroll = event => {
            if (this.active?.card.contains?.(event.target)) return;
            this.dismissNow();
        };
        this._pendingMove = event => {
            if (this.pending) this.pending.pointer = { x: event.clientX, y: event.clientY };
        };
        this._pendingCancel = () => {
            const cancel = this.pending?.onCancel;
            this.stopPending();
            cancel?.();
        };
        this._pendingWindowOut = event => {
            if (!event.relatedTarget) this._pendingCancel();
        };
    }

    beginPending(pointer, onCancel) {
        this.stopPending();
        const token = ++this.pendingSequence;
        this.pending = { token, pointer, onCancel };
        this.documentTarget?.addEventListener('pointermove', this._pendingMove, true);
        this.documentTarget?.addEventListener('pointercancel', this._pendingCancel, true);
        this.documentTarget?.addEventListener('scroll', this._pendingCancel, true);
        this.windowTarget?.addEventListener('blur', this._pendingCancel);
        this.windowTarget?.addEventListener('pointerout', this._pendingWindowOut);
        return token;
    }

    finishPending(token) {
        if (this.pending?.token !== token) return null;
        const pointer = this.pending.pointer;
        this.stopPending();
        return pointer;
    }

    stopPending() {
        this.documentTarget?.removeEventListener('pointermove', this._pendingMove, true);
        this.documentTarget?.removeEventListener('pointercancel', this._pendingCancel, true);
        this.documentTarget?.removeEventListener('scroll', this._pendingCancel, true);
        this.windowTarget?.removeEventListener('blur', this._pendingCancel);
        this.windowTarget?.removeEventListener('pointerout', this._pendingWindowOut);
        this.pending = null;
    }

    activate({ anchor, card, editor, pointer = null }) {
        this.detach();
        this.cancelReplacement();
        this.active = { anchor, card, editor };
        this.documentTarget?.addEventListener('pointermove', this._pointerMove, true);
        this.documentTarget?.addEventListener('pointercancel', this._pointerCancel, true);
        this.windowTarget?.addEventListener('blur', this._pointerCancel);
        this.windowTarget?.addEventListener('pointerout', this._windowOut);
        this.documentTarget?.addEventListener('scroll', this._scroll, true);
        if (this.MutationObserverClass && this.documentTarget?.documentElement) {
            this.observer = new this.MutationObserverClass(() => {
                if (this.active && this.active.card.isConnected === false) this.dismissNow();
            });
            this.observer.observe(this.documentTarget.documentElement, { childList: true, subtree: true });
        }
        if (pointer) this.updatePointer(pointer.x, pointer.y);
        else this.intent.updateContainment(false, false);
    }

    updatePointer(x, y) {
        if (!this.active) return;
        const { anchor, card, editor } = this.active;
        if (card.isConnected === false) {
            this.dismissNow();
            return;
        }
        const overCard = card.isConnected !== false && containsPoint(card.getBoundingClientRect(), x, y);
        const overAnchor = !overCard && isWithinHoverAnchor(anchor, x, y);
        this.intent.updateContainment(overAnchor, overCard);
        if (overAnchor || overCard) {
            this.cancelReplacement();
        } else if (containsPoint(editor.getBoundingClientRect(), x, y)) {
            this.scheduleReplacement(x, y);
        } else {
            this.cancelReplacement();
        }
    }

    scheduleReplacement(x, y) {
        this.cancelReplacement();
        this.replaceTimer = this.setTimer(() => {
            this.replaceTimer = null;
            this.onReplace(x, y);
        }, this.replaceDelay);
    }

    cancelReplacement() {
        if (this.replaceTimer === null) return;
        this.clearTimer(this.replaceTimer);
        this.replaceTimer = null;
    }

    _graceDismiss() {
        this.detach();
        this.onDismiss();
    }

    dismissNow() {
        this.stopPending();
        this.cancelReplacement();
        this.detach();
        this.intent.reset();
        this.onDismiss();
    }

    detach() {
        this.documentTarget?.removeEventListener('pointermove', this._pointerMove, true);
        this.documentTarget?.removeEventListener('pointercancel', this._pointerCancel, true);
        this.windowTarget?.removeEventListener('blur', this._pointerCancel);
        this.windowTarget?.removeEventListener('pointerout', this._windowOut);
        this.documentTarget?.removeEventListener('scroll', this._scroll, true);
        this.observer?.disconnect();
        this.observer = null;
        this.active = null;
    }
}
