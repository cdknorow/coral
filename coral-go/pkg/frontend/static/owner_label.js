/* A computer linked to Coral Hub before it recorded who owns it refuses its owner on the owner-only routes (artifacts, thumbnails, saved messages, custom models): the server answers
 * 403 owner_label_missing (#6108). The local fix is "Confirm owner" in Settings > Network and Hub on the computer; re-linking is the fallback. This is the one sentence the dashboard says
 * for it, and the check for that answer. The sentence is ours: the server's words are not shown. */

export const OWNER_LABEL_MISSING = 'owner_label_missing';
export const OWNER_LABEL_TEXT = 'Coral Hub can\'t confirm you own this computer yet. On the computer, open Settings > Network and Hub and choose Confirm owner.';
/** The short form beside a thumbnail that cannot load. */
export const OWNER_LABEL_NOTE = 'Confirm the owner on the computer to use this.';

/** Whether a fetch Response is the "no owner recorded" refusal. Reads a clone, so the caller can still read the body. */
export async function isOwnerLabelRefusal(resp) {
    if (!resp || resp.status !== 403) return false;
    try {
        const body = await resp.clone().json();
        return !!body && typeof body === 'object' && body.code === OWNER_LABEL_MISSING;
    } catch { return false; }
}
