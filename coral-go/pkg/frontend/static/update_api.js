/* Everything the auto-update UI says to the server, in one place (#5275), so the final route names drop in here and nowhere else. The two routes of the design (Security's section 13 of #5231) are
 * GET /api/system/update and POST /api/system/update/relaunch {ack}; the two below them are PROVISIONAL (the design has no local route to check now or to save the two switches yet).
 *
 * The status, as the server sends it:
 *   { enabled, installed, state: none|staged|applying|failed,
 *     staged: { version, staged_at, days_ready, verified },
 *     relaunch: { available, reason, hub_allowed, restarts: [{ session_id, name, backend, state, resumes? }], tmux_agents_restart, ack },
 *     last_result: { at, from, to, result: relaunched|refused|failed_rolled_back, by },
 *     checked_at?, install_kind? (homebrew|unsupported), upgrade_command?, auto? }
 * The fields marked ? are not in the design yet; the UI treats each as absent when it is not sent. */

import { serverBase, isLocalServer } from './server_base.js';
import { importOnce } from './import_once.js';
import { setUpdatesEnabled } from './update_flag.js';
const { noteRelaunchSent } = await importOnce('./relaunch_badge.js'); // lazy modules load the others they need through importOnce

const ROUTES = {
    status: '/api/system/update',
    relaunch: '/api/system/update/relaunch', // a machine behind Coral Hub: the hub's owner relaunches it
    relaunchLocal: '/api/system/update/relaunch-local', // this computer, from its own dashboard (#5280 step 3): same acknowledgement, a route of its own
    cloudStatus: '/api/cloud/status', // GET: hub_relaunch_updates is where the saved switch is read
    hubRelaunch: '/api/cloud/hub-relaunch', // PUT {allow}: the switch Coral Hub may relaunch this Coral (#5280)
    check: '/api/system/update/check', // POST: one background check, local only
    prefs: '/api/system/update/settings', // PUT {auto}: check for updates automatically, local only
};

/** What the server serves beyond the status, the relaunch and the hub switch. Both are off until a route is registered (QA, #5302): Check now and the auto switch are not drawn, and nothing is sent to a path that answers 404. */
export const serverHas = { check: true, autoPref: true }; // the server has both routes now (#5280 step 3 B); they answer 404 only where the update feature is off, and then the whole UI is off too

const str = (v) => (typeof v === 'string' ? v : '');
const num = (v) => (Number.isFinite(Number(v)) ? Number(v) : 0);

export const INSTALL_KINDS = ['app', 'msi', 'portable', 'tarball', 'homebrew', 'unwritable'];
const NOT_READY = ['not_checked', 'up_to_date', 'not_promoted', 'backoff', 'check_failed', 'unsupported_install', 'switch_off', 'applying'];
const ROLLBACK_REASONS = ['did_not_start', 'not_healthy', 'swap_failed', 'verify_failed', 'other'];
const VERSION_RE = /^\d{1,4}\.\d{1,4}\.\d{1,4}$/;

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
        // The fields the server sends beyond the design's (#5388), each only when it knows, in a fixed vocabulary; one that is absent is unknown, never "up to date"
        checkedAt: str(raw.checked_at),
        installKind: INSTALL_KINDS.includes(raw.install_kind) ? raw.install_kind : '',
        upgradeCommand: raw.install_kind === 'homebrew' ? str(raw.upgrade_command) : '',
        notReady: NOT_READY.includes(raw.not_ready_reason) ? raw.not_ready_reason : '',
        latest: VERSION_RE.test(str(raw.latest)) ? raw.latest : '',
        rollback: raw.last_rollback && typeof raw.last_rollback === 'object' ? { at: str(raw.last_rollback.at), from: str(raw.last_rollback.from), to: str(raw.last_rollback.to), reason: ROLLBACK_REASONS.includes(raw.last_rollback.reason) ? raw.last_rollback.reason : 'other', restartFailed: raw.last_rollback.restart_failed === true } : null,
        auto: typeof raw.auto === 'boolean' ? raw.auto : null,
        // Release channels (#5558; the server's #5557): the fields are optional, an older server sends none and the pane then draws no channel group.
        hasChannel: raw.channel === 'approved' || raw.channel === 'agentic',
        channel: raw.channel === 'agentic' ? 'agentic' : 'approved', // anything else is read as approved: the safe side wins
        approved: raw.approved && typeof raw.approved === 'object' && VERSION_RE.test(str(raw.approved.version)) ? { version: raw.approved.version, approvedAt: Date.parse(str(raw.approved.approved_at)) ? str(raw.approved.approved_at) : '' } : null,
        pastApproved: raw.past_approved === true,
        waiting: Array.isArray(raw.waiting) ? raw.waiting.filter(w => w && typeof w === 'object' && VERSION_RE.test(str(w.version))).slice(0, 5).map(w => ({ version: w.version, publishedAt: Date.parse(str(w.published_at)) ? str(w.published_at) : '', notes: str(w.notes).slice(0, 2000) })) : [],
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

