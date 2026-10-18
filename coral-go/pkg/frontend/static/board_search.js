/* The message board's search (#4771): a bar under the header (the shared list bar: search, Filters with From, Mentions me and two dates, First and Latest beside Filters), a results line with
 * the number of the match on screen and previous, next and Clear, and the matches marked in the messages. The server finds the matches (GET /api/board/{project}/messages/search, #4745); the
 * board takes the page to a match with its own jump (message_board.js: the pages between are loaded, the board's windowed rendering and its prepend stay as they were). Matches are numbered newest
 * first: 1 is the latest message that matches, the up arrow goes to an older one, the down arrow to a newer one. */
import { buildListBar } from './list_bar.js';
import { EMPTY, clean, chips, isFiltered, without, toParams, resultsText } from './board_filters.js';
import { boardFetch } from './server_base.js';

const SPEC = {
    idp: 'bm',
    noun: 'message',
    label: 'Search and filter the board',
    searchLabel: 'Search this board',
    placeholder: 'Search this board',
    sorts: null,
    fields: [
        { type: 'text', key: 'from', id: 'bm-from', className: 'list-bar-by', label: 'From', max: 80, placeholder: 'Agent or person', datalist: 'bm-from-list' },
        { type: 'check', key: 'mentions', id: 'bm-mentions', label: 'Mentions me', wrapClass: 'list-bar-field list-bar-kept' },
        { type: 'date', key: 'since', id: 'bm-since', label: 'Since' },
        { type: 'date', key: 'until', id: 'bm-until', label: 'Until' },
    ],
    model: { EMPTY, clean, chips, isFiltered, without },
};

const PAGE = 50;
let _bar = null, _host = null, _results = null, _status = null, _hooks = null;
let _project = null, _filters = { ...EMPTY };
let _list = { ids: [], total: 0, cursor: '', busy: false, error: '', truncated: false }; // the matches, newest first
let _index = -1; // the match on screen
let _seq = 0;

const escapeRegex = s => s.replace(/[.*+?^${}()[\]\\|]/g, '\\$&');

function make(tag, className, text) {
    const n = document.createElement(tag);
    if (className) n.className = className;
    if (text != null) n.textContent = text;
    return n;
}

/** The matches' markers in the loaded messages: the search text wrapped in <mark>, the match on screen stronger. Safe to call after any draw of the rows (it undoes its own marks first). */
export function highlightBoard(container) {
    if (!container) return;
    for (const m of container.querySelectorAll('mark.mb-hit')) m.replaceWith(document.createTextNode(m.textContent));
    for (const row of container.querySelectorAll('.mb-message-match, .mb-message-current')) row.classList.remove('mb-message-match', 'mb-message-current');
    const active = isFiltered(_filters) && _list.ids.length;
    if (!active) return;
    const ids = new Set(_list.ids.map(String));
    const current = _index >= 0 ? String(_list.ids[_index]) : null;
    const re = _filters.q ? new RegExp(escapeRegex(_filters.q), 'gi') : null;
    const has = _filters.q ? new RegExp(escapeRegex(_filters.q), 'i') : null; // to test a text node: a global expression keeps its place from one test() to the next, and skips the next node's first match
    for (const row of container.querySelectorAll(':scope > .mb-message[data-message-id]')) {
        const id = row.dataset.messageId;
        if (!ids.has(id)) continue;
        row.classList.add(id === current ? 'mb-message-current' : 'mb-message-match');
        if (!re) continue;
        const body = row.querySelector('.mb-message-body');
        if (!body) continue;
        const walker = document.createTreeWalker(body, NodeFilter.SHOW_TEXT);
        const nodes = [];
        for (let n = walker.nextNode(); n; n = walker.nextNode()) if (has.test(n.nodeValue)) nodes.push(n);
        for (const node of nodes) {
            const frag = document.createDocumentFragment();
            let last = 0;
            node.nodeValue.replace(re, (hit, at) => { frag.append(node.nodeValue.slice(last, at)); frag.append(make('mark', 'mb-hit', hit)); last = at + hit.length; return hit; });
            frag.append(node.nodeValue.slice(last));
            node.replaceWith(frag);
        }
    }
}

function status() {
    if (!_status) return;
    if (!isFiltered(_filters)) { _status.hidden = true; return; }
    _status.hidden = false;
    const busy = _list.busy && !_list.ids.length;
    _results.textContent = _list.error || (busy ? 'Searching…' : resultsText(Math.max(_index, 0), _list.total, _list.truncated));
    const none = !_list.ids.length;
    for (const b of _status.querySelectorAll('button[data-step]')) b.disabled = none || (b.dataset.step === 'older' ? _index >= _list.total - 1 : _index <= 0);
}

async function query(extra) {
    const params = toParams(_filters, { me: _hooks.me, limit: PAGE, ...extra });
    const qs = new URLSearchParams(params).toString();
    const r = await boardFetch(_project, `/messages/search?${qs}`);
    if (!r.ok) { const err = new Error(`search: HTTP ${r.status}`); err.status = r.status; throw err; }
    return r.json();
}

