/* Customize the side panel's tab row (#4103, operator request): hide or show each tab and reorder
 * them, from a small popover opened by the tune button at the end of the row or a right-click on
 * the row (a bottom sheet on a phone). The choice is kept per user in this browser (safe_storage.js,
 * under the signed-in account's storeKey) as tab ids only, and applies to every agent; the agent's tabs and the team's tabs keep separate lists, since they
 * show in different contexts.
 *
 * The tab buttons stay the real ones (their counts and handlers move with them): a hidden tab only
 * gets .tab-hidden, and the order is the DOM order. What is read back is checked against the page's own
 * tab ids (unknown and repeated ids dropped, missing ones last). The locked tab (Chat when the agent is on a
 * board, else the context's first tab) can be put anywhere but can't be hidden, so the row is never empty, and a hidden tab
 * that is open falls back to it. */
import { PHONE_QUERY } from './phone_layout.js';
import { registerActions } from './actions.js';
import { escapeHtml, escapeAttr } from './utils.js';
import { browserLocal } from './safe_storage.js';
import { storeKey } from './local_store.js';

const BASE_KEY = 'coral-agentic-tab-prefs'; // kept under storeKey(): on Coral Hub one browser serves several accounts
const empty = () => ({ order: [], hidden: [] });
let prefs = { agent: empty(), team: empty() };
let defaultOrder = null; // every customizable tab id in the page's own order: the ids a stored list may name
let loadedKey = null;
let dragId = null;

const strip = () => document.querySelector('#agentic-block-top .agentic-state-tabs');
const tabButtons = () => [...(strip()?.querySelectorAll('.agentic-tab[data-tab]') || [])];
const contextOf = () => (document.getElementById('agentic-state')?.classList.contains('team-context') ? 'team' : 'agent');

/** A stored list as ids this page knows: strings only, the page's own tab ids, no duplicates. Anything else is dropped (a corrupt or
 *  hand-edited value can't name a tab that doesn't exist, and what is stored is ids, never a title or content). */
function cleanIds(list) {
    if (!Array.isArray(list)) return [];
    const out = [];
    for (const id of list) if (typeof id === 'string' && defaultOrder.includes(id) && !out.includes(id)) out.push(id);
    return out;
}

/** Read the saved choice for the signed-in account (a different account, or the account arriving after start, reads its own). */
function ensureLoaded() {
    const key = storeKey(BASE_KEY);
    if (key === loadedKey) return;
    loadedKey = key;
    prefs = { agent: empty(), team: empty() };
    try {
        const p = JSON.parse(browserLocal.get(key) || 'null');
        for (const ctx of ['agent', 'team']) {
            const c = p && typeof p === 'object' ? p[ctx] : null;
            prefs[ctx] = { order: cleanIds(c?.order), hidden: cleanIds(c?.hidden) };
        }
    } catch { /* unreadable: the defaults */ }
}

/** Keep the choice; with nothing chosen the key is removed, so Reset leaves nothing behind. */
function save() {
    const key = storeKey(BASE_KEY);
    const chosen = ['agent', 'team'].some(ctx => prefs[ctx].order.length || prefs[ctx].hidden.length);
    if (chosen) browserLocal.set(key, JSON.stringify(prefs)); else browserLocal.remove(key);
}

/** The tabs the current context shows, in the page's own order: the agent's, or the team's. */
function available(ctx) {
    return tabButtons().filter(b => {
        const scope = b.dataset.scope || '';
        if (b.dataset.onlyPage) return false; // a tab of another kind of page (Subscribers, on a board) is not the agent page's to order or hide (#4539)
        if (ctx === 'team' ? scope === 'agent' : scope === 'team') return false;
        if (b.hasAttribute('data-local-only') && document.body.classList.contains('cloud-mode')) return false; // hidden by the hosted hub's stylesheet (Saved): a row for it would tick a tab that never appears (#4741)
        if (b.dataset.tab === 'board') return ctx === 'agent' && b.style.display !== 'none'; // a team has its chat in the centre
        return true;
    });
}

const byDefault = ids => ids.slice().sort((a, b) => defaultOrder.indexOf(a) - defaultOrder.indexOf(b));