/** What a reason says to the person, for the status's `relaunch.reason` and the refusals of a relaunch (the machine's own and Coral Hub's). A reason this build does not know is "Unavailable": never the server's word, which may be anything. */
const REASON_TEXT = {
    max_mode: 'This Coral is not allowed to restart agents from here.',
    owner_only: 'Only the owner can relaunch this Coral.',
    hub_relaunch_off: 'Relaunching from Coral Hub is switched off on this computer. Change it in Settings > Network and Hub.',
    switch_off: 'Relaunching from Coral Hub is switched off on this computer. Change it in Settings > Network and Hub.',
    nothing_staged: 'There is no update ready to install.',
    not_verified: 'The update has not been verified, so it was not installed.',
    busy: 'Another relaunch is already running.',
    applying: 'The update is being installed.',
    unsupported_install: 'This install cannot update itself.',
    already_relaunched: 'This update was already tried. Relaunch from this computer, or wait for a newer version.',
    mode_too_low: 'Needs Launch access on this machine.',
    hub_off: 'Relaunching from Coral Hub is switched off for this machine.',
    fleet_busy: 'Other machines are relaunching. Try again in a few minutes.',
    relaunch_fleet_busy: 'Try again in a few minutes.',
    too_soon: 'Try again in a few minutes.',
    label_missing: 'This machine is not set up for relaunching from Coral Hub.',
    reauth_required: 'Sign in again to relaunch this Coral.',
    no_run_record: 'Coral could not be restarted automatically on this computer, so it cannot relaunch itself here.', // #5419
};
/** Set while the person is away signing in again (the hub's 401 reauth_required): on return the dashboard reads the status afresh and shows the dialog again. */
export const RESUME_KEY = 'coral-update-resume';
export const reasonText = (reason) => (Object.hasOwn(REASON_TEXT, reason) ? REASON_TEXT[reason] : 'Unavailable');
/** One wording for a relaunch that is offered but cannot be used now (the top-bar control, the Settings card): "Not available right now: <why>". */
export const notAvailable = (reason) => `Not available right now: ${reasonText(reason)}`;

/** Why a roll-back happened, from the server's fixed codes (#5388); "other" and anything unknown say nothing more than that it was undone. */
const ROLLBACK_TEXT = {
    did_not_start: 'The new version did not start.',
    not_healthy: 'The new version started but did not become healthy.',
    swap_failed: 'The new version could not be put in place.',
    verify_failed: 'The new version did not pass the check before it was installed.',
};
export const rollbackText = (reason) => (Object.hasOwn(ROLLBACK_TEXT, reason) ? ROLLBACK_TEXT[reason] : '');

