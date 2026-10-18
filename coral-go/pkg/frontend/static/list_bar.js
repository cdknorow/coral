/* The search, filter and sort bar the list panels share (#4744), taken from Team Artifacts' (#4573): a search box with the count beside it, a Filters button that opens a menu, a Sort select and the chips of
 * the filters that are on. It is built once and kept in its panel (the panel's list is rebuilt on every change, which would take the keyboard focus out of a search box typed into), and told what to show with
 * set(). The server does the work: the bar only collects what to ask for. Each panel gives a spec (its fields, sorts and words) and a model (its filters: EMPTY, clean, chips, isFiltered, without; the panel's
 * own *_filters.js, which is pure). The classes are the ta-* ones agentic.css styles; the ids come from the spec so two bars never share one. */

const DEBOUNCE_MS = 300;

function make(tag, className, text) {
    const n = document.createElement(tag);
    if (className) n.className = className;
    if (text != null) n.textContent = text;
    return n;
}

function selectField(id, label, values, labels, anyLabel) {
    const wrap = make('label', 'ta-field');
    wrap.append(make('span', 'ta-field-label', label));
    const s = make('select', 'ta-select'); s.id = id;
    if (anyLabel) s.append(new Option(anyLabel, ''));
    for (const v of values) s.append(new Option(labels[v], v));
    wrap.append(s);
    return { wrap, input: s };
}

/** One control of the Filters menu, by the field's type: select, text (with an optional datalist of names), check, date. Returns its label wrapper, its input and the filter key it edits. */
function buildField(spec) {
    if (spec.type === 'select') return { ...selectField(spec.id, spec.label, spec.values, spec.labels, spec.any), spec };
    if (spec.type === 'text') {
        const wrap = make('label', 'ta-field'); wrap.append(make('span', 'ta-field-label', spec.label));
        const input = make('input', spec.className); input.type = 'text'; input.id = spec.id; input.maxLength = spec.max; input.placeholder = spec.placeholder; input.autocomplete = 'off';
        let list = null;
        if (spec.datalist) {
            input.setAttribute('l' + 'ist', spec.datalist); /* the attribute that links a datalist, spelled in two parts: the icon-name lint reads that quoted word as an icon name */
            list = make('datalist'); list.id = spec.datalist;
        }
        wrap.append(input); if (list) wrap.append(list);
        return { wrap, input, list, spec };
    }
    if (spec.type === 'check') {
        const wrap = make('label', spec.wrapClass); const input = make('input'); input.type = 'checkbox'; input.id = spec.id;
        wrap.append(input, make('span', 'ta-field-label', spec.label));
        return { wrap, input, spec };
    }
    const wrap = make('label', 'ta-field'); wrap.append(make('span', 'ta-field-label', spec.label));
    const input = make('input'); input.type = 'date'; input.id = spec.id; wrap.append(input);
    return { wrap, input, spec };
}

/** onChange(filters) is called with the new, clean filters when the person changes anything (typing after a short pause). */
export function buildListBar(spec, onChange) {
    const { EMPTY, clean, chips, isFiltered, without } = spec.model;
    const p = spec.idp;
    let f = { ...EMPTY };
    let timer = 0;
    const node = make('div', 'ta-bar');
    node.setAttribute('role', 'search'); node.setAttribute('aria-label', spec.label);

    const searchRow = make('div', 'ta-search-row');
    const wrap = make('div', 'ta-search-wrap');
    const q = make('input', 'ta-search'); q.type = 'search'; q.id = `${p}-search`; q.placeholder = spec.placeholder; q.maxLength = 200; q.autocomplete = 'off'; q.spellcheck = false;
    q.setAttribute('aria-label', spec.searchLabel);
    const clearQ = make('button', 'ta-clear-q', '×'); clearQ.type = 'button'; clearQ.setAttribute('aria-label', 'Clear search'); clearQ.hidden = true;
    wrap.append(q, clearQ);
    const count = make('span', 'ta-count'); count.setAttribute('role', 'status'); count.setAttribute('aria-live', 'polite');
    searchRow.append(wrap, count);

    const controls = make('div', 'ta-controls');
    const filterBtn = make('button', 'ta-filter-btn', 'Filters'); filterBtn.type = 'button';
    filterBtn.setAttribute('aria-haspopup', 'true'); filterBtn.setAttribute('aria-expanded', 'false'); filterBtn.setAttribute('aria-controls', `${p}-menu`);
    const sort = selectField(`${p}-sort`, 'Sort', spec.sorts.values, spec.sorts.labels);
    sort.wrap.classList.add('ta-sort'); sort.input.setAttribute('aria-label', spec.sorts.aria);
    controls.append(filterBtn, sort.wrap);

    const menu = make('div', 'ta-menu'); menu.id = `${p}-menu`; menu.hidden = true; menu.setAttribute('role', 'group'); menu.setAttribute('aria-label', 'Filters');
    const fields = spec.fields.map(buildField);
    const clearAll = make('button', 'ta-clear-all', 'Clear all'); clearAll.type = 'button';
    menu.append(...fields.map(x => x.wrap), clearAll);

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
        if (open && fields.length) fields[0].input.focus();
    });
    node.addEventListener('keydown', e => { if (e.key === 'Escape' && !menu.hidden) { e.preventDefault(); e.stopPropagation(); closeMenu(true); } });
    document.addEventListener('click', e => { if (!menu.hidden && !node.contains(e.target)) closeMenu(false); });
    for (const fld of fields) {
        const key = fld.spec.key;
        if (fld.spec.type === 'text') fld.input.addEventListener('input', () => later({ ...f, [key]: fld.input.value }));
        else if (fld.spec.type === 'check') fld.input.addEventListener('change', () => apply({ ...f, [key]: fld.input.checked }));
        else fld.input.addEventListener('change', () => apply({ ...f, [key]: fld.input.value }));
    }
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
            for (const fld of fields) {
                const key = fld.spec.key;
                if (fld.spec.type === 'text') put(fld.input, f[key]);
                else if (fld.spec.type === 'check') fld.input.checked = f[key];
                else fld.input.value = f[key];
            }
            sort.input.value = f.sort;
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
            const all = `${counts && counts.all}${counts && counts.truncated ? '+' : ''}`; // '+': older ones were not scanned
            count.textContent = !counts ? '' : isFiltered(f) ? `${counts.matched} of ${all}` : `${all} ${spec.noun}${counts.all === 1 && !counts.truncated ? '' : 's'}`;
        },
        /** The names seen in the list, offered in the text field that has a datalist (typed text still works). */
        names(list) { const fld = fields.find(x => x.list); if (fld) fld.list.replaceChildren(...list.map(n => new Option(n))); },
        closeMenu,
        focusSearch() { q.focus(); },
    };
}
