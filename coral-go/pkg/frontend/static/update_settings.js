/* Settings > Updates (#5275, design #5270): one status card (a dot, a line, a sentence, the action), the two switches, when it last looked, the version, and where Coral reaches. The pane is
 * drawn from the status each time it is shown. The switches are saved with Apply like the rest of Settings (applyUpdatePrefs); the card's buttons act at once: "Relaunch to update" opens the
 * confirmation (a dialog over this one), "Check now" asks the updater to look. A Homebrew or unsupported install says so and why, with the command to run. */
import { importOnce } from './import_once.js';
import { copyText, showToast } from './utils.js';
import { isCloud } from './server_base.js';
const { ageTier, notAvailable, notReadyText, fetchHubRelaunch, requestCheckNow, savePrefs, saveChannel, serverHas } = await importOnce('./update_api.js');
const { failureNotice, getUpdateStatus, refreshUpdateStatus, relaunchOffered } = await importOnce('./update_ui.js');

const mk = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
const plural = (n, w) => `${n} ${w}${n === 1 ? '' : 's'}`;
let hubOn = null; // the hub switch as the server has it (GET /api/cloud/status), null when unknown
const body = () => document.getElementById('update-pane-body');

function stamp(iso) {
    const t = Date.parse(iso);
    if (!t) return '';
    const d = new Date(t), now = new Date();
    const hm = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false });
    return d.toDateString() === now.toDateString() ? `Today ${hm}` : `${d.toLocaleDateString([], { month: 'short', day: 'numeric' })} ${hm}`;
}

function card(s) {
    const c = mk('div', 'update-status'); c.setAttribute('role', 'group');
    const dot = mk('span', 'update-state-dot'); dot.setAttribute('aria-hidden', 'true');
    const text = mk('div', 'update-status-text');
    const title = mk('b'), sub = mk('p');
    text.append(title, sub);
    let act = null;
    const notice = failureNotice(s);
    if (s.relaunch.reason === 'applying' || s.state === 'applying') {
        title.textContent = 'Updating Coral…'; sub.textContent = 'The update is being installed. Coral restarts when it is done.'; dot.dataset.k = 'warm'; c.dataset.k = 'warm';
    } else if (notice) {
        title.textContent = notice.lead; sub.textContent = notice.rest.trim(); dot.dataset.k = 'bad'; c.dataset.k = 'bad';
        if (serverHas.check) act = mk('button', 'btn update-act', 'Try again'); // no server route to check now, no button
        if (act) act.type = 'button';
        if (act) act.addEventListener('click', async () => { act.disabled = true; await requestCheckNow(); await refreshUpdateStatus(); showUpdatesPane(); });
    } else if (relaunchOffered(s)) {
        const days = s.staged.days, tier = ageTier(days);
        title.textContent = `Coral ${s.staged.version} is ready`;
        sub.textContent = `Downloaded and verified ${days < 1 ? 'earlier today' : plural(days, 'day') + ' ago'}. It installs the next time Coral starts.`;
        dot.dataset.k = tier; c.dataset.k = tier;
        act = mk('button', 'btn btn-primary update-act', 'Relaunch to update'); act.type = 'button';
        act.setAttribute('aria-haspopup', 'dialog');
        if (!s.relaunch.available) { act.setAttribute('aria-disabled', 'true'); sub.textContent += ` ${notAvailable(s.relaunch.reason)}`; }
        act.addEventListener('click', async () => { if (act.getAttribute('aria-disabled') === 'true') return; (await importOnce('./update_dialog.js')).openUpdateDialog(getUpdateStatus()); });
    } else if (s.progress !== null) {
        title.textContent = `Downloading Coral${s.staged && s.staged.version ? ' ' + s.staged.version : ''}`; sub.textContent = `${s.progress} percent`; dot.dataset.k = 'ok'; c.dataset.k = 'ok';
        const bar = mk('div', 'update-bar'); bar.setAttribute('role', 'progressbar'); bar.setAttribute('aria-valuemin', '0'); bar.setAttribute('aria-valuemax', '100'); bar.setAttribute('aria-valuenow', String(s.progress)); bar.setAttribute('aria-label', 'Download');
        const fill = mk('span'); fill.style.width = `${s.progress}%`; bar.append(fill); text.append(bar);
    } else {
        // Only the server's own reason says why nothing is ready (#5388): without it Coral does not claim to be up to date
        const t = notReadyText(s);
        title.textContent = t.title; sub.textContent = t.sub;
        if (s.notReady === 'up_to_date') { dot.dataset.k = 'ok'; c.dataset.k = 'ok'; } else if (s.notReady === 'check_failed') { dot.dataset.k = 'warm'; c.dataset.k = 'warm'; }
        if (serverHas.check) act = mk('button', 'btn update-act', 'Check now');
        if (act) act.type = 'button';
        if (act) act.addEventListener('click', async () => { act.disabled = true; const ok = await requestCheckNow(); await refreshUpdateStatus(); showUpdatesPane(); if (!ok) showToast('Could not check for updates.', true); });
    }
    c.append(dot, text); if (act) c.append(act);
    return c;
}

