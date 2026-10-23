/* A write the Coral Hub could not finish telling (#6342): the hub answers 502 with code outcome_unknown when it sent a write on to the computer and then lost the answer, so the write may or may not have happened. The one sentence for it, and the
 * test for it, in one module with no imports (cloud.js, server_status.js and the write paths read it). Nothing retries on it: the person checks first. A launch keeps its own wording (launch_server.js). */
export const OUTCOME_UNKNOWN_LINE = 'That may have reached the computer. Check before trying again.';

/** Whether a url is a launch (POST /api/sessions/launch or /launch-team, direct or through a hub's remote path). */
export const isLaunchUrl = (url) => /\/launch(-team)?$/.test(String(url == null ? '' : url).split('?')[0].replace(/\/+$/, ''));

/** A 502 whose body says code outcome_unknown, for anything but a launch. */
export function isOutcomeUnknown(status, body, url = '') {
    return status === 502 && !!body && typeof body === 'object' && body.code === 'outcome_unknown' && !isLaunchUrl(url);
}

/** The sentence for a failed response when it is one, else ''. Reads a clone of the body. */
export async function outcomeUnknownLine(resp) {
    if (!resp || resp.status !== 502) return '';
    let body = null;
    try { body = await resp.clone().json(); } catch { /* not JSON */ }
    return isOutcomeUnknown(resp.status, body, resp.url) ? OUTCOME_UNKNOWN_LINE : '';
}
