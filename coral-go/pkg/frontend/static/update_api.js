/* Everything the auto-update UI says to the server, in one place (#5275), so the final route names drop in here and nowhere else. The two routes of the design (Security's section 13 of #5231) are
 * GET /api/updates/status and POST /api/updates/relaunch {ack}; the two below them are PROVISIONAL (the design has no local route to check now or to save the two switches yet).
 *
 * The status, as the server sends it:
 *   { enabled, installed, state: none|staged|applying|failed,
 *     staged: { version, staged_at, days_ready, verified },
 *     relaunch: { available, reason, hub_allowed, restarts: [{ session_id, name, backend, state, resumes? }], tmux_agents_restart, ack },
 *     last_result: { at, from, to, result: relaunched|refused|failed_rolled_back, by },
 *     checked_at?, install_kind? (homebrew|unsupported), upgrade_command?, auto?, hub_relaunch? }
 * The fields marked ? are not in the design yet; the UI treats each as absent when it is not sent. */

const ROUTES = {
    status: '/api/updates/status',
    relaunch: '/api/updates/relaunch',
    check: '/api/updates/check', // provisional: ask the updater to look now
    prefs: '/api/updates/settings', // provisional: the two switches
};

const str = (v) => (typeof v === 'string' ? v : '');
const num = (v) => (Number.isFinite(Number(v)) ? Number(v) : 0);

/** The status with every field present and of its type (an older or newer server never throws in the UI). */
export function normalizeStatus(raw) {
    if (!raw || typeof raw !== 'object') return null;
    const staged = raw.staged && typeof raw.staged === 'object' ? { version: str(raw.staged.version), stagedAt: str(raw.staged.staged_at), days: Math.max(0, Math.floor(num(raw.staged.days_ready))), verified: raw.staged.verified === true } : null;
    const r = raw.relaunch && typeof raw.relaunch === 'object' ? raw.relaunch : {};
    const last = raw.last_result && typeof raw.last_result === 'object' ? { at: str(raw.last_result.at), from: str(raw.last_result.from), to: str(raw.last_result.to), result: str(raw.last_result.result), by: str(raw.last_result.by) } : null;
    return {
        enabled: raw.enabled === true,
        installed: str(raw.installed),
        state: ['none', 'staged', 'applying', 'failed'].includes(raw.state) ? raw.state : 'none',
        staged,
        relaunch: normalizeRelaunch(r),
        last,
        checkedAt: str(raw.checked_at),
        installKind: ['homebrew', 'unsupported'].includes(raw.install_kind) ? raw.install_kind : '',
        upgradeCommand: str(raw.upgrade_command),
        auto: typeof raw.auto === 'boolean' ? raw.auto : null,
        hubRelaunch: typeof raw.hub_relaunch === 'boolean' ? raw.hub_relaunch : null,
        progress: Number.isFinite(Number(raw.progress)) ? Math.min(100, Math.max(0, Math.round(Number(raw.progress)))) : null, // provisional: percent of a download in progress
    };
}

export function normalizeRelaunch(r) {
    return {
        available: r.available === true,
        reason: str(r.reason),
        restarts: (Array.isArray(r.restarts) ? r.restarts : []).map((a) => ({ sessionId: str(a && a.session_id), name: str(a && a.name), backend: str(a && a.backend), state: str(a && a.state), resumes: a && typeof a.resumes === 'boolean' ? a.resumes : null })),
        tmuxRestart: r.tmux_agents_restart === true,
        ack: str(r.ack),
    };
}

/** The status, or null when the server has no updater (a 404) or did not answer. */
export async function fetchUpdateStatus() {
    try {
        const resp = await fetch(ROUTES.status, { headers: { Accept: 'application/json' } });
        if (!resp.ok) return null;
        return normalizeStatus(await resp.json());
    } catch { return null; }
}

/** Relaunch to install the staged update, acknowledging the agents the user saw. Resolves { ok, status, code, error, relaunch }: relaunch is the server's current list when it answered 409 stale_ack. */
export async function requestRelaunch(ack) {
    try {
        const resp = await fetch(ROUTES.relaunch, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ack }) });
        let body = {};
        try { body = await resp.json(); } catch { /* an empty body */ }
        return { ok: resp.ok, status: resp.status, code: str(body.code), error: str(body.error), relaunch: body.relaunch && typeof body.relaunch === 'object' ? normalizeRelaunch(body.relaunch) : null };
    } catch (e) { return { ok: false, status: 0, code: 'network', error: 'Could not reach Coral.', relaunch: null }; }
}

/** Ask the updater to look for a release now (provisional). */
export async function requestCheckNow() {
    try { return (await fetch(ROUTES.check, { method: 'POST' })).ok; } catch { return false; }
}

/** Save the two switches (provisional): auto download, and whether Coral Hub may relaunch this Coral. */
export async function savePrefs({ auto, hubRelaunch }) {
    const resp = await fetch(ROUTES.prefs, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ auto: !!auto, hub_relaunch: !!hubRelaunch }) });
    if (!resp.ok) throw new Error('Could not save the update settings.');
}

/** How long an update has been ready, as the three states of the control. */
export const ageTier = (days) => (days >= 7 ? 'stale' : days >= 2 ? 'warm' : 'fresh');