/** Whether this install is one Coral does not update by itself: Homebrew, a folder it cannot write to, or whatever the server says is unsupported (its relaunch reason, since an older server sends no install kind). */
const isManaged = (s) => s.installKind === 'homebrew' || s.installKind === 'unwritable' || s.relaunch.reason === 'unsupported_install' || s.notReady === 'unsupported_install';

function managed(s) {
    const m = mk('div', 'update-managed'); m.setAttribute('role', 'group');
    const icon = mk('span', 'material-icons', 'info'); icon.setAttribute('aria-hidden', 'true');
    const t = mk('div');
    if (s.installKind === 'homebrew') {
        t.append(mk('b', null, 'Homebrew updates this Coral'), mk('p', null, 'Coral does not update itself when Homebrew installed it. To update, run:'));
        const cmd = mk('div', 'update-cmd'); const code = mk('code', null, s.upgradeCommand || 'brew upgrade --cask coral');
        const copy = mk('button', 'btn', 'Copy'); copy.type = 'button';
        copy.addEventListener('click', async () => { showToast((await copyText(code.textContent)) ? 'Copied' : 'Could not copy', false); });
        cmd.append(code, copy); t.append(cmd);
        t.append(mk('p', 'update-calm', 'Coral still tells you when a new version is out.'));
    } else {
        t.append(mk('b', null, 'This install cannot update itself'), mk('p', null, 'Coral can only update installs it can replace safely (the app, the installer or the archive from the releases page). Install the new release by hand.'));
        const a = mk('a', 'update-link', 'Open the releases page'); a.href = 'https://github.com/subgentic/coral/releases'; a.target = '_blank'; a.rel = 'noopener noreferrer'; t.append(a);
    }
    m.append(icon, t);
    return m;
}

function row(id, label, desc, checked, disabled) {
    const r = mk('div', 'settings-row'); const dt = mk('dt', 'toggle-dt'); const dd = mk('dd');
    const l = mk('label', null, label); l.htmlFor = id; const d = mk('span', 'toggle-desc', desc); d.id = `${id}-desc`; dt.append(l, d);
    const wrap = mk('label', 'check-hit'); const input = document.createElement('input'); input.type = 'checkbox'; input.id = id; input.checked = !!checked; input.disabled = !!disabled; input.setAttribute('aria-describedby', d.id);
    wrap.append(input); dd.append(wrap); r.append(dt, dd);
    return r;
}

// The page that runs the approval (Run workflow with the tag). A constant: no field the server or the release feed sent is ever put into this link.
let _hubForTests = null; // a test says it is the Hub without booting the Hub page
export function _setHubForTests(v) { _hubForTests = v; }
const inHub = () => (_hubForTests === null ? isCloud() : _hubForTests);

const APPROVE_URL = 'https://github.com/subgentic/coral/actions/workflows/approve-release.yml';
const daysSince = (iso) => { const t = Date.parse(iso); return t ? Math.max(0, Math.floor((Date.now() - t) / 86400000)) : null; };
const ago = (d) => (d === null ? '' : d < 1 ? 'earlier today' : `${plural(d, 'day')} ago`);

