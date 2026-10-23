/* The per-message buttons of the agent chat (Copy, Save for later) as ONE Tab stop (#6727, the phone chat audit #6712): a long chat has a button pair on every bubble, so a keyboard or screen-reader user passed
 * dozens of same-named stops before the message box. Now the buttons are a roving group: one of them (the last message's, near the box, until the person moves) is the stop, and Up, Down, Left, Right, Home and End
 * move between them; the stop is described by a line that says so. Each Copy button says which message it copies. Touch and mouse are unchanged: every button is still on its bubble. Work is done once per new
 * button (a streaming chat of a thousand messages re-reads nothing: a mutation that adds no button does nothing), and written only on a change. */

const SEL = '.chat-copy-btn, .chat-save-btn';
const LABEL_CHARS = 40;
const HINT = "Message buttons: use the arrow keys to move between messages, Home and End for the first and last.";

/** What a Copy button says: "Copy message: <the start of it>" (cut by characters, so an emoji is never split); '' when its message has no text yet. */
function copyName(btn) {
    const text = (btn.closest('.chat-bubble')?.querySelector('.message-text')?.textContent || '').replace(/\s+/g, ' ').trim();
    if (!text) return '';
    const c = [...text];
    return `Copy message: ${c.length > LABEL_CHARS ? c.slice(0, LABEL_CHARS).join('') + '…' : text}`;
}

/** Wire one chat container. */
export function wireMessageActions(container) {
    if (!container || container.dataset.messageActions) return;
    container.dataset.messageActions = '1';
    const hint = document.createElement('span'); // the group's name: said with the stop, so a person is told the arrows move between the messages
    hint.id = 'message-actions-hint'; hint.className = 'sr-only'; hint.textContent = HINT;
    container.before(hint);
    let stop = null;       // the button that is the Tab stop
    let tabbed = null;     // the one that carries tabindex 0 now
    let moved = false;     // the person has moved with the arrows: the stop stays where they put it
    let queued = false;
    const named = new WeakSet(); // buttons that have had their one-time work (tabindex -1, their name)
    const buttons = () => [...container.querySelectorAll(SEL)];
    const sync = () => {
        queued = false;
        const all = buttons();
        if (!all.length) { stop = tabbed = null; return; }
        for (const b of all) {
            if (!named.has(b)) {
                if (b !== tabbed && b.getAttribute('tabindex') !== '-1') b.setAttribute('tabindex', '-1');
                if (b.classList.contains('chat-copy-btn')) {
                    const n = copyName(b);
                    if (n) { if (b.getAttribute('aria-label') !== n) b.setAttribute('aria-label', n); named.add(b); } // a message with no words yet is looked at again
                } else named.add(b);
            }
        }
        const next = moved && stop && stop.isConnected ? stop : all[all.length - 1];
        if (next !== tabbed) {
            if (tabbed && tabbed.isConnected) { tabbed.setAttribute('tabindex', '-1'); tabbed.removeAttribute('aria-describedby'); }
            next.setAttribute('tabindex', '0'); next.setAttribute('aria-describedby', hint.id);
            tabbed = next;
        }
        stop = next;
    };
    new MutationObserver(records => {
        if (queued) return;
        let work = false;
        for (const r of records) {
            for (const n of r.addedNodes) if (n.nodeType === 1 && (n.matches(SEL) || n.querySelector(SEL))) { work = true; break; }
            if (!work) for (const n of r.removedNodes) if (n.nodeType === 1 && (n === tabbed || n.contains(tabbed))) { work = true; break; }
            if (work) break;
        }
        if (work) { queued = true; requestAnimationFrame(sync); }
    }).observe(container, { childList: true, subtree: true });
    container.addEventListener('keydown', e => {
        const b = e.target.closest && e.target.closest(SEL);
        if (!b || e.altKey || e.ctrlKey || e.metaKey) return;
        const all = buttons(), i = all.indexOf(b);
        const to = e.key === 'ArrowUp' || e.key === 'ArrowLeft' ? Math.max(0, i - 1) : e.key === 'ArrowDown' || e.key === 'ArrowRight' ? Math.min(all.length - 1, i + 1) : e.key === 'Home' ? 0 : e.key === 'End' ? all.length - 1 : -1;
        if (to < 0) return;
        e.preventDefault();
        moved = true; stop = all[to];
        sync();
        stop.focus();
    });
    sync();
}
