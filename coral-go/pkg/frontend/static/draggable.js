import { fitTerminal, setPanelDragging } from './xterm_renderer.js';

/**
 * Wire up a drag handle with pointer capture, rAF batching, and CSS
 * containment so every resizable panel behaves consistently.
 *
 * @param {HTMLElement} handle  - The drag-handle element.
 * @param {object}      opts
 * @param {string}      opts.cursor      - Cursor during drag ('col-resize' | 'row-resize').
 * @param {function}    opts.onMove      - (PointerEvent) => number|null. Compute and return
 *                                         the clamped value; return null to skip the frame.
 * @param {function}    opts.onFlush     - (value) => void. Apply the pending value to the DOM.
 * @param {function}    [opts.onEnd]     - (value|null) => void. Called once after the drag
 *                                         ends (after the final flush). Persist to localStorage here.
 * @param {function}    [opts.canDrag]   - () => boolean. If provided and returns false,
 *                                         the pointerdown is ignored.
 */
export function makeDraggable(handle, { cursor, onMove, onFlush, onEnd, canDrag }) {
    let dragging = false;
    let pending = null;
    let frame = null;

    const flush = () => {
        frame = null;
        if (pending !== null) onFlush(pending);
    };

    handle.addEventListener('pointerdown', (e) => {
        if (canDrag && !canDrag()) return;
        e.preventDefault();
        handle.setPointerCapture(e.pointerId);
        dragging = true;
        pending = null;
        setPanelDragging(true);
        handle.classList.add('dragging');
        document.body.style.cursor = cursor;
        document.body.style.userSelect = 'none';
    });

    handle.addEventListener('pointermove', (e) => {
        if (!dragging) return;
        const v = onMove(e);
        if (v !== null && v !== undefined) {
            pending = v;
            frame ??= requestAnimationFrame(flush);
        }
    });

    const finish = () => {
        if (!dragging) return;
        dragging = false;
        if (frame !== null) { cancelAnimationFrame(frame); frame = null; }
        if (pending !== null) onFlush(pending);
        setPanelDragging(false);
        handle.classList.remove('dragging');
        document.body.style.cursor = '';
        document.body.style.userSelect = '';
        if (onEnd) onEnd(pending);
        pending = null;
        fitTerminal();
    };
    handle.addEventListener('pointerup', finish);
    handle.addEventListener('lostpointercapture', finish);
}

/**
 * Like makeDraggable but uses event delegation on a parent — for handles
 * that are created dynamically (e.g. board chat resize).
 */
export function makeDelegatedDraggable(parent, handleSelector, opts) {
    parent.addEventListener('pointerdown', (e) => {
        const handle = e.target.closest(handleSelector);
        if (!handle) return;
        const resolved = opts.resolve ? opts.resolve(handle, e) : {};
        if (resolved === null) return;

        e.preventDefault();
        handle.setPointerCapture(e.pointerId);
        setPanelDragging(true);
        handle.classList.add('dragging');
        document.body.style.cursor = opts.cursor;
        document.body.style.userSelect = 'none';
        if (opts.onStart) opts.onStart(handle, resolved);

        let pending = null;
        let frame = null;
        const flush = () => {
            frame = null;
            if (pending !== null) opts.onFlush(pending, resolved);
        };

        const onMove = (ev) => {
            const v = opts.onMove(ev, resolved);
            if (v !== null && v !== undefined) {
                pending = v;
                frame ??= requestAnimationFrame(flush);
            }
        };
        const onFinish = () => {
            if (frame !== null) { cancelAnimationFrame(frame); frame = null; }
            if (pending !== null) opts.onFlush(pending, resolved);
            setPanelDragging(false);
            handle.classList.remove('dragging');
            document.body.style.cursor = '';
            document.body.style.userSelect = '';
            if (opts.onEnd) opts.onEnd(handle, pending, resolved);
            handle.removeEventListener('pointermove', onMove);
            handle.removeEventListener('pointerup', onFinish);
            handle.removeEventListener('lostpointercapture', onFinish);
            fitTerminal();
        };
        handle.addEventListener('pointermove', onMove);
        handle.addEventListener('pointerup', onFinish);
        handle.addEventListener('lostpointercapture', onFinish);
    });
}