/** The channel choice (#5558, the Terminal backend's radio cards): Approved (the default) or Agentic. Through the Hub it is a line of text: the owner of that computer chooses. */
function channelBlock(s) {
    const g = mk('div', 'tb-row update-channel'); g.setAttribute('role', 'group'); g.setAttribute('aria-labelledby', 'uc-label');
    const head = mk('div', 'tb-head'); const lab = mk('span', 'tb-label', 'Update channel'); lab.id = 'uc-label'; head.append(lab); g.append(head);
    if (inHub()) { g.append(mk('p', 'tb-sub', `Channel: ${s.channel === 'agentic' ? 'Agentic' : 'Approved'} (set on this computer)`)); return g; }
    g.append(mk('p', 'tb-sub', 'Which releases Coral tells you about. Saved with Apply.'));
    const opt = (value, title, desc) => {
        const l = mk('label', 'tb-opt'); const i = document.createElement('input'); i.type = 'radio'; i.name = 'update-channel'; i.value = value; i.id = `uc-${value}`; i.checked = s.channel === value;
        const t = mk('span'); t.append(mk('span', 'tb-t', title), mk('span', 'tb-d', desc)); l.append(i, t); return l;
    };
    g.append(opt('approved', 'Approved (recommended)', 'Releases the team has approved.'));
    g.append(opt('agentic', 'Agentic (every release)', 'Every release as soon as it is published, including ones not yet approved. These may change faster and be less tested.'));
    if (s.approved) { const d = daysSince(s.approved.approvedAt); g.append(mk('p', 'update-calm', `Approved now: v${s.approved.version}${d === null ? '' : `, approved ${ago(d)}`}.`)); }
    if (s.pastApproved) g.append(mk('p', 'update-calm', 'You are already past the Approved release, so Approved offers nothing newer.'));
    if (s.auto !== false) { const n = mk('p', 'update-calm update-channel-auto', 'Checking for updates automatically still uses Approved releases only.'); n.hidden = s.channel !== 'agentic'; g.append(n); }
    return g;
}

/** The releases published but not yet approved (only those newer than the newest approved one), for the operator: collapsed, each with the tag to type and the one link to the approval page. Plain text only. */
function waitingBlock(s) {
    const d = mk('details', 'update-waiting');
    const sum = mk('summary', null, `Waiting for approval (${s.waiting.length})`);
    d.append(sum);
    d.append(mk('p', 'update-calm', 'Published, not yet approved. Approving makes a release the Approved channel, Homebrew and the download links. Only the repository\'s operator can: Coral does not approve anything itself.'));
    for (const r of s.waiting) {
        const days = daysSince(r.publishedAt);
        const tier = days === null ? 'ok' : ageTier(days) === 'stale' ? 'bad' : ageTier(days);
        const c = mk('div', 'update-status'); c.setAttribute('role', 'group'); c.dataset.k = tier;
        const dot = mk('span', 'update-state-dot'); dot.dataset.k = tier; dot.setAttribute('aria-hidden', 'true');
        const text = mk('div', 'update-status-text');
        text.append(mk('b', null, `Coral ${r.version}`), mk('p', null, days === null ? 'Waiting for approval' : `Published ${ago(days)}${days >= 7 ? ' (over a week)' : ''}, waiting for approval`));
        const how = mk('p', null, 'Run it with the tag '); how.append(mk('code', null, `v${r.version}`));
        text.append(how);
        if (r.notes) { const n = mk('details', 'update-notes'); n.append(mk('summary', null, 'Release notes'), mk('pre', null, r.notes)); text.append(n); }
        const a = mk('a', 'btn update-act', 'Open the approval page'); a.href = APPROVE_URL; a.target = '_blank'; a.rel = 'noopener noreferrer';
        a.setAttribute('aria-label', `Open the approval page for Coral ${r.version} on GitHub (opens a new tab)`);
        c.append(dot, text, a); d.append(c);
    }
    return d;
}

