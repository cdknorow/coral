/* Codex's hooks question (#6409, Security's #6408 rank 4): a new Codex agent started by Coral stops on "Hooks need review: N hooks are new or changed" (Review hooks / Trust all and continue / Continue without
 * trusting) until the person answers it, and no message is typed into it (the server refuses one, 409 codex_hooks_review_pending). The chat shows the question as its card; the box under it says to answer that first, in
 * the card's words, so the first message is not written into a refusal. Nothing here answers for the person. */
import { isPhoneLayout } from './phone_layout.js';
import { state } from './state.js';
import { promptOptionsBySession } from './needs_input_card.js';
import { setHooksPlaceholder } from './composer_placeholder.js';

export const HOOKS_PLACEHOLDER = "Answer Codex's question about Coral's hooks first";
const HOOKS_PLACEHOLDER_PHONE = "Answer Codex's hooks question first"; // the full text is cut off mid-word in a phone's box

/** Whether the prompt read from the terminal is Codex's hooks-review question (the stable words, not the version or the count of hooks; the server matches the same). */
export function isHooksReview(screen) {
    if (!screen) return false;
    const text = [screen.question, ...(screen.options || []).map(o => o && o.label)].filter(Boolean).join(' ').toLowerCase().replace(/\s+/g, ' ');
    return text.includes('hook') && (text.includes('new or changed') || text.includes('trust all and continue') || text.includes('review hooks') || text.includes('hooks need review'));
}

/** The box says to answer the hooks question first while the open session's screen shows it, and has its own words back when it does not. */
export function syncHooksComposer(session) {
    const input = document.getElementById('command-input');
    if (!input) return;
    const cur = state.currentSession;
    const open = !!session && !!cur && cur.session_id === session.session_id && session.agent_type === 'codex' && isHooksReview(promptOptionsBySession.get(session.session_id));
    setHooksPlaceholder(input, open ? (isPhoneLayout() ? HOOKS_PLACEHOLDER_PHONE : HOOKS_PLACEHOLDER) : '');
    // The instruction is also a line that stays (a placeholder is gone once a person types, and is not read as the box's description): the box is described by it while it shows.
    const hint = document.getElementById('composer-hooks-hint');
    if (hint) {
        if (open) { if (hint.textContent !== HOOKS_PLACEHOLDER) hint.textContent = HOOKS_PLACEHOLDER; } else if (hint.textContent) hint.textContent = '';
        if (hint.hidden === open) hint.hidden = !open; // only on a change, as the held notice
        const ids = (input.getAttribute('aria-describedby') || '').split(/\s+/).filter(x => x && x !== hint.id);
        if (open) ids.push(hint.id);
        if (ids.length) input.setAttribute('aria-describedby', ids.join(' ')); else input.removeAttribute('aria-describedby');
    }
}