function lockedId(ids) {
    return ids.includes('board') ? 'board' : byDefault(ids)[0];
}

/** The ids in the user's order: their saved order, then any tab they have not placed. The locked tab (Chat) can sit anywhere the
 *  user put it; one the saved order lacks goes first. */
function orderedIds(ctx, ids) {
    const locked = lockedId(ids);
    const saved = prefs[ctx].order.filter(id => ids.includes(id));
    const rest = byDefault(ids.filter(id => !saved.includes(id)));
    return locked && !saved.includes(locked) ? [locked, ...saved, ...rest.filter(id => id !== locked)] : [...saved, ...rest];
}

/** Put the saved choice on the real tab row. Safe to call at any time; it only changes what differs. */
export function applyTabPrefs() {
    const bar = strip();
    if (!bar) return;
    if (!defaultOrder) defaultOrder = tabButtons().map(b => b.dataset.tab);
    ensureLoaded();
    const ctx = contextOf();
    const avail = available(ctx);
    const ids = avail.map(b => b.dataset.tab);
    const locked = lockedId(ids);
    const hidden = new Set(prefs[ctx].hidden.filter(id => id !== locked));
    for (const b of tabButtons()) b.classList.toggle('tab-hidden', ids.includes(b.dataset.tab) && hidden.has(b.dataset.tab));
    const want = orderedIds(ctx, ids);
    // Moving a focused button would drop its focus, so only reorder when the order differs.
    if (want.join() !== ids.join()) {
        const anchor = document.getElementById('agentic-tab-customize');
        for (const id of want) bar.insertBefore(document.getElementById(`agentic-tab-${id}`), anchor);
    }
    const active = bar.querySelector('.agentic-tab.active');
    if (active?.classList.contains('tab-hidden') && locked) window.switchAgenticTab?.(locked, 'top');
}

/** For switchAgenticTab: a tab the user hid is not opened; the locked tab is. */
export function openableTab(tabName) {
    const b = document.getElementById(`agentic-tab-${tabName}`);
    if (!b?.classList.contains('tab-hidden')) return tabName;
    return lockedId(available(contextOf()).map(x => x.dataset.tab)) || tabName;
}

// ── The popover ────────────────────────────────────────────────────────────

function rowsHTML(ctx) {
    ensureLoaded();
    const avail = available(ctx);
    const ids = orderedIds(ctx, avail.map(b => b.dataset.tab));
    const locked = lockedId(ids);
    const hidden = new Set(prefs[ctx].hidden);
    return ids.map((id, i) => {
        const b = document.getElementById(`agentic-tab-${id}`);
        const name = b.getAttribute('aria-label') || id;
        const glyph = b.querySelector('.material-icons')?.textContent || '';
        const svg = glyph ? null : b.querySelector('svg'); // Saved draws its bookmark as an inline SVG, not a font glyph
        const iconHtml = glyph ? `<span class="material-icons tc-icon" aria-hidden="true">${escapeHtml(glyph)}</span>` : svg ? `<span class="tc-icon tc-icon-svg" aria-hidden="true">${svg.outerHTML}</span>` : '';
        const isLocked = id === locked;
        const on = isLocked || !hidden.has(id);
        return `<li class="tc-row" draggable="true" data-tab="${escapeAttr(id)}">
            <span class="material-icons tc-grip" aria-hidden="true">drag_indicator</span>
            <label class="tc-label"><input type="checkbox" class="tc-check" data-tab="${escapeAttr(id)}"${on ? ' checked' : ''}${isLocked ? ' disabled' : ''}
                aria-label="Show ${escapeAttr(name)}${isLocked ? ' (always shown)' : ''}">
                ${iconHtml}<span class="tc-name">${escapeHtml(name)}</span></label>
            ${isLocked ? '<span class="material-icons tc-lock" aria-hidden="true" title="Always shown">lock</span>' : ''}
            <span class="tc-sp"></span>
            <button type="button" class="tc-move" data-tab="${escapeAttr(id)}" data-dir="-1" aria-label="Move ${escapeAttr(name)} up"${i === 0 ? ' aria-disabled="true"' : ''}><span class="material-icons" aria-hidden="true">keyboard_arrow_up</span></button>
            <button type="button" class="tc-move" data-tab="${escapeAttr(id)}" data-dir="1" aria-label="Move ${escapeAttr(name)} down"${i === ids.length - 1 ? ' aria-disabled="true"' : ''}><span class="material-icons" aria-hidden="true">keyboard_arrow_down</span></button>
        </li>`;
    }).join('');
}

