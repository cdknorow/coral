/* The auto-update UI (#5275, design #5270): the "Relaunch to update" control in the top bar, the failure notice, and the status both read. Hidden and silent while the flag is off (update_flag.js):
 * nothing is fetched. The status comes from update_api.js; this module keeps the last one, asks again every ten minutes and when the tab is shown (the Settings pane reads it with getUpdateStatus).
 * The control never animates: its state is a colour, a word and a number of days (colour is not the only signal: from "warm" on the age is written, on a phone too). */
import { importOnce } from './import_once.js';
import { browserLocal, browserSession } from './safe_storage.js';
import { pollEvery } from './overload.js';
const { RESUME_KEY, ageTier, fetchUpdateStatus, notAvailable, requestCheckNow, rollbackText, serverHas } = await importOnce('./update_api.js'); // lazy modules load the others they need through importOnce, so a failed load is retried
import { updatesUiOn } from './update_flag.js';

const POLL_MS = 10 * 60 * 1000;
const DISMISSED_KEY = 'coral-update-fail-dismissed';
let status = null;
let timer = 0;

export const getUpdateStatus = () => status;

const $ = (id) => document.getElementById(id);
const plural = (n, w) => `${n} ${w}${n === 1 ? '' : 's'}`;

/** Whether the control is offered: an update is staged and verified and this install can apply it. */
export function relaunchOffered(s) {
    if (!s || !s.enabled || s.state !== 'staged' || !s.staged || !s.staged.verified) return false;
    return !['unsupported_install', 'nothing_staged', 'not_verified', 'hub_off'].includes(s.relaunch.reason); // hub_off: Coral Hub's relaunching is off for this machine, so there is nothing to offer
}

/** What the control says, and the name a screen reader reads. */
export function pillWords(s) {
    const days = s.staged.days, tier = ageTier(days);
    const age = tier === 'fresh' ? '' : plural(days, 'day');
    const ago = days < 1 ? 'earlier today' : `${plural(days, 'day')} ago`;
    return {
        tier, age,
        name: `Relaunch to update to version ${s.staged.version}. Downloaded ${ago}.${s.relaunch.available ? '' : ` ${notAvailable(s.relaunch.reason)}`}`,
    };
}

const phone = window.matchMedia('(max-width: 767px)');
/** On a phone the control is a row of its own under the top bar (the bar has five buttons and no room: the view picker's name was squeezed out and the gear pushed off screen); elsewhere it is in the bar's actions, before the icons. */
function placePill() {
    const pill = $('update-pill'), strip = $('update-strip'), actions = document.querySelector('.top-bar-actions');
    if (!pill || !strip || !actions) return;
    if (phone.matches) { if (pill.parentElement !== strip) strip.append(pill); }
    else if (pill.parentElement !== actions) actions.prepend(pill);
    strip.hidden = !(phone.matches && !pill.hidden);
}

function renderPill() {
    const pill = $('update-pill');
    if (!pill) return;
    const on = relaunchOffered(status) && status.relaunch.reason !== 'no_run_record'; // no_run_record: Settings carries the sentence, the top bar has nothing to offer (like unsupported_install)
    pill.hidden = !on;
    placePill();
    if (!on) return;
    const w = pillWords(status);
    pill.dataset.age = w.tier;
    pill.setAttribute('aria-label', w.name);
    pill.title = w.name;
    pill.querySelector('.update-age').textContent = w.age ? ` · ${w.age}` : '';
    const busy = !status.relaunch.available;
    pill.setAttribute('aria-disabled', String(busy));
}

/** The text of the failure notice for the status, or null when there is none (offline is not a failure). */
export function failureNotice(s) {
    if (!s || !s.enabled) return null;
    const to = (s.last && s.last.to) || (s.staged && s.staged.version) || '';
    if (s.state === 'failed' || (s.last && s.last.result === 'refused')) {
        return { kind: 'verify', key: `verify:${to}:${s.last ? s.last.at : ''}`, lead: `Coral found an update (v${to}) it could not verify, so it was not installed.`, rest: ` Your version is unchanged.` }; // the docs' sentence (updates.md)
    }
    if (s.last && s.last.result === 'failed_rolled_back') {
        return { kind: 'rolled', key: `rolled:${to}:${s.last.at}`, lead: `The update to ${to} was undone.`, rest: ` ${s.rollback && rollbackText(s.rollback.reason) ? rollbackText(s.rollback.reason) + ' ' : ''}${s.rollback && s.rollback.restartFailed ? `Coral was stopped by an update that could not restart it, and had to be started again by hand. It is on ${s.installed}.` : `Coral went back to ${s.installed}. Your agents are unaffected.`}` };
    }
    return null;
}

