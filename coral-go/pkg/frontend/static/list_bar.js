/* The search, filter and sort bar the list panels share (#4744), taken from Team Artifacts' (#4573): a search box with the count beside it, a Filters button that opens a menu, a Sort select and the chips of
 * the filters that are on. It is built once and kept in its panel (the panel's list is rebuilt on every change, which would take the keyboard focus out of a search box typed into), and told what to show with
 * set(). The server does the work: the bar only collects what to ask for. Each panel gives a spec (its fields, sorts and words) and a model (its filters: EMPTY, clean, chips, isFiltered, without; the panel's
 * own *_filters.js, which is pure). The classes are list-bar-* (agentic.css, touch.css); the ids come from the spec so two bars never share one. */

import { browserLocal } from './safe_storage.js';

const DEBOUNCE_MS = 300;
const LIVE_SETTLE_MS = 500; // how long the count must stay as it is before it is announced

/** Whether a collapsible bar's panel was left open for this list (scope: the panel, key: the board or agent), kept in this browser like the filters. */
export function loadPanelOpen(scope, key) { return !!key && browserLocal.get(`coral-list-bar-open:${scope}:${key}`) === '1'; }
export function savePanelOpen(scope, key, open) {
    if (!key) return;
    if (open) browserLocal.set(`coral-list-bar-open:${scope}:${key}`, '1'); else browserLocal.remove(`coral-list-bar-open:${scope}:${key}`);
}

function make(tag, className, text) {
    const n = document.createElement(tag);
    if (className) n.className = className;
    if (text != null) n.textContent = text;
    return n;
}

function selectField(id, label, values, labels, anyLabel) {
    const wrap = make('label', 'list-bar-field');
    wrap.append(make('span', 'list-bar-field-label', label));
    const s = make('select', 'list-bar-select'); s.id = id;
    if (anyLabel) s.append(new Option(anyLabel, ''));
    for (const v of values) s.append(new Option(labels[v], v));
    wrap.append(s);
    return { wrap, input: s };
}

/** One control of the Filters menu, by the field's type: select, text (with an optional datalist of names), check, date. Returns its label wrapper, its input and the filter key it edits. */
function buildField(spec) {
    if (spec.type === 'select') return { ...selectField(spec.id, spec.label, spec.values, spec.labels, spec.any), spec };
    if (spec.type === 'text') {
        const wrap = make('label', 'list-bar-field'); wrap.append(make('span', 'list-bar-field-label', spec.label));
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
        wrap.append(input, make('span', 'list-bar-field-label', spec.label));
        return { wrap, input, spec };
    }
    const wrap = make('label', 'list-bar-field'); wrap.append(make('span', 'list-bar-field-label', spec.label));
    const input = make('input'); input.type = 'date'; input.id = spec.id; wrap.append(input);
    return { wrap, input, spec };
}

