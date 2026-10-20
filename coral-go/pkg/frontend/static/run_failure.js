/* A background run that failed because the agent's CLI is not signed in (#5469, #5471): the server fails the run at once with a fixed `error_code` and the plain
   `error_msg`, and the history, the run's header and the job's banner say what to do. A run with no such code is shown as before. The table is the one place
   that names the server's words. */
import { escapeHtml } from './utils.js';

const CLI = { claude: 'Claude', codex: 'Codex', gemini: 'Gemini' };

/** { text, short, hint } for a run record that failed for want of a sign-in, else null (a stranger code is not one). */
export function runFailure(rec) {
    if (!rec || rec.error_code !== 'not_signed_in') return null;
    const cli = Object.hasOwn(CLI, rec.agent_type) ? rec.agent_type : 'claude';
    return { text: `${CLI[cli]} is not signed in`, short: 'not signed in', hint: `Sign in with ${cli} in a terminal, then run this again.` };
}

/** The line shown under a failed run: the words and what to do. */
export function runFailureHtml(rec) {
    const f = runFailure(rec);
    return f ? `<span class="run-failure-text">${escapeHtml(f.text)}.</span> <span class="run-failure-hint">${escapeHtml(f.hint)}</span>` : '';
}

/** A short tag after a status in a table row ("failed (not signed in)"), or ''. */
export function runFailureTag(rec) {
    const f = runFailure(rec);
    return f ? ` <span class="run-failure-tag">(${escapeHtml(f.short)})</span>` : '';
}