function renderBanner() {
    const banner = $('update-fail-banner');
    if (!banner) return;
    const n = failureNotice(status);
    const shown = !!n && browserLocal.get(DISMISSED_KEY) !== n.key;
    const wasShown = !banner.hidden;
    banner.hidden = !shown;
    if (!shown) return;
    banner.dataset.kind = n.kind; banner.dataset.key = n.key;
    banner.querySelector('.update-fail-lead').textContent = n.lead;
    banner.querySelector('.update-fail-rest').textContent = n.rest;
    // Try again is drawn only when it can do something: a check route to ask, or a rolled-back update that can be relaunched again
    const again = banner.querySelector('.update-fail-again');
    if (again) again.hidden = !(serverHas.check || (n.kind === 'rolled' && relaunchOffered(status) && status.relaunch.available));
    // role=alert announces a banner when it appears; the same notice redrawn by the next poll is not announced again (the text is only written when it changed)
    if (!wasShown) banner.setAttribute('role', 'alert');
}

/** The one-time notice (#6292): a verified update that this install can apply now, said once per version in this browser. Review opens the confirmation; Later closes the notice and changes nothing (the control stays). Never drawn where the control is not (Homebrew, unsupported, no run record, Coral Hub off), and never in a hidden tab (it would be marked seen unread). */
const READY_SEEN_KEY = 'coral-update-ready-seen';
export function readyNotice(s) {
    if (!relaunchOffered(s) || s.relaunch.reason === 'no_run_record' || !s.relaunch.available) return null;
    return { key: s.staged.version, lead: `Coral ${s.staged.version} is ready.`, rest: ' It is downloaded and checked. Nothing is installed until you say yes.' };
}
function renderReadyNotice() {
    const box = $('update-ready-notice');
    if (!box) return;
    const n = readyNotice(status);
    if (!n) { box.hidden = true; return; }
    if (!box.hidden && box.dataset.key === n.key) return; // already showing this one: not announced again
    if (browserLocal.get(READY_SEEN_KEY) === n.key || document.hidden) return;
    browserLocal.set(READY_SEEN_KEY, n.key);
    box.dataset.key = n.key;
    box.querySelector('.update-ready-lead').textContent = n.lead;
    box.querySelector('.update-ready-rest').textContent = n.rest;
    box.hidden = false;
}

function notify() { renderPill(); renderBanner(); renderReadyNotice(); }

export async function refreshUpdateStatus() {
    status = await fetchUpdateStatus();
    notify();
    return status;
}

async function openDialog() {
    if (!relaunchOffered(status) || !status.relaunch.available) return;
    const m = await importOnce('./update_dialog.js');
    m.openUpdateDialog(status);
}

async function tryAgain() {
    const banner = $('update-fail-banner');
    if (banner && banner.dataset.kind === 'rolled' && relaunchOffered(status) && status.relaunch.available) { openDialog(); return; }
    await requestCheckNow();
    await refreshUpdateStatus();
}

/** Back from signing in again (the hub's reauth_required): read the status afresh, for a fresh ack, and show the dialog again; the relaunch itself is never sent for the person. */
export async function resumeAfterSignIn() {
    const flag = browserSession.get(RESUME_KEY) === '1';
    browserSession.remove(RESUME_KEY);
    if (!flag) return false;
    await refreshUpdateStatus();
    if (relaunchOffered(status) && status.relaunch.available) { await openDialog(); return true; }
    return false;
}

export function openUpdatesSettings() { window.showSettingsModal && window.showSettingsModal('updates'); }

/** Wire the controls and start asking. Safe to call twice. */
export function startUpdateUi() {
    if (!updatesUiOn() || timer) return;
    $('update-pill')?.addEventListener('click', (e) => { if (e.currentTarget.getAttribute('aria-disabled') === 'true') return; openDialog(); });
    const banner = $('update-fail-banner');
    banner?.querySelector('.update-fail-details')?.addEventListener('click', openUpdatesSettings);
    banner?.querySelector('.update-fail-again')?.addEventListener('click', tryAgain);
    const ready = $('update-ready-notice');
    ready?.querySelector('.update-ready-review')?.addEventListener('click', () => { ready.hidden = true; openDialog(); });
    ready?.querySelector('.update-ready-later')?.addEventListener('click', () => { ready.hidden = true; });
    banner?.querySelector('.update-fail-close')?.addEventListener('click', () => { browserLocal.set(DISMISSED_KEY, banner.dataset.key || ''); banner.hidden = true; });
    timer = pollEvery(refreshUpdateStatus, POLL_MS); // skips a hidden tab and an overloaded server (overload.js)
    phone.addEventListener('change', placePill);
    document.addEventListener('visibilitychange', () => { if (!document.hidden) { renderReadyNotice(); refreshUpdateStatus(); } }); // the notice waiting for a tab to be seen shows at once, from the last status; the read after it brings the newest
    document.addEventListener('coral-settings-group', (e) => { if (e.detail && e.detail.id === 'updates') importOnce('./update_settings.js').then((m) => m.showUpdatesPane()).catch(() => {}); });
    refreshUpdateStatus().then(resumeAfterSignIn);
}
