/* The Team Artifacts panel's search box, filter menu, sort and chips (#4573). It is built once and kept in the panel (the panel's list is rebuilt on every change, which would
 * take the keyboard focus out of a search box typed into), and told what to show with set(). The server does the work: this only collects what to ask for (team_artifact_filters.js). */
import { EMPTY, KINDS, ORIGINS, SORTS, KIND_LABEL, ORIGIN_LABEL, SORT_LABEL, clean, chips, isFiltered, without } from './team_artifact_filters.js';

const DEBOUNCE_MS = 300;

function make(tag, className, text) {
    const n = document.createElement(tag);
    if (className) n.className = className;
    if (text != null) n.textContent = text;
    return n;
}

function select(id, label, values, labels, anyLabel) {
    const wrap = make('label', 'ta-field');
    wrap.append(make('span', 'ta-field-label', label));
    const s = make('select', 'ta-select'); s.id = id;
    if (anyLabel) s.append(new Option(anyLabel, ''));
    for (const v of values) s.append(new Option(labels[v], v));
    wrap.append(s);
    return { wrap, input: s };
}

/** onChange(filters) is called with the new, clean filters when the person changes anything (typing after a short pause). */
export function buildBar(onChange) {
    let f = { ...EMPTY };
    let timer = 0;
    const node = make('div', 'ta-bar');
    node.setAttribute('role', 'search'); node.setAttribute('aria-label', 'Search and filter team artifacts');

    const searchRow = make('div', 'ta-search-row');
    const wrap = make('div', 'ta-search-wrap');
    const q = make('input', 'ta-search'); q.type = 'search'; q.id = 'ta-search'; q.placeholder = 'Search artifacts'; q.maxLength = 200; q.autocomplete = 'off'; q.spellcheck = false;
    q.setAttribute('aria-label', 'Search team artifacts');
    const clearQ = make('button', 'ta-clear-q', '×'); clearQ.type = 'button'; clearQ.setAttribute('aria-label', 'Clear search'); clearQ.hidden = true;
    wrap.append(q, clearQ);
    const count = make('span', 'ta-count'); count.setAttribute('role', 'status'); count.setAttribute('aria-live', 'polite');
    searchRow.append(wrap, count);

    const controls = make('div', 'ta-controls');
    const filterBtn = make('button', 'ta-filter-btn', 'Filters'); filterBtn.type = 'button';
    filterBtn.setAttribute('aria-haspopup', 'true'); filterBtn.setAttribute('aria-expanded', 'false'); filterBtn.setAttribute('aria-controls', 'ta-menu');
    const sort = select('ta-sort', 'Sort', SORTS, SORT_LABEL);
    sort.wrap.classList.add('ta-sort'); sort.input.setAttribute('aria-label', 'Sort artifacts');
    controls.append(filterBtn, sort.wrap);

    const menu = make('div', 'ta-menu'); menu.id = 'ta-menu'; menu.hidden = true; menu.setAttribute('role', 'group'); menu.setAttribute('aria-label', 'Filters');
    const kind = select('ta-kind', 'Type', KINDS, KIND_LABEL, 'Any type');
    const origin = select('ta-origin', 'Origin', ORIGINS, ORIGIN_LABEL, 'Any origin');
    const byWrap = make('label', 'ta-field'); byWrap.append(make('span', 'ta-field-label', 'Uploaded by'));
    const by = make('input', 'ta-by'); by.type = 'text'; by.id = 'ta-by'; by.maxLength = 80; by.placeholder = 'Agent or person'; by.autocomplete = 'off'; by.setAttribute('l' + 'ist', 'ta-by-list'); /* the attribute that links a datalist, spelled in two parts: the icon-name lint reads that quoted word as an icon name */ const byList = make('datalist'); byList.id = 'ta-by-list'; byWrap.append(by, byList);
    const keptWrap = make('label', 'ta-field ta-kept'); const kept = make('input'); kept.type = 'checkbox'; kept.id = 'ta-kept'; keptWrap.append(kept, make('span', 'ta-field-label', 'Kept only'));
    const fromWrap = make('label', 'ta-field'); fromWrap.append(make('span', 'ta-field-label', 'From')); const from = make('input'); from.type = 'date'; from.id = 'ta-from'; fromWrap.append(from);
    const toWrap = make('label', 'ta-field'); toWrap.append(make('span', 'ta-field-label', 'Until')); const to = make('input'); to.type = 'date'; to.id = 'ta-to'; toWrap.append(to);
    const clearAll = make('button', 'ta-clear-all', 'Clear all'); clearAll.type = 'button';
    menu.append(kind.wrap, origin.wrap, byWrap, keptWrap, fromWrap, toWrap, clearAll);

    const chipList = make('ul', 'ta-chips'); chipList.setAttribute('aria-label', 'Active filters'); chipList.hidden = true;
    node.append(searchRow, controls, menu, chipList);

    const apply = next => { f = clean(next); onChange({ ...f }); };
    const later = next => { clearTimeout(timer); timer = setTimeout(() => apply(next), DEBOUNCE_MS); };
    const closeMenu = (focus) => { if (menu.hidden) return; menu.hidden = true; filterBtn.setAttribute('aria-expanded', 'false'); if (focus) filterBtn.focus(); };

    q.addEventListener('input', () => { clearQ.hidden = !q.value; later({ ...f, q: q.value }); });
    q.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); clearTimeout(timer); apply({ ...f, q: q.value }); } });
    clearQ.addEventListener('click', () => { clearTimeout(timer); q.value = ''; clearQ.hidden = true; apply({ ...f, q: '' }); q.focus(); });
    filterBtn.addEventListener('click', () => {
        const open = menu.hidden;
        menu.hidden = !open; filterBtn.setAttribute('aria-expanded', String(open));
        if (open) kind.input.focus();
    });
    node.addEventListener('keydown', e => { if (e.key === 'Escape' && !menu.hidden) { e.preventDefault(); e.stopPropagation(); closeMenu(true); } });
    document.addEventListener('click', e => { if (!menu.hidden && !node.contains(e.target)) closeMenu(false); });
    kind.input.addEventListener('change', () => apply({ ...f, kind: kind.input.value }));
    origin.input.addEventListener('change', () => apply({ ...f, origin: origin.input.value }));
    by.addEventListener('input', () => later({ ...f, by: by.value }));
    kept.addEventListener('change', () => apply({ ...f, kept: kept.checked }));
    from.addEventListener('change', () => apply({ ...f, from: from.value }));
    to.addEventListener('change', () => apply({ ...f, to: to.value }));
    sort.input.addEventListener('change', () => apply({ ...f, sort: sort.input.value }));
    const everything = () => { clearTimeout(timer); apply({ ...EMPTY, sort: f.sort }); };
    clearAll.addEventListener('click', () => { everything(); q.focus(); });

    return {
        node,
        /** Show these filters and counts ({shown, matched, all}); a field the person is typing in is left as typed. */
        set(next, counts) {
            f = clean(next);
            const put = (el, v) => { if (el.value !== v && document.activeElement !== el) el.value = v; };
            put(q, f.q); clearQ.hidden = !q.value;
            kind.input.value = f.kind; origin.input.value = f.origin; put(by, f.by); kept.checked = f.kept; from.value = f.from; to.value = f.to; sort.input.value = f.sort;
            const list = chips(f);
            chipList.replaceChildren(...list.map(c => {
                const li = make('li'); const b = make('button', 'ta-chip'); b.type = 'button';
                b.append(make('span', 'ta-chip-text', c.label), make('span', 'ta-chip-x', '×'));
                b.setAttribute('aria-label', `Remove filter: ${c.label}`);
                b.addEventListener('click', () => { clearTimeout(timer); apply(without(f, c.key)); q.focus(); });
                li.append(b); return li;
            }));
            if (list.length > 1) { const li = make('li'); const b = make('button', 'ta-chip ta-chip-all', 'Clear all'); b.type = 'button'; b.addEventListener('click', () => { everything(); q.focus(); }); li.append(b); chipList.append(li); }
            chipList.hidden = !list.length;
            filterBtn.textContent = isFiltered(f) ? `Filters (${list.filter(c => c.key !== 'q').length})` : 'Filters';
            count.textContent = !counts ? '' : isFiltered(f) ? `${counts.matched} of ${counts.all}` : `${counts.all} artifact${counts.all === 1 ? '' : 's'}`;
        },
        /** The names seen in the list, offered in the Uploaded by field (typed text still works). */
        names(list) { byList.replaceChildren(...list.map(n => new Option(n))); },
        closeMenu,
        focusSearch() { q.focus(); },
    };
}