function popover() { return document.getElementById('tab-customize'); }
const isOpen = () => !!popover(); // the DOM is the state, so a removed popover can't leave it stuck open

function render(focus) {
    const pop = popover();
    if (!pop) return;
    pop.querySelector('.tc-list').innerHTML = rowsHTML(contextOf());
    if (focus) pop.querySelector(focus)?.focus();
}

/** Say what a change did, politely, in the popover's own status line: after a move the list is redrawn and focus goes back to the
 *  control that was used, which reads that control's name again but not the result. */
function announce(text) {
    const s = popover()?.querySelector('.tc-status');
    if (s) s.textContent = text;
}

const nameOf = id => document.getElementById(`agentic-tab-${id}`)?.getAttribute('aria-label') || id;

function place(pop) {
    const btn = document.getElementById('agentic-tab-customize');
    if (!btn || window.matchMedia(PHONE_QUERY).matches) return; // the sheet is fixed to the bottom by CSS
    const r = btn.getBoundingClientRect();
    pop.style.top = `${Math.round(r.bottom + 4)}px`;
    pop.style.right = `${Math.max(8, Math.round(window.innerWidth - r.right))}px`;
}

function move(id, dir, refocus) {
    const ctx = contextOf();
    const ids = orderedIds(ctx, available(ctx).map(b => b.dataset.tab));
    const i = ids.indexOf(id);
    const j = i + dir;
    if (i < 0 || j < 0 || j >= ids.length) return;
    [ids[i], ids[j]] = [ids[j], ids[i]];
    prefs[ctx].order = ids;
    save();
    applyTabPrefs();
    render(refocus);
    announce(`${nameOf(id)} moved to position ${j + 1} of ${ids.length}`);
}

function setShown(id, shown) {
    const ctx = contextOf();
    const hidden = new Set(prefs[ctx].hidden);
    if (shown) hidden.delete(id); else hidden.add(id);
    prefs[ctx].hidden = [...hidden];
    save();
    applyTabPrefs();
    render(`.tc-check[data-tab="${CSS.escape(id)}"]`);
    announce(`${nameOf(id)} ${shown ? 'shown' : 'hidden'}`);
}

function reset() {
    prefs[contextOf()] = empty();
    save();
    applyTabPrefs();
    render('.tc-reset');
    announce('Tabs reset to the default');
}

function onOutside(e) {
    const pop = popover();
    if (!pop || pop.contains(e.target) || e.target.closest?.('#agentic-tab-customize')) return;
    closeCustomize(false);
}

function onDocKey(e) {
    if (e.key !== 'Escape' || e.isComposing) return;
    e.preventDefault();
    closeCustomize(true);
}

function closeCustomize(refocus) {
    popover()?.remove();
    document.removeEventListener('click', onOutside, true);
    document.removeEventListener('keydown', onDocKey, true);
    const btn = document.getElementById('agentic-tab-customize');
    btn?.setAttribute('aria-expanded', 'false');
    if (refocus) btn?.focus();
}

