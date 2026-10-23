/* What a screen reader hears when an agent replies in the Chat view (#6213): once, when the turn is done, "<agent> replied: <the start of it>" in one polite status that stays in the page. Not once per
 * message or per streamed step: the replies of a turn are remembered and the last one is said when the Working row goes (the agent stopped working), or after a quiet moment when there was no Working row. */
const QUIET_MS = 2500;
const pending = new Map(); // session_id -> { who, text }
let timer = 0, flip = false;

const words = (t, max = 120) => {
    const s = String(t || '').replace(/[`*_#>]/g, '').replace(/\s+/g, ' ').trim();
    if (s.length <= max) return s;
    const cut = s.lastIndexOf(' ', max);
    return s.slice(0, cut > max / 2 ? cut : max) + '…';
};

/** Say `message` once to a screen reader (the same words twice are still a change). */
export function sayReply(message) {
    const el = document.getElementById('chat-reply-status');
    if (!el) return;
    flip = !flip;
    el.textContent = message + (flip ? '' : ' ');
}

/** The agent said something in the chat: remember it as this turn's reply, and say it after a quiet moment if no Working row is on screen to end the turn. */
export function noteReplyText(session, msg) {
    const text = msg && (msg.text || msg.content);
    if (!session || !session.session_id || typeof text !== 'string' || !text.trim()) return;
    pending.set(session.session_id, { who: session.display_name || session.name || 'The agent', text });
    clearTimeout(timer);
    timer = setTimeout(() => { // timer-ok: one quiet-period timer, replaced by the next reply
        if (!document.querySelector('#live-history-messages > .chat-working')) flushReply(session.session_id);
    }, QUIET_MS);
}

/** The turn is over (the Working row went): say the reply, once. */
export function flushReply(sessionId) {
    const p = pending.get(sessionId);
    if (!p) return;
    pending.delete(sessionId);
    clearTimeout(timer);
    sayReply(`${p.who} replied: ${words(p.text)}`);
}

/** Forget what is waiting to be said (another agent was opened). */
export function forgetReplies() { pending.clear(); clearTimeout(timer); }
