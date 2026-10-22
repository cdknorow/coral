/* Running agents whose hook programs are gone (#6089): the app they were started from was replaced or moved while they ran, so their hooks cannot run and Coral cannot confirm what they take.
 * The server lists them (hooks-status "missing"); each is flagged once, on its row. Restarting the agent starts it with the tools that are there now. */
import { state } from './state.js';
import { escapeAttr } from './utils.js';
import { redrawLiveList } from './list_hooks.js';

/** The chip on an agent's row: "Hooks can't run", with the fix in its tooltip. */
export function hooksMissingChip(s) {
    if (!s || !state.hooksMissing || !state.hooksMissing.has(s.session_id)) return '';
    const why = "Coral can't reach this agent's hooks, so it can't confirm messages you send it. Reopen Coral or restart the agent.";
    return `<span class="hooks-missing-chip" role="img" aria-label="${escapeAttr("Hooks can't run. " + why)}" title="${escapeAttr(why)}">Hooks can't run</span>`;
}

/** The server's list of agents without hooks: the rows are drawn again only when the set changes. */
export function setHooksMissing(agents) {
    const next = new Set((agents || []).map(a => a.session_id));
    const now = state.hooksMissing || new Set();
    if (next.size === now.size && [...next].every(id => now.has(id))) return;
    state.hooksMissing = next;
    redrawLiveList();
}