/** What Settings says when nothing is ready, from the server's own reason (#5388). Absent or unknown: it does not know, and does not say "up to date". */
export function notReadyText(s) {
    switch (s.notReady) {
        case 'up_to_date': return { title: 'Coral is up to date', sub: `You are on ${s.installed}.` };
        case 'not_promoted': return { title: s.latest ? `Coral ${s.latest} is out` : 'A newer Coral is out', sub: 'Coral will download and check it once it is ready. Installing it is up to you.' };
        case 'backoff': return { title: 'Nothing to install right now', sub: 'Coral will check again later.' };
        case 'check_failed': return { title: 'Coral could not check for updates', sub: 'It will try again later. Nothing was changed.' };
        case 'not_checked': return { title: 'Coral has not checked for updates yet', sub: `You are on ${s.installed}.` };
        case 'switch_off': return { title: 'Automatic update checks are off', sub: 'Turn on "Check for updates automatically" in Settings > Updates to have updates downloaded and kept ready.' };
        default: return { title: 'Nothing is ready to install', sub: `You are on ${s.installed}.` };
    }
}

/** A sign-in address the hub sent: only a path on this same site is followed (never another host, never a script). */
const safePath = (v) => (typeof v === 'string' && /^\/(?!\/)[^\s\\]*$/.test(v) ? v : '');

/** The status, or null when the server has no updater (a 404) or did not answer. */
export async function fetchUpdateStatus() {
    try {
        const resp = await fetch(ROUTES.status, { headers: { Accept: 'application/json' } });
        if (!resp.ok) { setUpdatesEnabled(false); return null; }
        const s = normalizeStatus(await resp.json());
        setUpdatesEnabled(!!s && s.enabled); // the server says whether the UI is on
        return s;
    } catch { return null; }
}

/** Relaunch to install the staged update, acknowledging the agents the user saw. Resolves { ok, status, code, error, relaunch }: relaunch is the server's current list when it answered 409 stale_ack. */
export async function requestRelaunch(ack, { server = '' } = {}) {
    try {
        const resp = await fetch(serverBase(server) + (isLocalServer(server) ? ROUTES.relaunchLocal : ROUTES.relaunch), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ack }) });
        if (resp.status === 202 && !isLocalServer(server)) noteRelaunchSent(server); // a machine behind the hub was told to relaunch: its Servers row says Updating (#5355)
        let body = {};
        try { body = await resp.json(); } catch { /* an empty body */ }
        // the hub says why in `reason` (reauth_required, mode_too_low), the machine in `code`; error text is never shown (a reason this build does not know is "Unavailable")
        return { ok: resp.ok, status: resp.status, code: str(body.code) || str(body.reason), signInUrl: safePath(body.sign_in_url), relaunch: body.relaunch && typeof body.relaunch === 'object' ? normalizeRelaunch(body.relaunch) : null };
    } catch (e) { return { ok: false, status: 0, code: 'network', signInUrl: '', relaunch: null }; }
}

/** The saved "Coral Hub may relaunch this Coral" switch, or null when it can't be read (the status of the update has no such field; the connector's does). */
export async function fetchHubRelaunch() {
    try {
        const resp = await fetch(ROUTES.cloudStatus);
        if (!resp.ok) return null;
        const v = (await resp.json()).hub_relaunch_updates;
        return typeof v === 'boolean' ? v : null;
    } catch { return null; }
}

/** Ask the updater to look for a release now (false when the server has no such route). */
export async function requestCheckNow() {
    if (!serverHas.check) return false;
    try { return (await fetch(ROUTES.check, { method: 'POST' })).ok; } catch { return false; }
}

/** Save "Check for updates automatically". */
export async function savePrefs({ auto }, was = {}) {
    if (serverHas.autoPref && auto !== undefined && !!auto !== (was.auto !== false)) {
        const resp = await fetch(ROUTES.prefs, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ auto: !!auto }) });
        if (!resp.ok) throw new Error('Could not save the update settings.');
    }
}

/** Save the update channel (an ordinary setting: PUT /api/settings { update_channel }); throws with the server's reason when it refuses. */
export async function saveChannel(channel) {
    const resp = await fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ update_channel: channel === 'agentic' ? 'agentic' : 'approved' }) });
    if (!resp.ok) throw new Error('Could not save the update channel.');
}

/** How long an update has been ready, as the three states of the control. */
export const ageTier = (days) => (days >= 7 ? 'stale' : days >= 2 ? 'warm' : 'fresh');
