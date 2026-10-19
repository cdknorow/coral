/* The jump to the latest message button (moved out of live_chat.js, #5065; no behaviour change). */
import { state } from './state.js';
import { registerActions } from './actions.js';

// ── Jump to latest (#4511, operator request) ────────────────────────────
// Reading higher up in an agent's Chat view, a round down-arrow button above the chat input jumps to the latest message and pins
// the view again, so new messages follow. It shows once you are more than about a screen from the bottom (or when messages came
// in while you read higher up: a dot on it, the number in its name) and goes at the bottom. Its live region stays displayed and only its text changes
// (a region shown already filled can go unannounced: UX on #4502); it reads like the board's "N new messages" pill (#4502).
let _jumpUnseen = 0;

let _jumpWired = false;

const JUMP_AT_BOTTOM_PX = 50; // the pin's own threshold (state.autoScroll, app.js)

function jumpParts() {
    const region = document.getElementById('chat-jump-latest');
    return region && { btn: region.querySelector('button'), dot: region.querySelector('.chat-jump-latest-dot'), status: region.querySelector('.chat-jump-latest-status') };
}

export function syncJumpToLatest() {
    const container = document.getElementById('live-history-messages');
    const p = jumpParts();
    if (!container || !p) return;
    if (!_jumpWired) {
        _jumpWired = true;
        container.addEventListener('scroll', syncJumpToLatest, { passive: true });
        if (!container.hasAttribute('tabindex')) container.tabIndex = -1; // focus after a jump on a touch screen
    }
    const dist = container.scrollHeight - container.scrollTop - container.clientHeight;
    const atBottom = dist < JUMP_AT_BOTTOM_PX;
    if (atBottom && _jumpUnseen) setJumpUnseen(0);
    p.btn.hidden = atBottom || !(dist > container.clientHeight || _jumpUnseen > 0);
}

function setJumpUnseen(n) {
    _jumpUnseen = n;
    const p = jumpParts();
    if (!p) return;
    const words = n ? `${n} new message${n === 1 ? '' : 's'}` : '';
    p.dot.hidden = !n; // a dot, no number: the count is in the name and the live region (the operator's restyle)
    const name = n ? `Jump to latest, ${words}` : 'Jump to latest';
    p.btn.setAttribute('aria-label', name);
    p.btn.title = name;
    if (p.status.textContent !== words) p.status.textContent = words; // announced politely when the count changes
}

// The messages a poll brings while the view is not pinned: what people and the agent wrote, not tool calls and results.
export function noteUnseenChat(messages) {
    if (state.autoScroll) return;
    const n = messages.filter(m => m.type === 'user' || m.type === 'assistant').length;
    if (n) setJumpUnseen(_jumpUnseen + n);
}

function jumpToLatest() {
    const container = document.getElementById('live-history-messages');
    if (!container) return;
    setJumpUnseen(0);
    state.autoScroll = true;
    // Pinned at the bottom when the smooth scroll gets there, so new messages follow. Messages that come in on the way make the
    // scroll stop short of the new bottom: once it stops (or after a second) it is finished at once. If you scroll away from the
    // bottom meanwhile, it lets go; and it watches for that second only, so nothing is left to pull the view down later.
    const pin = () => { container.scrollTop = container.scrollHeight; state.autoScroll = true; syncJumpToLatest(); };
    if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) pin();
    else {
        container.scrollTo({ top: container.scrollHeight, behavior: 'smooth' });
        const t0 = performance.now();
        let lastTop = container.scrollTop, lastHeight = container.scrollHeight, still = 0;
        const watch = () => {
            const top = container.scrollTop, height = container.scrollHeight;
            if (height - top - container.clientHeight < 2) { pin(); return; }
            // Scrolled back up: yours. A real drop in the top counts even when a message grew the height that frame
            // (#4601: the old height===lastHeight guard pinned the view against a scroll-up that landed on the same frame
            // as a reply); content above that shrinks can pull the top up on its own, so the drop must exceed that shrink.
            if (top < lastTop - 1 - Math.max(0, lastHeight - height)) return;
            still = top === lastTop ? still + 1 : 0;
            lastTop = top;
            lastHeight = height;
            if (still >= 3 || performance.now() - t0 > 1000) pin();
            else requestAnimationFrame(watch);
        };
        requestAnimationFrame(watch);
    }
    // Focus: the chat input on a desktop, to type next; on a touch screen the messages (the input would open the keyboard).
    const input = document.getElementById('command-input');
    const touch = window.matchMedia?.('(pointer: coarse)').matches;
    if (!touch && input && !input.disabled && input.getClientRects().length) input.focus({ preventScroll: true });
    else container.focus({ preventScroll: true });
}

// A prompt card's Open terminal (#3645), and the jump to the latest message (#4511).
registerActions({
    'terminal-view-open': () => window.showTerminalView(),
    'chat-jump-latest': () => jumpToLatest(),
});
