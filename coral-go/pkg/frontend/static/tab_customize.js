/* Customize the side panel's tab row (#4103, operator request): hide or show each tab and reorder
 * them, from a small popover opened by the tune button at the end of the row or a right-click on
 * the row (a bottom sheet on a phone). The choice is kept per user in this browser (localStorage)
 * and applies to every agent; the agent's tabs and the team's tabs keep separate lists, since they
 * show in different contexts.
 *
 * The tab buttons stay the real ones (their counts and handlers move with them): a hidden tab only
 * gets .tab-hidden, and the order is the DOM order. The locked tab (Chat when the agent is on a
 * board, else the context's first tab) can't be hidden, so the row is never empty, and a hidden tab
 * that is open falls back to it. */
import { registerActions } from './actions.js';
import { escapeHtml, escapeAttr } from './utils.js';

const KEY = 'coral-agentic-tab-prefs';
const empty = () => ({ order: [], hidden: [] });
let prefs = { agent: empty(), team: empty() };
let defaultOrder = null; // every customizable tab id in the page's own order
let dragId = null;

const strip = () => document.querySelector('#agentic-block-top .agentic-state-tabs');
const tabButtons = () => [...(strip()?.querySelectorAll('.agentic-tab[data-tab]') || [])];
const contextOf = () => (document.getElementById('agentic-state')?.classList.contains('team-context') ? 'team' : 'agent');

function load() {
    try {
        const p = JSON.parse(localStorage.getItem(KEY) || 'null');
        for (const ctx of ['agent', 'team']) {
            const c = p && p[ctx];
            prefs[ctx] = {
                order: Array.isArray(c?.order) ? c.order.filter(x => typeof x === 'string') : [],
                hidden: Array.isArray(c?.hidden) ? c.hidden.filter(x => typeof x === 'string') : [],
            };
        }
    } catch { /* unreadable: the defaults */ }
}

function save() {
    try { localStorage.setItem(KEY, JSON.stringify(prefs)); } catch { /* private mode: lasts until reload */ }
}

/** The tabs the current context shows, in the page's own order: the agent's, or the team's. */
function available(ctx) {
    return tabButtons().filter(b => {
        const scope = b.dataset.scope || '';
        if (ctx === 'team' ? scope === 'agent' : scope === 'team') return false;
        if (b.dataset.tab === 'board') return ctx === 'agent' && b.style.display !== 'none'; // a team has its chat in the centre
        return true;
    });
}

const byDefault = ids => ids.slice().sort((a, b) => defaultOrder.indexOf(a) - defaultOrder.indexOf(b));

function lockedId(ids) {
    return ids.includes('board') ? 'board' : byDefault(ids)[0];
}

/** The ids in the user's order: their saved order first, then any tab they have not placed. */
function orderedIds(ctx, ids) {
    const saved = prefs[ctx].order.filter(id => ids.includes(id));
    return [...saved, ...byDefault(ids.filter(id => !saved.includes(id)))];
}

/** Put the saved choice on the real tab row. Safe to call at any time; it only changes what differs. */
export function applyTabPrefs() {
    const bar = strip();
    if (!bar) return;
    if (!defaultOrder) defaultOrder = tabButtons().map(b => b.dataset.tab);
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
    const avail = available(ctx);
    const ids = orderedIds(ctx, avail.map(b => b.dataset.tab));
    const locked = lockedId(ids);
    const hidden = new Set(prefs[ctx].hidden);
    return ids.map((id, i) => {
        const b = document.getElementById(`agentic-tab-${id}`);
        const name = b.getAttribute('aria-label') || id;
        const icon = b.querySelector('.material-icons')?.textContent || '';
        const isLocked = id === locked;
        const on = isLocked || !hidden.has(id);
        return `<li class="tc-row" draggable="true" data-tab="${escapeAttr(id)}">
            <span class="material-icons tc-grip" aria-hidden="true">drag_indicator</span>
            <label class="tc-label"><input type="checkbox" class="tc-check" data-tab="${escapeAttr(id)}"${on ? ' checked' : ''}${isLocked ? ' disabled' : ''}
                aria-label="Show ${escapeAttr(name)}${isLocked ? ' (always shown)' : ''}">
                <span class="material-icons tc-icon" aria-hidden="true">${escapeHtml(icon)}</span><span class="tc-name">${escapeHtml(name)}</span></label>
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

function place(pop) {
    const btn = document.getElementById('agentic-tab-customize');
    if (!btn || window.matchMedia('(max-width: 767px)').matches) return; // the sheet is fixed to the bottom by CSS
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
}

function setShown(id, shown) {
    const ctx = contextOf();
    const hidden = new Set(prefs[ctx].hidden);
    if (shown) hidden.delete(id); else hidden.add(id);
    prefs[ctx].hidden = [...hidden];
    save();
    applyTabPrefs();
    render(`.tc-check[data-tab="${CSS.escape(id)}"]`);
}

function reset() {
    prefs[contextOf()] = empty();
    save();
    applyTabPrefs();
    render('.tc-reset');
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

export function closeCustomize(refocus) {
    popover()?.remove();
    document.removeEventListener('click', onOutside, true);
    document.removeEventListener('keydown', onDocKey, true);
    const btn = document.getElementById('agentic-tab-customize');
    btn?.setAttribute('aria-expanded', 'false');
    if (refocus) btn?.focus();
}

export function openCustomize() {
    if (isOpen()) return;
    const pop = document.createElement('div');
    pop.id = 'tab-customize';
    pop.className = 'tab-customize';
    pop.setAttribute('role', 'dialog');
    pop.setAttribute('aria-labelledby', 'tab-customize-title');
    pop.innerHTML = `<h3 id="tab-customize-title" class="tc-title">Customize tabs</h3>
        <ul class="tc-list"></ul>
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
    load();
    applyTabPrefs();
    // The row changes with the context (agent or team) and with whether the agent is on a board.
    const state = document.getElementById('agentic-state');
    if (state) new MutationObserver(applyTabPrefs).observe(state, { attributes: true, attributeFilter: ['class'] });
    const board = document.getElementById('agentic-tab-board');
    if (board) new MutationObserver(applyTabPrefs).observe(board, { attributes: true, attributeFilter: ['style'] });
}
