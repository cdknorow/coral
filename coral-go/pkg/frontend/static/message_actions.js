/* The per-message buttons of the agent chat (Copy, Save for later) as ONE Tab stop (#6727, the phone chat audit #6712): a long chat has a button pair on every bubble, so a keyboard or screen-reader user passed
 * dozens of same-named stops before the message box. Now the buttons are a roving group: one of them (the last message's, near the box, until the person moves) is the stop, and Up, Down, Left, Right, Home and End
 * move between them. Each Copy button says which message it copies. Touch and mouse are unchanged: every button is still on its bubble. Written only on a change (a chat that changes nothing writes nothing). */

const SEL = '.chat-copy-btn, .chat-save-btn';
const LABEL_CHARS = 40;

/** What a Copy button says: "Copy message: <the start of it>" (cut by characters, so an emoji is never split). */
function copyName(btn) {
    const text = (btn.closest('.chat-bubble')?.querySelector('.message-text')?.textContent || '').replace(/\s+/g, ' ').trim();
    const c = [...text];
    return text ? `Copy message: ${c.length > LABEL_CHARS ? c.slice(0, LABEL_CHARS).join('') + '…' : text}` : 'Copy message';
}

/** Wire one chat container. */
export function wireMessageActions(container) {
    if (!container || container.dataset.messageActions) return;
    container.dataset.messageActions = '1';
    let stop = null; // the button that is the Tab stop (the last one until the person moves)
    let queued = false;
    const buttons = () => [...container.querySelectorAll(SEL)];
    const sync = () => {
        queued = false;
        const all = buttons();
        if (!all.length) { stop = null; return; }
        if (!stop || !stop.isConnected) stop = all[all.length - 1];
        for (const b of all) {
            const want = b === stop ? '0' : '-1';
            if (b.getAttribute('tabindex') !== want) b.setAttribute('tabindex', want);
            if (b.classList.contains('chat-copy-btn')) { const n = copyName(b); if (b.getAttribute('aria-label') !== n) b.setAttribute('aria-label', n); }
        }
    };
    new MutationObserver(() => { if (!queued) { queued = true; requestAnimationFrame(sync); } }).observe(container, { childList: true, subtree: true });
    container.addEventListener('keydown', e => {
        const b = e.target.closest && e.target.closest(SEL);
        if (!b || e.altKey || e.ctrlKey || e.metaKey) return;
        const all = buttons(), i = all.indexOf(b);
        const to = e.key === 'ArrowUp' || e.key === 'ArrowLeft' ? Math.max(0, i - 1) : e.key === 'ArrowDown' || e.key === 'ArrowRight' ? Math.min(all.length - 1, i + 1) : e.key === 'Home' ? 0 : e.key === 'End' ? all.length - 1 : -1;
        if (to < 0) return;
        e.preventDefault();
        stop = all[to];
        sync();
        stop.focus();
    });
    sync();
}