/** Draw the pane from the last status (and ask for a fresh one). */
export async function showUpdatesPane() {
    const host = body();
    if (!host) return;
    const s = getUpdateStatus() || await refreshUpdateStatus();
    hubOn = await fetchHubRelaunch();
    host.replaceChildren();
    if (!s || !s.enabled) { host.append(mk('p', 'update-calm', 'Updates are not available in this Coral.')); return; }
    const unmanaged = isManaged(s);
    host.append(unmanaged ? managed(s) : card(s));
    if (s.hasChannel) {
        host.append(channelBlock(s));
        // The waiting list is the operator's, and the client reads the release list only on Agentic: it shows while Agentic is the choice and the server sent releases (never through the Hub)
        const sync = () => {
            host.querySelector('.update-waiting')?.remove();
            const agentic = !inHub() && !!(host.querySelector('#uc-agentic') || {}).checked;
            const auto = host.querySelector('.update-channel-auto'); if (auto) auto.hidden = !agentic;
            if (agentic && s.waiting.length) host.querySelector('.update-channel').after(waitingBlock(s));
        };
        host.querySelector('.update-channel').addEventListener('change', sync); sync();
    }
    // Through Coral Hub the switch is not the hub's to change (the machine's owner sets it on that computer), so it is not drawn there
    const grid = mk('dl', 'info-grid settings-grid');
    const reason = unmanaged ? (s.installKind === 'homebrew' ? 'Homebrew manages updates.' : 'This install cannot update itself.') : '';
    if (serverHas.autoPref) grid.append(row('settings-update-auto', 'Check for updates automatically', unmanaged ? reason : 'Downloads in the background and checks the release is genuine. The update installs when you relaunch Coral, never while you work. Only the latest releases of Coral\'s own GitHub repository are used.', unmanaged ? false : s.auto !== false, unmanaged));
    // The Coral Hub relaunch switch lives in Settings > Network and Hub (with the rest of what the hub may do here); this pane only says where it is
    if (hubOn !== null && !unmanaged) {
        const r = mk('div', 'settings-row'); const dt = mk('dt', null, 'Relaunch from Coral Hub'); const dd = mk('dd');
        dd.append(mk('span', 'toggle-desc', `${hubOn ? 'On' : 'Off'}. `));
        const go = mk('button', 'btn btn-small', 'Change in Network and Hub'); go.type = 'button';
        go.addEventListener('click', () => window.showSettingsModal && window.showSettingsModal('network'));
        dd.append(go); r.append(dt, dd); grid.append(r);
    }
    if (!isCloud() && grid.children.length) host.append(grid);
    const facts = mk('dl', 'info-grid settings-grid update-facts');
    const fact = (k, v) => { const r = mk('div', 'settings-row'); r.append(mk('dt', null, k), mk('dd', null, v)); facts.append(r); };
    if (s.checkedAt) fact('Last checked', stamp(s.checkedAt));
    fact('Version', s.installed || 'unknown');
    if (s.last && s.last.result === 'relaunched' && /^hub:/.test(s.last.by) && stamp(s.last.at)) fact('Last update', `Relaunched from Coral Hub at ${stamp(s.last.at)}`); // the time, never the hub user's name
    host.append(facts);
    host.append(mk('p', 'update-calm update-privacy', 'Coral contacts GitHub only, to read the release list and download the package.'));
}

/** Save what the pane changed, with Apply: "Check for updates automatically" and the update channel (each only when it changed; a no-op when the pane was not drawn). Returns true when something was saved. */
export async function applyUpdatePrefs() {
    const s = getUpdateStatus();
    if (isCloud() || !s) return false;
    let saved = false;
    const auto = document.getElementById('settings-update-auto');
    if (auto && !auto.disabled && auto.checked !== (s.auto !== false)) { await savePrefs({ auto: auto.checked }, { auto: s.auto }); saved = true; }
    const picked = document.querySelector('input[name="update-channel"]:checked');
    const channelChanged = !!(s.hasChannel && picked && picked.value !== s.channel);
    if (channelChanged) { await saveChannel(picked.value); saved = true; }
    if (saved) await refreshUpdateStatus();
    if (channelChanged && body()) { await showUpdatesPane(); if (picked.value === 'agentic') redrawWhenFactsArrive(0); } // the pane shows the new channel's facts now (#5561)
    return saved;
}

// The server answers the first status after a switch to Agentic from a cold cache: the channel only, its release list a moment later. Read it again a few times, and redraw while nothing is pending in the pane.
const RETRY_MS = [2000, 5000, 12000];
function redrawWhenFactsArrive(i) {
    if (i >= RETRY_MS.length) return;
    setTimeout(async () => {
        const cur = getUpdateStatus();
        if (!body() || !cur || cur.channel !== 'agentic' || cur.approved) return; // the pane is gone, the choice changed, or the facts are here
        await refreshUpdateStatus();
        const picked = document.querySelector('input[name="update-channel"]:checked');
        if (body() && picked && picked.value === 'agentic') await showUpdatesPane();
        redrawWhenFactsArrive(i + 1);
    }, RETRY_MS[i]);
}
