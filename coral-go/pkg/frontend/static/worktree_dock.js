/* The kept-worktree notice on a phone (#6859, UI Accessibility's #6851): the floating toast covered the Chat/Terminal toggle and, at 200% text, the whole composer, so on a phone's layout it is a bar in the page's
 * flow instead, at the foot of the main stage (#worktree-dock, the last child of #main-stage: the views above it get what is left, nothing is covered, on every view). One line: "Worktree kept", Copy path,
 * Details and a × (Dismiss); the words (why, path, branch) are behind Details. Several kept worktrees are one bar, "3 worktrees kept", and Details lists each with its own Copy path, so no path is lost.
 * What is new is said once, in the notices' own status (announceNotice: polite for a kept worktree, an alert for one that could not be removed). Everything is text (textContent), never markup. */
import { copyText } from './utils.js';
import { announceNotice } from './notifications.js';

const items = [];   // { text, path, error } newest last
let open = false;   // Details shown

const $ = () => document.getElementById('worktree-dock');

/** Is the text at 150% or more of the default (the page's root size is 24 px or more)? The bar's closed row is then just the title and the x. */
const largeText = () => parseFloat(getComputedStyle(document.documentElement).fontSize) >= 24;

function button(cls, label, text) {
    const b = document.createElement('button'); b.type = 'button'; b.className = `btn btn-small ${cls}`; b.textContent = text; if (label) b.setAttribute('aria-label', label);
    return b;
}

async function copy(path, b) {
    const ok = await copyText(String(path));
    announceNotice(ok ? 'Path copied.' : 'Could not copy the path.', !ok);
    const was = b.dataset.label || b.textContent; b.dataset.label = was;
    b.textContent = ok ? 'Copied' : 'Not copied';
    setTimeout(() => { if (b.isConnected) b.textContent = was; }, 2000);
}

/** The bar's focus goes back to where the person works when it goes (the first agent in the Agents list, else the message box, else the list's search box). */
function giveBack() {
    setTimeout(() => {
        const a = document.activeElement;
        if (a && a !== document.body && a.isConnected) return;
        const row = [...document.querySelectorAll('#live-sessions-list [tabindex="0"], #live-sessions-list button, #live-sessions-list a[href], #mobile-agent-list [tabindex="0"]')].find(e => e.getClientRects().length);
        const box = document.getElementById('command-input');
        const search = [...document.querySelectorAll('input.sidebar-search-input')].find(e => e.getClientRects().length);
        const target = row || (box && box.getClientRects().length ? box : null) || search;
        if (target) target.focus();
    }, 0);
}

export function dismissWorktreeDock() {
    const had = $() && $().contains(document.activeElement);
    items.length = 0; open = false;
    paint();
    if (had) giveBack();
}

function paint() {
    const dock = $();
    if (!dock) return;
    if (!items.length) { dock.hidden = true; dock.replaceChildren(); return; }
    const bad = items.some(i => i.error);
    dock.hidden = false; dock.classList.toggle('error', bad);
    const line = document.createElement('div'); line.className = 'worktree-dock-line';
    const title = items.length === 1 ? (items[0].error ? 'Worktree not removed' : 'Worktree kept') : `${items.length} worktrees ${bad ? 'kept or not removed' : 'kept'}`;
    const big = largeText(); // at large text the closed bar is one row, the title (which opens the details) and the x: Copy path and the words are inside the disclosure
    const toggle = () => { open = !open; const was = document.activeElement && document.activeElement.classList.contains('worktree-dock-details'); paint(); if (was) { const n = $() && $().querySelector('.worktree-dock-details'); if (n) n.focus(); } };
    if (big) {
        const t = button('worktree-dock-details worktree-dock-title', '', title); t.setAttribute('aria-expanded', String(open)); t.setAttribute('aria-controls', 'worktree-dock-details'); t.addEventListener('click', toggle);
        line.append(t);
    } else {
        const t = document.createElement('span'); t.className = 'worktree-dock-title'; t.textContent = title; line.append(t);
        if (items.length === 1) { const c = button('worktree-dock-copy', 'Copy the worktree path', 'Copy path'); c.addEventListener('click', () => copy(items[0].path, c)); line.append(c); }
        const d = button('worktree-dock-details', '', 'Details'); d.setAttribute('aria-expanded', String(open)); d.setAttribute('aria-controls', 'worktree-dock-details'); d.addEventListener('click', toggle);
        line.append(d);
    }
    const x = button('worktree-dock-dismiss', items.length > 1 ? `Dismiss all ${items.length} worktree notices` : 'Dismiss the worktree notice', '×'); x.addEventListener('click', dismissWorktreeDock);
    line.append(x);
    const body = document.createElement('div'); body.id = 'worktree-dock-details'; body.className = 'worktree-dock-body'; body.hidden = !open;
    for (const i of items) {
        const row = document.createElement('div'); row.className = 'worktree-dock-item';
        const t = document.createElement('span'); t.className = 'worktree-dock-text'; t.textContent = i.text; row.append(t);
        if (items.length > 1 || big) { const c = button('worktree-dock-copy', `Copy the path ${i.path}`, 'Copy path'); c.addEventListener('click', () => copy(i.path, c)); row.append(c); }
        body.append(row);
    }
    if (!dock.dataset.keys) { dock.dataset.keys = '1'; dock.addEventListener('keydown', e => { if (e.key === 'Escape') dismissWorktreeDock(); }); }
    dock.replaceChildren(line, body);
}

/** Add a kept (or not removed) worktree to the bar and say it once. `focus`: the answer to the person's own Kill, so the focus goes to the bar's first button (the bar is last in Tab order). */
export function showWorktreeDock({ text, path, error }, focus = true) {
    if (!$()) return false;
    items.push({ text, path, error: !!error });
    paint();
    announceNotice(text, !!error);
    if (focus) {
        const a = document.activeElement;
        if (!a || a === document.body || a.closest('#confirm-modal') || !a.isConnected) { const b = $().querySelector('button'); if (b) b.focus(); }
    }
    return true;
}
