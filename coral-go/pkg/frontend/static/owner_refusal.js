/* What to say when an owner-only team setting (the Health check, the Prompt inspection) is refused (#5323). Through Coral Hub the hub answers with its own word (not_found, mode_too_low, rate_limited,
 * owner_unverified, settings_unavailable, bad_request, too_large), and this computer answers with its code (owner_only, local_only, too_large, operator_identity_required) or only a status (403 below
 * Launch, 400, 413). Each of them is mapped to one short sentence of ours; anything else is "Unavailable". The server's own words are never shown. */

const TEXT = {
    not_found: 'This team was not found.',
    mode_too_low: 'This computer\'s access mode is too low for that. Raise it to Launch on the computer.',
    rate_limited: 'Too many tries. Wait a moment and try again.',
    owner_unverified: 'Only the account this computer is linked to can do that through Coral Hub.',
    settings_unavailable: 'These settings are not available through Coral Hub.',
    bad_request: 'That request was not accepted.',
    too_large: 'That is too large to save through Coral Hub.',
};
// This computer's own codes, said as the hub word that means the same (Backend's mapping, #5307)
const OWN = { owner_only: 'owner_unverified', local_only: 'bad_request', too_large: 'too_large', operator_identity_required: 'settings_unavailable' };
// With no code at all, the status says what it can
const BY_STATUS = { 400: 'bad_request', 403: 'mode_too_low', 404: 'not_found', 413: 'too_large', 429: 'rate_limited' };
const UNAVAILABLE = 'Unavailable';

const has = (o, k) => Object.hasOwn(o, k); // a code of "constructor" or "toString" is not one of ours

/** The key of the refusal: one of the TEXT keys, or '' when it is none of them. `body` is the parsed answer (or null). A code this build does not know is none of them (a hub reason we cannot read must not
 *  send the person to change a setting that is not the problem); the status alone speaks only for an answer that carries no code at all. */
export function refusalKey(status, body) {
    const b = body && typeof body === 'object' ? body : {};
    // the hub names its reason in `code`, or as the word of `error`; this computer's codes are mapped to the hub's word that means the same
    const word = typeof b.code === 'string' && b.code ? b.code : typeof b.error === 'string' && (has(TEXT, b.error) || has(OWN, b.error)) ? b.error : '';
    if (word) return has(OWN, word) ? OWN[word] : has(TEXT, word) ? word : '';
    if (typeof b.code === 'string') return ''; // an empty code says nothing, and no word of ours is in `error`: unknown
    return has(BY_STATUS, Number(status)) ? BY_STATUS[Number(status)] : '';
}

/** A plain validation message this computer's own server wrote for a 400 that has no code ("X is not a member of this team"): it tells the person what to fix, and is shown as it is. */
const ownValidation = (status, body) => (Number(status) === 400 && body && typeof body === 'object' && body.code === undefined && typeof body.error === 'string' && body.error.trim() && body.error.length <= 200 ? body.error.trim() : '');

/** The sentence to show for a refused answer (a Response, already read: pass its status and parsed body). */
export function refusalText(status, body) {
    return ownValidation(status, body) || TEXT[refusalKey(status, body)] || UNAVAILABLE;
}

/** The sentence for a refused fetch Response: reads its body (an unreadable one counts as none). */
export async function refusalOf(resp) {
    let body = null;
    try { body = await resp.json(); } catch { /* no body: the status alone */ }
    return refusalText(resp.status, body);
}