/** The filters changed: ask for the matches again and go to the newest. */
async function run(next) {
    _filters = clean(next);
    const seq = ++_seq;
    _index = -1;
    _list = { ids: [], total: 0, cursor: '', busy: isFiltered(_filters), error: '' };
    _bar.set(_filters, null);
    _hooks.redraw();
    status();
    if (!isFiltered(_filters)) return;
    try {
        const d = await query({});
        if (seq !== _seq) return;
        _list = { ids: (d.messages || []).map(m => m.id), total: d.total ?? (d.messages || []).length, cursor: d.next_cursor || '', busy: false, error: '', truncated: !!d.truncated };
        _index = _list.ids.length ? 0 : -1;
    } catch (e) {
        if (seq !== _seq) return;
        _list = { ids: [], total: 0, cursor: '', busy: false, error: e && e.status === 400 ? 'This search can not be run. Change it and try again.' : 'Could not search the board. Try again.' };
    }
    status();
    if (_index >= 0) _hooks.jumpTo(_list.ids[_index]);
    _hooks.redraw();
}

/** Step to an older (+1) or newer (-1) match, loading the next page of matches when the ones in hand run out. */
async function step(delta) {
    const to = _index + delta;
    if (to < 0 || to >= _list.total || _list.busy) return;
    if (to >= _list.ids.length) {
        if (!_list.cursor) return;
        const seq = _seq;
        _list.busy = true;
        try {
            const d = await query({ cursor: _list.cursor });
            if (seq !== _seq) return;
            const more = (d.messages || []).map(m => m.id).filter(id => !_list.ids.includes(id));
            _list = { ..._list, ids: [..._list.ids, ...more], cursor: d.next_cursor || '', busy: false };
        } catch { if (seq === _seq) { _list.busy = false; _list.error = 'Could not load more matches. Try again.'; status(); } return; }
        if (to >= _list.ids.length) { status(); return; }
    }
    _index = to;
    status();
    _hooks.jumpTo(_list.ids[_index]);
    _hooks.redraw();
}

/** The tools row in its place for this board (a fresh search); hooks: {me, redraw(), jumpTo(id), first(), latest()}. */
export function mountBoardSearch(host, project, hooks) {
    _hooks = hooks;
    if (_project !== project) {
        _project = project;
        _seq++;
        _filters = { ...EMPTY }; _list = { ids: [], total: 0, cursor: '', busy: false, error: '' }; _index = -1;
        if (_bar) { _bar.dispose(); _bar = null; }
        _status = null;
    }
    _host = host;
    if (!_bar) {
        _bar = buildListBar(SPEC, run);
        const first = make('button', 'list-bar-filter-btn mb-tool-btn', 'First'); first.type = 'button'; first.title = 'The first message of the board';
        const latest = make('button', 'list-bar-filter-btn mb-tool-btn', 'Latest'); latest.type = 'button'; latest.title = 'The latest message';
        first.addEventListener('click', () => _hooks.first());
        latest.addEventListener('click', () => _hooks.latest());
        _bar.controls.append(first, latest);
        _status = make('div', 'mb-search-status'); _status.hidden = true;
        _results = make('span', 'mb-search-results'); _results.setAttribute('role', 'status'); _results.setAttribute('aria-live', 'polite');
        const icon = (name, label, stepName) => { const b = make('button', 'list-bar-filter-btn mb-search-step'); b.type = 'button'; b.dataset.step = stepName; b.setAttribute('aria-label', label); b.title = label;
            const i = make('span', 'material-icons', name); i.setAttribute('aria-hidden', 'true'); b.append(i); return b; };
        const older = icon('keyboard_arrow_up', 'Previous match (older)', 'older'), newer = icon('keyboard_arrow_down', 'Next match (newer)', 'newer');
        older.addEventListener('click', () => step(+1)); newer.addEventListener('click', () => step(-1));
        const clear = make('button', 'list-bar-clear-all mb-search-clear', 'Clear'); clear.type = 'button';
        clear.addEventListener('click', () => { _bar.focusSearch(); run({ ...EMPTY }); });
        _status.append(_results, older, newer, clear);
        _bar.node.append(_status);
        _bar.set(_filters, null);
    }
    if (_bar.node.parentElement !== host) host.append(_bar.node);
}

/** The board's members, offered in the From field (typed text still works). */
export function setBoardMembers(names) { if (_bar) _bar.names(names); }

/** Leave the board: the search goes with it. */
export function resetBoardSearch() {
    _seq++; _project = null; _filters = { ...EMPTY }; _list = { ids: [], total: 0, cursor: '', busy: false, error: '' }; _index = -1;
    if (_bar) { _bar.dispose(); _bar = null; }
    _status = null;
}

export const boardSearchState = () => ({ filters: _filters, ids: _list.ids, total: _list.total, index: _index });
