/* What the dashboard says about a team's git worktree when it is stopped (#6707, from UI Accessibility's #6702 audit). Coral never deletes a person's work without asking: a stop removes the worktree only
 * when it has no unsaved changes and keeps it otherwise, so the confirmation says that before, and the stop's answer says what happened after. No imports (the sidebar's menus and the tests ask it). */

/** The sentence a Kill Team confirmation adds for a team that works in a worktree: what a stop does to it. `branch` is the team's branch, when known. */
export function worktreeConfirmWords(branch) {
    return `Its worktree${branch ? ` (branch ${branch})` : ''} is removed if it has no unsaved changes; otherwise it is kept.`;
}

/** The kill answer's `worktree` ({ state: removed | kept | failed | none, path, branch, reason }) as words: { text, error } (error: said as an alert, the rest as a status), or null when there is nothing to say. */
export function worktreeAnswerWords(w) {
    if (!w || typeof w !== 'object' || !w.state || w.state === 'none') return null;
    const reason = typeof w.reason === 'string' ? w.reason.trim().replace(/[.\s]+$/, '') : '';
    const where = w.path ? ` It is at ${w.path}${w.branch ? `, on branch ${w.branch}` : ''}.` : (w.branch ? ` Its branch is ${w.branch}.` : '');
    if (w.state === 'removed') return { text: 'The team stopped and its worktree was removed.', error: false };
    if (w.state === 'kept') return { text: `The team stopped. Its worktree was kept${reason ? `: ${reason}` : ''}.${where}`, error: false };
    if (w.state === 'failed') return { text: `The team stopped, but its worktree could not be removed${reason ? `: ${reason}` : ''}.${where}`, error: true };
    return null;
}