function openCustomize() {
    if (isOpen()) return;
    const pop = document.createElement('div');
    pop.id = 'tab-customize';
    pop.className = 'tab-customize';
    pop.setAttribute('role', 'dialog');
    pop.setAttribute('aria-labelledby', 'tab-customize-title');
    pop.innerHTML = `<h3 id="tab-customize-title" class="tc-title">Customize tabs</h3>
        <ul class="tc-list"></ul>
        <div class="tc-status" role="status" aria-live="polite"></div>
        <div class="tc-foot"><span class="tc-note">Saved for you, applies to every agent</span><button type="button" class="btn btn-small tc-reset">Reset to default</button></div>`;
    document.body.appendChild(pop);
    document.getElementById('agentic-tab-customize')?.setAttribute('aria-expanded', 'true');
    render();
    place(pop);

    pop.addEventListener('click', e => {
        const mv = e.target.closest('.tc-move');
        if (mv) { if (mv.getAttribute('aria-disabled') !== 'true') move(mv.dataset.tab, Number(mv.dataset.dir) === 1 ? 1 : -1, `.tc-move[data-tab="${CSS.escape(mv.dataset.tab)}"][data-dir="${Number(mv.dataset.dir) === 1 ? 1 : -1}"]`); else mv.focus(); return; }
        if (e.target.closest('.tc-reset')) reset();
    });
    pop.addEventListener('change', e => {
        const c = e.target.closest('.tc-check');
        if (c) setShown(c.dataset.tab, c.checked);
    });
    // Tab past either end leaves the popover: it closes and focus goes back to the button that opened it, not to the page's start.
    pop.addEventListener('keydown', e => {
        if (e.key !== 'Tab' || e.altKey || e.ctrlKey || e.metaKey) return;
        const stops = [...pop.querySelectorAll('input:not(:disabled), button')];
        if (e.target === stops[e.shiftKey ? 0 : stops.length - 1]) { e.preventDefault(); closeCustomize(true); }
    });
    pop.addEventListener('keydown', e => {
        if (!e.altKey || (e.key !== 'ArrowUp' && e.key !== 'ArrowDown')) return;
        const row = e.target.closest('.tc-row');
        if (!row) return;
        e.preventDefault();
        const id = row.dataset.tab;
        move(id, e.key === 'ArrowUp' ? -1 : 1, `.tc-check[data-tab="${CSS.escape(id)}"]`);
    });
    // Dragging a row (a mouse; the up/down buttons and Alt+arrows are the same thing without one).
    pop.addEventListener('dragstart', e => {
        const row = e.target.closest?.('.tc-row');
        if (!row) return;
        dragId = row.dataset.tab;
        e.dataTransfer.effectAllowed = 'move';
        try { e.dataTransfer.setData('text/plain', dragId); } catch { /* some browsers refuse */ }
        row.classList.add('dragging');
    });
    pop.addEventListener('dragover', e => {
        if (dragId && e.target.closest?.('.tc-row')) e.preventDefault();
    });
    pop.addEventListener('drop', e => {
        const row = e.target.closest?.('.tc-row');
        if (!dragId || !row) return;
        e.preventDefault();
        const ctx = contextOf();
        const ids = orderedIds(ctx, available(ctx).map(b => b.dataset.tab));
        const from = ids.indexOf(dragId);
        const to = ids.indexOf(row.dataset.tab);
        dragId = null;
        if (from < 0 || to < 0 || from === to) return;
        ids.splice(to, 0, ids.splice(from, 1)[0]);
        prefs[ctx].order = ids;
        save();
        applyTabPrefs();
        render();
    });
    pop.addEventListener('dragend', () => { dragId = null; pop.querySelector('.dragging')?.classList.remove('dragging'); });

    // The opening click (or right-click) must not close it at once.
    setTimeout(() => { if (isOpen()) document.addEventListener('click', onOutside, true); }, 0);
    document.addEventListener('keydown', onDocKey, true);
    pop.querySelector('.tc-check:not(:disabled)')?.focus();
}

registerActions({
    'tabs-customize': () => { if (isOpen()) closeCustomize(true); else openCustomize(); },
    'tabs-customize-menu': (el, event) => {
        if (event.target.closest?.('[data-on-contextmenu]') !== el) return; // a tab with its own menu keeps it
        event.preventDefault();
        openCustomize();
    },
});

export function initTabCustomize() {
    applyTabPrefs(); // reads the saved choice for the account
    // The row changes with the context (agent or team) and with whether the agent is on a board.
    const state = document.getElementById('agentic-state');
    if (state) new MutationObserver(applyTabPrefs).observe(state, { attributes: true, attributeFilter: ['class'] });
    const board = document.getElementById('agentic-tab-board');
    if (board) new MutationObserver(applyTabPrefs).observe(board, { attributes: true, attributeFilter: ['style'] });
}
