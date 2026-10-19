/* The first pages of chats kept for a quick return to an agent (moved out of live_chat.js, #5065; no behaviour change). */
import { onAccountForgotten } from './local_store.js';

// The chat page of each recently seen agent, kept so a switch back shows it at once and fetches only what came after
// (#3726), instead of the whole first page again. A page is kept for a few minutes after the agent was last on screen,
// for the last few agents, and only its newest messages (what the first page and the polls rendered).
const KEPT_TTL_MS = 5 * 60 * 1000;

const KEPT_MAX_AGENTS = 6;

const KEPT_MAX_MESSAGES = 600;

export const keptPages = new Map(); // session_id -> { messages, total, start, at }; the newest-used last

export function keepFirstPage(sessionId, messages, total, start) {
    keptPages.delete(sessionId);
    keptPages.set(sessionId, { messages: messages.slice(), total, start, at: Date.now() });
    while (keptPages.size > KEPT_MAX_AGENTS) keptPages.delete(keptPages.keys().next().value);
}

export function keepMore(sessionId, messages, total) {
    const kept = keptPages.get(sessionId);
    if (!kept) return;
    kept.messages.push(...messages);
    kept.total = total;
    kept.at = Date.now();
    if (kept.messages.length > KEPT_MAX_MESSAGES) {
        const drop = kept.messages.length - KEPT_MAX_MESSAGES;
        kept.messages.splice(0, drop);
        kept.start += drop;
    }
    keptPages.delete(sessionId);
    keptPages.set(sessionId, kept);
}

/** Forget every kept page (a suite that reuses an agent id with different content between its cases). */
export function clearKeptChatPages() { keptPages.clear(); }

// The account's chat text is not kept past sign-out or a change of Coral Hub account.
onAccountForgotten(clearKeptChatPages);

/** The kept page of a session seen within the last few minutes, or null (an older one is dropped). */
export function freshKeptPage(sessionId) {
    const kept = keptPages.get(sessionId);
    if (!kept) return null;
    if (Date.now() - kept.at > KEPT_TTL_MS || kept.messages.length === 0) { keptPages.delete(sessionId); return null; }
    return kept;
}
