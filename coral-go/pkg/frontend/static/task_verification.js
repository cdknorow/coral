/* A finished task's verification state (#4564), as the server computes it (verification: {state, by, at}): Verified (reviewers or a gate confirmed it), Evidence attached
 * (it has evidence, nothing confirmed it) or Unverified (the agent marked it done, nothing else). A label for the Operator, never a blocker. Pure: no imports, so it can be
 * tested on its own; tasks.js draws the badge from what this returns. */

// (Object keys, not quoted strings: the icon-name lint reads a quoted word that is a Material icon name as an icon.)
const LABEL = { verified: 'Verified', evidence: 'Evidence attached', unverified: 'Unverified' };
const ICON = { verified: 'verified_user', evidence: 'description', unverified: 'help_outline' };
export const VERIFY_STATES = Object.keys(LABEL);

/** Who confirmed it, as words: the server's "by" may be a name, a list of names or nothing. */
function byText(by) {
    const list = (Array.isArray(by) ? by : [by]).filter(x => typeof x === 'string' && x.trim()).map(x => x.trim().slice(0, 80)).slice(0, 8);
    return list.join(', ');
}

/** {state, label, icon, detail, line, confirmed} for a finished task that carries a verification state, else null (an open task, or a server that does not send one: nothing is claimed).
 *  detail is the sentence for the tooltip and the accessible name; line is the one plain line the task detail shows; confirmed is true when reviewers or a gate verified it;
 *  date(iso) formats the time. */
export function verificationOf(task, date = x => String(x)) {
    if (!task || task.status !== 'completed') return null;
    const v = task.verification;
    if (!v || !VERIFY_STATES.includes(v.state)) return null;
    const by = byText(v.by);
    const at = typeof v.at === 'string' && v.at ? date(v.at) : '';
    const detail = v.state === 'evidence' ? `Evidence attached${by ? ` (${by})` : ''}; no reviewer or check has confirmed it`
        : v.state === 'unverified' ? 'Marked done by the agent; no reviewer or check confirmed it'
        : `Verified${by ? ` by ${by}` : ' by reviewers or a gate'}${at ? `, ${at}` : ''}`;
    const confirmed = !(v.state === 'evidence' || v.state === 'unverified');
    const line = confirmed ? detail : v.state === 'evidence' ? `Not independently verified (evidence attached${by ? `: ${by}` : ''})` : 'Not independently verified';
    return { state: v.state, label: LABEL[v.state], icon: ICON[v.state], detail, line, confirmed };
}
