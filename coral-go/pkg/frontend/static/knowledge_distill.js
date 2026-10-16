/* Knowledge distillation runs (#3325). The server runs a distillation in the background
   (POST /api/teams/detail/{team}/distill-knowledge answers 202; GET on the same path gives
   {status: none|running|complete|failed, progress, error, started_at, finished_at, and for a
   complete run that was partial: failed_agents [{agent, error}], collaboration_error}).

   The run is tracked here, per team, independent of any view: one poller per team while it
   runs, an event ('coral:distill-status', detail {team, job}) on every change, a toast when it
   finishes (or is lost to a server restart) wherever the user is, and a "Distilling…" chip on the team's sidebar header. The
   Knowledge tab renders from this state, so leaving the tab (or the team) and coming back
   shows the run still going, or its result. The first time the sidebar shows a team, its
   status is asked once, so a run started in another window (or before a reload) is found. */

import { teamFetch, keyLabel } from './server_base.js';
import { showToast } from './utils.js';
import { importOnce } from './import_once.js';
import { pollEvery } from './overload.js';

export const DISTILL_POLL_MS = 2000;
export const distillHooks = { pollMs: DISTILL_POLL_MS };

const jobs = new Map();     // team key -> last status from the server
const pollers = new Map();  // team key -> timer
const asked = new Set();    // teams whose status was asked once this page load

const path = n => `/api/teams/detail/${encodeURIComponent(n)}/distill-knowledge`;

function _set(team, job) {
    const prev = jobs.get(team);
    jobs.set(team, job);
    document.dispatchEvent(new CustomEvent('coral:distill-status', { detail: { team, job, prev: prev || null } }));
    // The sidebar chip follows the running state.
    if (!!(prev && prev.status === 'running') !== (job.status === 'running')) {
        importOnce('./render.js').then(m => m.scheduleLiveSessionsRender && m.scheduleLiveSessionsRender()).catch(() => {});
    }
}

/** The last known status of a team's run ({status: 'none'} when none is known). */
export function distillJob(team) {
    return jobs.get(team) || { status: 'none' };
}

export function isDistilling(team) {
    return distillJob(team).status === 'running';
}

async function _fetchStatus(team) {
    const r = await teamFetch(team, path);
    if (!r.ok) throw new Error(`distill status ${r.status}`);
    return r.json();
}

function _stopPoll(team) {
    clearInterval(pollers.get(team));
    pollers.delete(team);
}

function _ensurePoll(team) {
    if (pollers.has(team)) return;
    const tick = async () => {
        let job;
        try { job = await _fetchStatus(team); } catch { return; } // keep polling through a blip
        const was = jobs.get(team);
        _set(team, job);
        if (job.status === 'running') return;
        _stopPoll(team);
        if (was && was.status === 'running') {
            if (job.status === 'complete') {
                // A partial run still completed: not an error, but shown longer (the tab has the details).
                const failed = (job.failed_agents || []).length;
                if (failed) showToast(`Knowledge for ${keyLabel(team)} is ready, but ${failed} agent${failed === 1 ? '' : 's'} couldn't be distilled`, false, 8000);
                else showToast(`Knowledge for ${keyLabel(team)} is ready`);
            }
            else if (job.status === 'failed') showToast(`Knowledge distillation for ${keyLabel(team)} failed${job.error ? ': ' + job.error : ''}`, true);
            // Runs live in the server's memory: a restart mid-run loses it, and the status reads 'none'.
            else if (job.status === 'none') showToast(`The distillation for ${keyLabel(team)} was interrupted (Coral restarted); start it again`, true);
        }
    };
    pollers.set(team, pollEvery(tick, distillHooks.pollMs));
}

/** Ask the server for a team's status now; a running job gets a poller. Resolves to the job. */
export async function refreshDistill(team) {
    if (!team) return { status: 'none' };
    let job;
    try { job = await _fetchStatus(team); } catch { return distillJob(team); }
    _set(team, job);
    if (job.status === 'running') _ensurePoll(team);
    return job;
}

/** Start a run for `team`, or join the one already running (the server never starts two). */
export async function startDistill(team) {
    const r = await teamFetch(team, path, { method: 'POST' });
    let job = null;
    try { job = await r.json(); } catch { /* none */ }
    if (!r.ok) throw new Error((job && job.error) || 'Failed to start distillation');
    _set(team, job && job.status ? job : { status: 'running', progress: { phase: 'starting' } });
    _ensurePoll(team);
    return jobs.get(team);
}

/** The sidebar's team header: a chip while the team's knowledge is being distilled. The first
 *  time a team is shown, its status is asked once (a run from another window or before a reload). */
export function distillChip(team) {
    if (team && !asked.has(team)) {
        asked.add(team);
        refreshDistill(team);
    }
    if (!isDistilling(team)) return '';
    return '<span class="team-distill-chip" title="Distilling team knowledge…"><span class="team-distill-dot" aria-hidden="true"></span>Distilling…</span>';
}