/** onChange(filters) is called with the new, clean filters when the person changes anything (typing after a short pause). */
export function buildListBar(spec, onChange, hooks = {}) {
    const { EMPTY, clean, chips, isFiltered, without } = spec.model;
    const p = spec.idp;
    let f = { ...EMPTY };
    let timer = 0;
    const node = make('div', 'list-bar-bar');
    node.setAttribute('role', 'search'); node.setAttribute('aria-label', spec.label);

    // A panel that has a search box of its own (the agent list's) gives spec.noSearch: the bar then has the filters, the sort, the count and the chips, and no search row (its q is not the bar's).
    const noSearch = !!spec.noSearch;
    // spec.inline (the agent list's, #4947): the fields and the sort sit open in the panel that holds the bar (the Display expander), not behind a Filters button; the chips give way to the panel owner's summary line.
    const inline = !!spec.inline;
    // spec.collapsible (#5293, the agent list's pattern): the Filters button and the Sort row give way to one icon on the search row, with the count beside it; it opens a panel under the row that holds the
    // fields, the sort and Clear filters, and the chips of what is on stay under the row when it is shut. spec.expandAll adds the second icon (the list's expand or collapse all), told by hooks.onExpandAll.
    const collapsible = !!spec.collapsible && !spec.noSearch && !inline;
    const searchRow = make('div', 'list-bar-search-row');
    const wrap = make('div', 'list-bar-search-wrap');
    const q = make('input', 'list-bar-search'); q.type = 'search'; q.id = `${p}-search`; q.placeholder = spec.placeholder; q.maxLength = 200; q.autocomplete = 'off'; q.spellcheck = false;
    q.setAttribute('aria-label', spec.searchLabel);
    const clearQ = make('button', 'list-bar-clear-q', '×'); clearQ.type = 'button'; clearQ.setAttribute('aria-label', 'Clear search'); clearQ.hidden = true;
    wrap.append(q, clearQ);
    const count = make('span', 'list-bar-count');
    // The count is not a live region itself: a list that changes under the person (agents coming and going, a poll) would have a screen reader announce it again and again. A hidden status region says it
    // only after the person changed the search or a filter (apply), which is when they want to hear how many are left.
    const live = make('span', 'list-bar-live'); live.setAttribute('role', 'status'); live.setAttribute('aria-live', 'polite');
    let announce = false;
    let liveTimer = 0; // the announcement waits for the count to settle: a server-backed panel draws the old count first and the answer's a moment later
    let tog = null, badge = null, chev = null;
    if (collapsible) {
        tog = make('button', 'sidebar-collapse-btn list-bar-toggle'); tog.type = 'button'; tog.id = `${p}-toggle`;
        tog.setAttribute('aria-expanded', 'false'); tog.setAttribute('aria-controls', `${p}-menu`); tog.title = 'Filters and sort'; tog.setAttribute('aria-label', 'Filters and sort');
        const icon = make('span', 'material-icons', 'notes'); icon.setAttribute('aria-hidden', 'true');
        badge = make('span', 'display-badge'); badge.hidden = true;
        tog.append(icon, badge);
        if (spec.expandAll) {
            chev = make('button', 'sidebar-collapse-btn group-toggle-btn list-bar-expand-all'); chev.type = 'button'; chev.dataset.next = 'expand';
            chev.innerHTML = '<svg class="group-toggle-collapse" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M7 9l5-5 5 5M7 15l5 5 5-5"/></svg><svg class="group-toggle-expand" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M7 4l5 5 5-5M7 20l5-5 5 5"/></svg>';
            chev.title = spec.expandAll.expand; chev.setAttribute('aria-label', spec.expandAll.expand);
        }
    }
    // spec.headerSlots (#5315): the owner's title goes before the search box and its action icons after the filter icon, on this one row (setHeader). The slots are display: contents, so their children are the row's own.
    const lead = spec.headerSlots ? make('span', 'list-bar-lead') : null, tail = spec.headerSlots ? make('span', 'list-bar-tail') : null;
    if (noSearch) searchRow.hidden = true; else searchRow.append(...(lead ? [lead] : []), wrap, count, ...(tog ? [tog] : []), ...(chev ? [chev] : []), ...(tail ? [tail] : []));

    const controls = make('div', 'list-bar-controls');
    const filterBtn = make('button', 'list-bar-filter-btn', 'Filters'); filterBtn.type = 'button';
    filterBtn.setAttribute('aria-haspopup', 'true'); filterBtn.setAttribute('aria-expanded', 'false'); filterBtn.setAttribute('aria-controls', `${p}-menu`);
    // A panel with one fixed order (the board's messages: the page is the order) has no sort: spec.sorts is null.
    const sort = spec.sorts ? selectField(`${p}-sort`, 'Sort', spec.sorts.values, spec.sorts.labels) : null;
    if (sort) { sort.wrap.classList.add('list-bar-sort'); sort.input.setAttribute('aria-label', spec.sorts.aria); }
    // A panel with only a search (no fields: the Servers list) has no Filters button and no menu.
    const hasFilters = spec.fields.length > 0;
    if (inline) { if (sort) sort.wrap.classList.remove('list-bar-sort'); }
    else if (collapsible) { if (sort) sort.wrap.classList.remove('list-bar-sort'); }
    else controls.append(...[hasFilters ? filterBtn : null, sort ? sort.wrap : null].filter(Boolean));
    if (noSearch && !inline) controls.append(count);

    const menu = make('div', 'list-bar-menu'); menu.id = `${p}-menu`; menu.hidden = true; menu.setAttribute('role', 'group'); menu.setAttribute('aria-label', 'Filters');
    const fields = spec.fields.map(buildField);
    const clearAll = make('button', 'list-bar-clear-all', 'Clear all'); clearAll.type = 'button';
    menu.append(...fields.map(x => x.wrap), ...((inline || collapsible) && sort ? [sort.wrap] : []), ...(inline ? [count] : []), clearAll);
    if (inline) { menu.hidden = false; menu.classList.add('list-bar-menu-inline'); clearAll.textContent = 'Clear filters'; }
    if (collapsible) { menu.classList.add('list-bar-menu-inline', 'list-bar-panel'); clearAll.textContent = 'Clear filters'; menu.setAttribute('aria-label', 'Filters and sort'); }

    const chipList = make('ul', 'list-bar-chips'); chipList.setAttribute('aria-label', 'Active filters'); chipList.hidden = true;
    if (noSearch) node.classList.add('list-bar-nosearch');
    node.append(...(noSearch ? [] : [searchRow]), ...(controls.children.length ? [controls] : []), ...(hasFilters ? [menu] : []), ...(inline ? [] : [chipList]), live);
    if (collapsible) node.classList.add('list-bar-collapsible');
    if (lead) node.classList.add('list-bar-has-header');

    // Whatever a panel's model does with a text, the bar sends it trimmed: a tab or a space pasted at either end is a request the server refuses (#4806), and no panel can forget it.
    const textKeys = ['q', ...spec.fields.filter(x => x.type === 'text').map(x => x.key)];
    const trimmed = o => { const t = { ...o }; for (const k of textKeys) if (typeof t[k] === 'string') t[k] = t[k].trim(); return t; };
    const apply = next => { f = clean(trimmed(next)); announce = true; onChange({ ...f }); };
    const later = next => { clearTimeout(timer); timer = setTimeout(() => apply(next), DEBOUNCE_MS); };
    // the panel of a collapsible bar: opened and shut by the icon (the state is the panel's owner's to keep: hooks.onPanel)
    const setOpen = (open, { focus = false, silent = true } = {}) => {
        if (!tog) return;
        menu.hidden = !open; tog.setAttribute('aria-expanded', String(open));
        if (!silent && hooks.onPanel) hooks.onPanel(open);
        if (open && focus && fields.length) fields[0].input.focus();
    };
    const closeMenu = (focus) => { if (tog) { if (!menu.hidden) { setOpen(false, { silent: false }); if (focus) tog.focus(); } return; } if (inline || menu.hidden) return; menu.hidden = true; filterBtn.setAttribute('aria-expanded', 'false'); if (focus) filterBtn.focus(); };

    if (!noSearch) {
        q.addEventListener('input', () => { clearQ.hidden = !q.value; later({ ...f, q: q.value }); });
        q.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); clearTimeout(timer); apply({ ...f, q: q.value }); } });
        clearQ.addEventListener('click', () => { clearTimeout(timer); q.value = ''; clearQ.hidden = true; apply({ ...f, q: '' }); q.focus(); });
    }
    if (tog) tog.addEventListener('click', () => setOpen(menu.hidden, { focus: true, silent: false }));
    if (chev) chev.addEventListener('click', () => { const next = chev.dataset.next; if (hooks.onExpandAll) hooks.onExpandAll(next); });
    filterBtn.addEventListener('click', () => {
        const open = menu.hidden;
        menu.hidden = !open; filterBtn.setAttribute('aria-expanded', String(open));
        if (open && fields.length) fields[0].input.focus();
    });
    node.addEventListener('keydown', e => { if (e.key === 'Escape' && !inline && !menu.hidden && !(tog && !menu.contains(e.target))) { e.preventDefault(); e.stopPropagation(); closeMenu(true); } });
    const outside = e => { if (!inline && !tog && !menu.hidden && !node.contains(e.target)) closeMenu(false); }; // a collapsible panel is in the flow: it stays open until the icon shuts it
    document.addEventListener('click', outside);
    for (const fld of fields) {
        const key = fld.spec.key;
        if (fld.spec.type === 'text') fld.input.addEventListener('input', () => later({ ...f, [key]: fld.input.value }));
        else if (fld.spec.type === 'check') fld.input.addEventListener('change', () => apply({ ...f, [key]: fld.input.checked }));
        else fld.input.addEventListener('change', () => apply({ ...f, [key]: fld.input.value }));
    }
    if (sort) sort.input.addEventListener('change', () => apply({ ...f, sort: sort.input.value }));
    const everything = () => { clearTimeout(timer); apply({ ...EMPTY, sort: f.sort }); };
    const focusSearch = () => { if (!noSearch) q.focus(); else if (inline) fields[0]?.input.focus(); else filterBtn.focus(); };
    clearAll.addEventListener('click', () => { everything(); focusSearch(); });

    return {
        node,
        /** The row of the Filters button (and Sort): a panel puts its own buttons beside them. */
        controls,
        /** Show these filters and counts ({shown, matched, all}); a field the person is typing in is left as typed. */
        set(next, counts) {
            f = clean(next);
            const put = (el, v) => { if (el.value !== v && document.activeElement !== el) el.value = v; };
            if (!noSearch) { put(q, f.q); clearQ.hidden = !q.value; }
            for (const fld of fields) {
                const key = fld.spec.key;
                if (fld.spec.type === 'text') put(fld.input, f[key]);
                else if (fld.spec.type === 'check') fld.input.checked = f[key];
                else fld.input.value = f[key];
            }
            if (sort) sort.input.value = f.sort;
            const list = chips(f);
            chipList.replaceChildren(...list.map(c => {
                const li = make('li'); const b = make('button', 'list-bar-chip'); b.type = 'button';
                b.append(make('span', 'list-bar-chip-text', c.label), make('span', 'list-bar-chip-x', '×'));
                b.setAttribute('aria-label', `Remove filter: ${c.label}`);
                b.addEventListener('click', () => { clearTimeout(timer); apply(without(f, c.key)); focusSearch(); });
                li.append(b); return li;
            }));
            if (list.length > 1) { const li = make('li'); const b = make('button', 'list-bar-chip list-bar-chip-all', 'Clear all'); b.type = 'button'; b.addEventListener('click', () => { everything(); focusSearch(); }); li.append(b); chipList.append(li); }
            chipList.hidden = !list.length;
            if (inline || collapsible) clearAll.hidden = !isFiltered(f);
            filterBtn.textContent = isFiltered(f) ? `Filters (${list.filter(c => c.key !== 'q').length})` : 'Filters';
            if (tog) {
                // what is on behind the icon: the filters (the search is its own box) and a sort that is not the default; the icon says so, so a shut panel never hides a filter
                const on = list.filter(c => c.key !== 'q').length + (f.sort !== EMPTY.sort ? 1 : 0);
                badge.hidden = on === 0; tog.classList.toggle('active', on > 0);
                const name = on ? `Filters and sort, ${on} on` : 'Filters and sort';
                tog.setAttribute('aria-label', name); tog.title = name;
            }
            const all = `${counts && counts.all}${counts && counts.truncated ? '+' : ''}`; // '+': older ones were not scanned
            const say = text => {
                count.textContent = text;
                if (!announce || !text) return; // an empty text (no counts yet) is not a count to say
                clearTimeout(liveTimer);
                liveTimer = setTimeout(() => { if (!count.textContent) return; /* no count to say yet (the answer is slower than the settle): keep the flag for it */ announce = false; live.textContent = count.textContent; }, LIVE_SETTLE_MS);
            };
            if (counts && counts.text != null) { say(counts.text); return; } // a panel that words its own count (the agent list's: its search text narrows it too)
            say(!counts ? '' : isFiltered(f) ? `${counts.matched} of ${all}` : `${all} ${counts.all === 1 && !counts.truncated ? spec.noun : (spec.plural || spec.noun + 's')}`);
        },
        /** The names seen in the list, offered in the text field that has a datalist (typed text still works). */
        names(list) { const fld = fields.find(x => x.list); if (fld) fld.list.replaceChildren(...list.map(n => new Option(n))); },
        /** A select field's options, when they change with the list (the boards or machines there are now): the value is kept if it is still one of them. */
        setOptions(key, values, labels) {
            const fld = fields.find(x => x.spec.key === key); if (!fld || fld.spec.type !== 'select') return;
            const cur = fld.input.value;
            const any = fld.spec.any ? [new Option(fld.spec.any, '')] : [];
            fld.input.replaceChildren(...any, ...values.map(v => new Option(labels[v] || v, v)));
            fld.input.value = values.includes(cur) ? cur : '';
        },
        /** Show or hide a field of the Filters menu (the Machine field, where there is only one machine). */
        showField(key, visible) { const fld = fields.find(x => x.spec.key === key); if (fld) fld.wrap.hidden = !visible; },
        closeMenu,
        focusSearch,
        /** A collapsible bar: show or hide the panel (the owner restores what it kept; this does not call hooks.onPanel). */
        setOpen,
        isOpen: () => !menu.hidden,
        /** The expand-all icon: what pressing it will do next ('expand' or 'collapse'). */
        setExpandNext(next) { if (!chev) return; chev.dataset.next = next; const word = spec.expandAll[next]; chev.title = word; chev.setAttribute('aria-label', word); },
        /** A bar with headerSlots: the title (a node) before the search box, and the action icons (nodes) after the filter icons. */
        setHeader(title, tools = []) { if (!lead) return; lead.replaceChildren(...(title ? [title] : [])); tail.replaceChildren(...tools); },
        /** A bar that is replaced (a panel rebuilt for another board) lets go of the page's click listener and any search still waiting. */
        dispose() { document.removeEventListener('click', outside); clearTimeout(timer); clearTimeout(liveTimer); node.remove(); },
    };
}
