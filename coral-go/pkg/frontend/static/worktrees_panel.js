/* Kept worktrees, in Settings > Storage and cleanup (#6743, the dashboard side of #6736). A stop removes a team's (or a job run's) git worktree only when it holds nothing of yours, and keeps it otherwise
 * (#6701), so kept ones pile up: this lists them (GET /api/worktrees: id, kind, name, path, repo_path, branch, dirty, unmerged, in_use) with what keeps each, and Remove asks Coral to remove one the same
 * safe way (POST /api/worktrees/remove {id}: never forced, an id from the list only). The answer {state, path, branch, reason} is said in the status that is always in the page, and the list is read again.
 * Remove is this computer's dashboard only: through Coral Hub the list shows and Remove does not (a Hub viewer must not delete folders on the owner's computer). */
import { isCloud } from './server_base.js';
import { showToast } from './utils.js';
import { worktreeAnswerWords } from './worktree_words.js';

const $ = id => document.getElementById(id);
let busy = false;

const say = text => { const s = $('wt-status'); if (s && s.textContent !== text) s.textContent = text; };

/** What keeps a worktree from being removed, as badge words (text, never colour alone). */
export function worktreeBadges(w) {
    const out = [];
    if (w.in_use) out.push({ text: 'In use', why: 'A running agent is working in it.' });
    if (w.dirty) out.push({ text: 'Unsaved changes', why: 'It has files that are not committed.' });
    if (w.unmerged) out.push({ text: 'Unmerged commits', why: 'Its branch has commits that are not merged anywhere.' });
    if (w.checked === false) out.push({ text: 'Not checked', why: 'Coral could not check it in time, so it may hold unsaved work. It is never shown as clean.' });
    return out;
}

function item(w, canRemove) {
    const li = document.createElement('li'); li.className = 'wt-item'; li.dataset.id = String(w.id);
    const head = document.createElement('div'); head.className = 'wt-head';
    const name = document.createElement('strong'); name.className = 'wt-name'; name.textContent = w.name || '(unnamed)';
    const kind = document.createElement('span'); kind.className = 'wt-kind'; kind.textContent = w.kind === 'job' ? 'job run' : 'team';
    head.append(name, ' ', kind);
    const meta = document.createElement('div'); meta.className = 'wt-meta';
    const parts = [];
    if (w.branch) { const b = document.createElement('code'); b.textContent = w.branch; parts.push('Branch ', b); }
    if (w.repo_path) { if (parts.length) parts.push(' · '); parts.push(`repository ${w.repo_path}`); }
    meta.append(...parts);
    const where = document.createElement('div'); where.className = 'wt-path'; where.textContent = w.path || '';
    const badges = worktreeBadges(w);
    const bs = document.createElement('div'); bs.className = 'wt-badges';
    for (const b of badges) { const s = document.createElement('span'); s.className = 'wt-badge'; s.textContent = b.text; s.title = b.why; bs.append(s); }
    li.append(head, meta, where);
    if (badges.length) li.append(bs);
    if (canRemove && !w.in_use) {
        // a row that holds work (or was not checked) is only removed when git finds nothing left: the button says so where the person decides
        const held = w.dirty || w.unmerged || w.checked === false;
        const rm = document.createElement('button'); rm.type = 'button'; rm.className = 'btn btn-small wt-remove'; rm.textContent = held ? 'Remove if clean' : 'Remove';
        rm.setAttribute('aria-label', `Remove the worktree of ${w.name || 'this team'}${held ? ' if it is clean' : ''}`);
        rm.addEventListener('click', () => removeOne(w, rm));
        li.append(rm);
    }
    return li;
}

async function removeOne(w, btn) {
    if (busy) return;
    busy = true; btn.disabled = true;
    try {
        const r = await fetch('/api/worktrees/remove', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: w.id }) });
        const d = await r.json().catch(() => null);
        if (!r.ok || !d || (!d.state && d.error)) { say((d && d.error) || `Could not remove it (HTTP ${r.status}).`); btn.disabled = false; return; }
        const words = worktreeAnswerWords(d, 'worktree');
        say(words ? words.text : 'Nothing to remove.');
        if (words && words.error) showToast(words.text, true);
    } catch { say('Could not reach Coral to remove it.'); btn.disabled = false; return; } finally { busy = false; }
    const order = [...document.querySelectorAll('#wt-list .wt-item')].map(li => li.dataset.id);
    await loadWorktrees({ keepSaid: true, canRemove: true }); // Remove only exists where it can be pressed
    focusAfter(String(w.id), order);
}

/** The pressed Remove is gone once the list is drawn again: keyboard focus goes to the same row's Remove if it stayed, else the next row's, else the one before, else the Kept worktrees title. */
function focusAfter(id, order) {
    const list = $('wt-list'); if (!list) return;
    const at = order.indexOf(id), tryRow = rid => { const b = list.querySelector(`.wt-item[data-id="${CSS.escape(rid)}"] .wt-remove`); if (b) { b.focus(); return true; } return false; };
    if (tryRow(id)) return;
    for (const rid of order.slice(at + 1)) if (tryRow(rid)) return;
    for (const rid of order.slice(0, Math.max(at, 0)).reverse()) if (tryRow(rid)) return;
    const t = $('wt-title'); if (t) { t.tabIndex = -1; t.focus(); }
}

/** Read the kept worktrees and draw them. Safe to call when the pane is not on screen. */
export async function loadWorktrees({ keepSaid = false, canRemove = !isCloud() } = {}) {
    const list = $('wt-list'), empty = $('wt-empty');
    if (!list) return;
    if (!keepSaid) say('');
    try {
        const r = await fetch('/api/worktrees');
        if (r.status === 403 || r.status === 404) { list.replaceChildren(); if (empty) { empty.hidden = false; empty.textContent = r.status === 403 ? 'Your access does not include the list of worktrees.' : 'This Coral does not list worktrees.'; } return; }
        const d = await r.json();
        // 'None kept.' is short on purpose (the Settings text budget): the row's description says a stop removes what holds nothing of yours
        const rows = Array.isArray(d) ? d : (Array.isArray(d.worktrees) ? d.worktrees : []);
        list.replaceChildren(...rows.map(w => item(w, canRemove)));
        if (empty) { empty.hidden = rows.length > 0; empty.textContent = 'None kept.'; }
    } catch {
        list.replaceChildren();
        if (empty) { empty.hidden = false; empty.textContent = "Couldn't read the kept worktrees."; }
    }
    const note = $('wt-hub-note'); if (note) note.hidden = canRemove || !list.children.length;
}
