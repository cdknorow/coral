/* Settings > Updates (#5275, design #5270): one status card (a dot, a line, a sentence, the action), the two switches, when it last looked, the version, and where Coral reaches. The pane is
 * drawn from the status each time it is shown. The switches are saved with Apply like the rest of Settings (applyUpdatePrefs); the card's buttons act at once: "Relaunch to update" opens the
 * confirmation (a dialog over this one), "Check now" asks the updater to look. A Homebrew or unsupported install says so and why, with the command to run. */
import { importOnce } from './import_once.js';
import { copyText, showToast } from './utils.js';
import { isCloud } from './server_base.js';
const { ageTier, reasonText, fetchHubRelaunch, requestCheckNow, savePrefs, serverHas } = await importOnce('./update_api.js');
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
        if (!s.relaunch.available) { act.setAttribute('aria-disabled', 'true'); sub.textContent += ` Not available right now: ${reasonText(s.relaunch.reason)}`; }
        act.addEventListener('click', async () => { if (act.getAttribute('aria-disabled') === 'true') return; (await importOnce('./update_dialog.js')).openUpdateDialog(getUpdateStatus()); });
    } else if (s.progress !== null) {
        title.textContent = `Downloading Coral${s.staged && s.staged.version ? ' ' + s.staged.version : ''}`; sub.textContent = `${s.progress} percent`; dot.dataset.k = 'ok'; c.dataset.k = 'ok';
        const bar = mk('div', 'update-bar'); bar.setAttribute('role', 'progressbar'); bar.setAttribute('aria-valuemin', '0'); bar.setAttribute('aria-valuemax', '100'); bar.setAttribute('aria-valuenow', String(s.progress)); bar.setAttribute('aria-label', 'Download');
        const fill = mk('span'); fill.style.width = `${s.progress}%`; bar.append(fill); text.append(bar);
    } else {
        title.textContent = 'Coral is up to date'; sub.textContent = `You are on ${s.installed}.`; dot.dataset.k = 'ok'; c.dataset.k = 'ok';
        if (serverHas.check) act = mk('button', 'btn update-act', 'Check now');
        if (act) act.type = 'button';
        if (act) act.addEventListener('click', async () => { act.disabled = true; const ok = await requestCheckNow(); await refreshUpdateStatus(); showUpdatesPane(); if (!ok) showToast('Could not check for updates.', true); });
    }
    c.append(dot, text); if (act) c.append(act);
    return c;
}

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
    } else {
        t.append(mk('b', null, 'This install cannot update itself'), mk('p', null, 'Coral can only update installs it can replace safely (the app, the installer or the archive from the releases page).'));
        const a = mk('a', 'update-link', 'Open the releases page'); a.href = 'https://github.com/cdknorow/coral/releases'; a.target = '_blank'; a.rel = 'noopener noreferrer'; t.append(a);
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

/** Draw the pane from the last status (and ask for a fresh one). */
export async function showUpdatesPane() {
    const host = body();
    if (!host) return;
    const s = getUpdateStatus() || await refreshUpdateStatus();
    hubOn = await fetchHubRelaunch();
    host.replaceChildren();
    if (!s || !s.enabled) { host.append(mk('p', 'update-calm', 'Updates are not available in this Coral.')); return; }
    const unmanaged = s.installKind !== '';
    host.append(unmanaged ? managed(s) : card(s));
    // Through Coral Hub the switches are not the hub's to change (the machine's owner sets them on that computer), so they are not drawn there
    const grid = mk('dl', 'info-grid settings-grid');
    const reason = unmanaged ? (s.installKind === 'homebrew' ? 'Homebrew manages updates.' : 'This install cannot update itself.') : '';
    if (serverHas.autoPref) grid.append(row('settings-update-auto', 'Download updates automatically', unmanaged ? reason : 'Downloads in the background and checks the release is genuine. The update installs the next time Coral starts, never while you work. Only the latest releases of Coral\'s own GitHub repository are used.', unmanaged ? false : s.auto !== false, unmanaged));
    if (hubOn !== null) grid.append(row('settings-update-hub', 'Let Coral Hub relaunch this Coral to install updates', unmanaged ? reason : 'Coral Hub can ask for the relaunch only; it cannot choose a version, and cannot change this setting.', unmanaged ? false : hubOn, unmanaged));
    if (!isCloud()) host.append(grid);
    const facts = mk('dl', 'info-grid settings-grid update-facts');
    const fact = (k, v) => { const r = mk('div', 'settings-row'); r.append(mk('dt', null, k), mk('dd', null, v)); facts.append(r); };
    if (s.checkedAt) fact('Last checked', stamp(s.checkedAt));
    fact('Version', s.installed || 'unknown');
    if (s.last && s.last.result === 'relaunched' && /^hub:/.test(s.last.by)) fact('Last update', `Relaunched from Coral Hub by ${s.last.by.slice(4)}`);
    host.append(facts);
    host.append(mk('p', 'update-calm update-privacy', 'Coral contacts GitHub only, to read the release list and download the package.'));
}

/** Save the two switches with Apply (a no-op when the pane was not drawn or nothing changed). */
export async function applyUpdatePrefs() {
    const auto = document.getElementById('settings-update-auto'), hub = document.getElementById('settings-update-hub');
    const s = getUpdateStatus();
    if (isCloud() || !s || (!auto && !hub) || (auto || hub).disabled) return;
    const next = { auto: auto ? auto.checked : undefined, hubRelaunch: hub ? hub.checked : undefined };
    if ((!auto || next.auto === (s.auto !== false)) && (!hub || hub.checked === hubOn)) return;
    await savePrefs(next, { auto: s.auto, hubRelaunch: hubOn });
    await refreshUpdateStatus();
}
