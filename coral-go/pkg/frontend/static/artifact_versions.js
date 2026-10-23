/* Versions of an artifact in the viewer (#6357, #6472 layout A): a bar under the title for an artifact that is one of several versions of a series: Previous and Next, "v3 of 3" opening the list of
 * every version (a listbox, newest first), and "Compare with v2". Compare draws the text's lines that were added and removed since the version before (Changes) or the two versions side by side. All
 * text is set with textContent. The server's list is GET /api/artifacts/{digest}/versions (an artifact in no series is a 404: no bar). A switch and a compare are said once in a polite line. */
import { serverUrl, artifactPath } from './server_base.js';

const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
const btn = (cls, text, label) => { const b = el('button', 'btn btn-small ' + cls, text); b.type = 'button'; if (label) b.setAttribute('aria-label', label); return b; };
const sr = (text) => el('span', 'sr-only', text);

const TEXTY = /^(text\/|application\/(json|xml|yaml|x-yaml|toml)|application\/.*\+(json|xml))/i;
const MAX_LINES = 4000, MAX_CELLS = 6e6, CONTEXT = 3;

/** A version's time as people say it ("today 08:15", "yesterday 16:40", "Oct 21 09:00"), and the full time for a title and a status line. */
export function whenOf(iso, now = new Date()) {
    const t = new Date(iso);
    if (isNaN(t)) return { label: '', full: '' };
    const hm = t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false });
    const day = (d) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
    const diff = Math.round((day(now) - day(t)) / 86400000);
    const label = diff === 0 ? `today ${hm}` : diff === 1 ? `yesterday ${hm}` : `${t.toLocaleDateString([], { month: 'short', day: 'numeric' })} ${hm}`;
    return { label, full: t.toLocaleString() };
}

const sizeOf = (n) => (n >= 1048576 ? `${(n / 1048576).toFixed(1)} MB` : n >= 1024 ? `${Math.round(n / 1024)} KB` : `${n} B`);

/** The lines of `b` that are added and the lines of `a` that are removed: [{ t: 'same'|'add'|'del', text }] in reading order, or null when the two are too long to compare line by line. */
export function diffLines(a, b) {
    const x = a.split('\n'), y = b.split('\n');
    let lo = 0;
    while (lo < x.length && lo < y.length && x[lo] === y[lo]) lo++;
    let hx = x.length, hy = y.length;
    while (hx > lo && hy > lo && x[hx - 1] === y[hy - 1]) { hx--; hy--; }
    const mx = x.slice(lo, hx), my = y.slice(lo, hy);
    if (x.length > MAX_LINES || y.length > MAX_LINES || mx.length * my.length > MAX_CELLS) return null;
    // the longest common subsequence of the middle, by the usual table
    const w = my.length + 1, tab = new Uint16Array((mx.length + 1) * w);
    for (let i = mx.length - 1; i >= 0; i--) for (let j = my.length - 1; j >= 0; j--) {
        tab[i * w + j] = mx[i] === my[j] ? tab[(i + 1) * w + j + 1] + 1 : Math.max(tab[(i + 1) * w + j], tab[i * w + j + 1]);
    }
    const out = [];
    for (let i = 0; i < lo; i++) out.push({ t: 'same', text: x[i] });
    let i = 0, j = 0;
    while (i < mx.length || j < my.length) {
        if (i < mx.length && j < my.length && mx[i] === my[j]) { out.push({ t: 'same', text: mx[i] }); i++; j++; }
        else if (i < mx.length && (j === my.length || tab[(i + 1) * w + j] >= tab[i * w + j + 1])) { out.push({ t: 'del', text: mx[i] }); i++; } // a removed line before the line that replaces it
        else { out.push({ t: 'add', text: my[j] }); j++; }
    }
    for (let k = hx; k < x.length; k++) out.push({ t: 'same', text: x[k] });
    return out;
}

/** Wire the version bar into the viewer `dialog`. hooks: show(row) opens that version, reload() draws the version on show again, body() is the element the document is drawn in. Returns { open, close }. */
export function attachVersions(dialog, hooks) {
    const bar = el('div', 'av-bar'); bar.hidden = true;
    bar.setAttribute('role', 'group'); bar.setAttribute('aria-label', 'Versions');
    const live = el('div', 'sr-only av-live'); live.setAttribute('role', 'status'); live.setAttribute('aria-live', 'polite'); live.setAttribute('aria-atomic', 'true');
    let flip = false;
    const say = (msg) => { flip = !flip; live.textContent = msg + (flip ? '' : ' '); };
    dialog.querySelector('.artifact-viewer-header').after(bar);
    dialog.append(live);
    const st = { hex: null, server: undefined, rows: null, listOpen: false, comparing: false, view: 'changes', gen: 0, abort: null };
    const cur = () => st.rows ? st.rows.findIndex(r => r.digest === st.hex) : -1;
    const nameOf = (r) => `v${r.version}`;

    async function load(hex, server) {
        try {
            const r = await fetch(serverUrl(`/api/artifacts/${hex}/versions`, server));
            if (!r.ok) return null;
            const d = await r.json();
            return Array.isArray(d) && d.length > 1 ? d.slice().sort((p, q) => p.version - q.version) : null; // one version is no series to move in
        } catch { return null; }
    }

    // Open or close the list in place: the bar's buttons are not redrawn (a Tab walk or a screen reader holds them).
    function setList(open) {
        st.listOpen = open;
        bar.querySelector('.av-current')?.setAttribute('aria-expanded', String(open));
        const list = bar.querySelector('.av-list'); if (list) list.hidden = !open;
    }
    function closeList(focusButton) {
        if (!st.listOpen) return;
        setList(false);
        if (focusButton) bar.querySelector('.av-current')?.focus();
    }
    bar.addEventListener('focusout', (e) => { if (st.listOpen && e.relatedTarget && !bar.contains(e.relatedTarget)) closeList(false); }); // Tab out of the bar closes the list it left
    document.addEventListener('click', (e) => { if (st.listOpen && !e.composedPath().includes(bar)) closeList(false); }); // the path as it was when the click began: a click on a button the bar then redrew is still the bar's

    function stepTo(delta) {
        const i = cur() + delta;
        if (!st.rows || i < 0 || i >= st.rows.length) return;
        choose(st.rows[i]);
    }
    function choose(row) {
        st.listOpen = false; st.comparing = false;
        hooks.show(row);
    }

    function draw() {
        const keepFocus = bar.contains(document.activeElement) ? document.activeElement.className.split(' ').find(c => c.startsWith('av-')) : '';
        bar.replaceChildren();
        const i = cur();
        bar.hidden = !st.rows || i < 0;
        dialog.classList.toggle('av-has-versions', !bar.hidden);
        if (bar.hidden) return;
        const rows = st.rows, row = rows[i], total = rows.length, latest = i === total - 1;
        const prev = btn('av-prev av-icon', '◀', 'Previous version'); prev.setAttribute('aria-disabled', String(i === 0));
        prev.addEventListener('click', () => stepTo(-1));
        const next = btn('av-next av-icon', '▶', 'Next version'); next.setAttribute('aria-disabled', String(latest));
        next.addEventListener('click', () => stepTo(1));
        const current = btn('av-current', `v${row.version} of ${total} ▾`, `Version: v${row.version} of ${total}${latest ? ', newest' : ''}`);
        current.setAttribute('aria-haspopup', 'listbox'); current.setAttribute('aria-expanded', String(st.listOpen)); current.setAttribute('aria-controls', 'av-list');
        current.addEventListener('click', () => { setList(!st.listOpen); if (st.listOpen) focusOption(); });
        current.addEventListener('keydown', (e) => { if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); if (!st.listOpen) setList(true); focusOption(); } else if (e.key === 'Escape' && st.listOpen) { e.preventDefault(); e.stopPropagation(); closeList(true); } });
        bar.append(prev, current, next, el('span', 'av-spacer'));
        if (i > 0) {
            const cmp = btn('av-compare', st.comparing ? `Back to v${row.version}` : `Compare with v${rows[i - 1].version}`);
            cmp.setAttribute('aria-pressed', String(st.comparing));
            cmp.addEventListener('click', () => (st.comparing ? leaveCompare() : startCompare()));
            bar.append(cmp);
        }
        if (!latest) {
            const note = el('p', 'av-notice'); note.append(`v${rows[total - 1].version} is newer. `);
            const open = el('button', 'av-link', 'Open the latest'); open.type = 'button'; open.addEventListener('click', () => choose(rows[total - 1]));
            note.append(open); bar.append(note);
        }
        const list = el('ul', 'av-list'); list.id = 'av-list'; list.setAttribute('role', 'listbox'); list.setAttribute('aria-label', 'All versions'); list.hidden = !st.listOpen;
        for (const r of rows.slice().reverse()) {
            const w = whenOf(r.at);
            // the shown version is the list's one Tab stop; the arrow keys move between the rest (a roving tabindex)
            const li = el('li', 'av-option'); li.setAttribute('role', 'option'); li.id = `av-opt-${r.version}`; li.tabIndex = r.digest === st.hex ? 0 : -1; li.dataset.version = String(r.version);
            li.setAttribute('aria-selected', String(r.digest === st.hex)); li.title = w.full;
            li.append(el('span', 'av-vname', `v${r.version}${r.is_latest ? ' (latest)' : ''}`), el('span', 'av-who', [w.label, r.role || r.uploader].filter(Boolean).join(' · ')), el('span', 'av-size', sizeOf(r.size || 0)));
            li.addEventListener('click', () => choose(r));
            li.addEventListener('focus', () => { for (const o of options()) o.tabIndex = o === li ? 0 : -1; });
            list.append(li);
        }
        list.addEventListener('keydown', onListKey);
        bar.append(list);
        if (keepFocus) bar.querySelector('.' + keepFocus)?.focus();
    }
    const options = () => [...bar.querySelectorAll('.av-option')];
    function focusOption() {
        const o = options(), sel = o.find(x => x.getAttribute('aria-selected') === 'true') || o[0];
        sel?.focus();
    }
    function onListKey(e) {
        const o = options(), at = o.indexOf(document.activeElement);
        if (e.key === 'ArrowDown') { e.preventDefault(); o[Math.min(o.length - 1, at + 1)]?.focus(); }
        else if (e.key === 'ArrowUp') { e.preventDefault(); o[Math.max(0, at - 1)]?.focus(); }
        else if (e.key === 'Home') { e.preventDefault(); o[0]?.focus(); }
        else if (e.key === 'End') { e.preventDefault(); o[o.length - 1]?.focus(); }
        else if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); const r = st.rows.find(x => String(x.version) === document.activeElement?.dataset.version); if (r) choose(r); }
        else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); closeList(true); }
    }

    // ── compare
    async function text(digest, signal) {
        const r = await fetch(serverUrl(artifactPath(digest), st.server), { signal });
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.text();
    }
    async function startCompare() {
        const i = cur(), row = st.rows[i], before = st.rows[i - 1];
        if (!TEXTY.test(row.media_type || '') && !/\.(md|markdown|txt|json|ya?ml|toml|csv|log)$/i.test(row.name || '')) { say('Compare is for text files.'); return; }
        dialog.classList.toggle('av-keep-wide', dialog.classList.contains('ac-on')); // the dialog keeps the width the version had with its comments: no jump on Compare
        st.comparing = true; st.sheetWas = hooks.sheetOpen(); if (st.sheetWas) hooks.setSheet(false); draw(); // a phone's open comments sheet would keep the document out of the keyboard's reach
        st.abort?.abort(); st.abort = new AbortController();
        const gen = ++st.gen, signal = st.abort.signal;
        try {
            const [a, b] = await Promise.all([text(before.digest, signal), text(row.digest, signal)]);
            if (gen !== st.gen || !st.comparing) return;
            const lines = diffLines(a, b);
            const body = hooks.body();
            dialog.classList.add('av-comparing');
            if (!lines) { body.replaceChildren(heading(row, before), el('p', 'av-note', 'These versions are too long to compare line by line.')); say('Too long to compare line by line.'); focusHeading(); return; }
            drawCompare(body, row, before, lines, true);
        } catch (e) {
            if (gen !== st.gen) return;
            st.comparing = false; draw();
            say(`Could not load the versions to compare: ${e.message || 'refused'}.`);
        }
    }
    const heading = (row, before) => { const h = el('h3', 'av-heading', `Changes since v${before.version}`); h.tabIndex = -1; return h; };
    const focusHeading = () => hooks.body().querySelector('.av-heading')?.focus();
    function drawCompare(body, row, before, lines, first = false) {
        const added = lines.filter(l => l.t === 'add').length, removed = lines.filter(l => l.t === 'del').length;
        const count = `${added} added, ${removed} removed`;
        const wrap = el('div', 'av-compare');
        const h = heading(row, before);
        const top = el('div', 'av-modebar');
        const status = el('p', 'av-count', count); // plain text: the one announcement is the polite line (a second live region would say it twice)
        const seg = el('div', 'av-seg'); seg.setAttribute('role', 'group'); seg.setAttribute('aria-label', 'Compare view');
        const bChanges = btn('av-view-changes', 'Changes'), bSplit = btn('av-view-split', 'Side by side');
        for (const [b, v] of [[bChanges, 'changes'], [bSplit, 'split']]) { b.setAttribute('aria-pressed', String(st.view === v)); b.addEventListener('click', () => { st.view = v; drawCompare(body, row, before, lines, false); body.querySelector(v === 'changes' ? '.av-view-changes' : '.av-view-split')?.focus(); }); }
        seg.append(bChanges, bSplit); top.append(status, seg);
        wrap.append(h, top);
        if (!added && !removed) wrap.append(el('p', 'av-note', `v${row.version} has the same text as v${before.version}.`));
        else wrap.append(st.view === 'changes' ? changesView(lines) : splitView(lines, row, before));
        body.replaceChildren(wrap);
        if (first) { say(`Changes since v${before.version}: ${count}`); h.focus(); } // said and shown once, when the compare opens, not on each switch of the view
    }
    function lineEl(l) {
        if (l.t === 'same') return el('div', 'av-line av-same', l.text || ' ');
        const tag = l.t === 'add' ? 'ins' : 'del';
        const d = el('div', l.t === 'add' ? 'av-line av-add' : 'av-line av-del'); const m = el(tag, '', l.text || ' '); d.append(sr(l.t === 'add' ? 'Added: ' : 'Removed: '), m);
        return d;
    }
    function changesView(lines) {
        const box = el('div', 'av-lines');
        const keep = new Array(lines.length).fill(false);
        lines.forEach((l, i) => { if (l.t !== 'same') for (let k = Math.max(0, i - CONTEXT); k <= Math.min(lines.length - 1, i + CONTEXT); k++) keep[k] = true; });
        // a run of one or two unchanged lines is shown, not folded into a note that costs as much as the lines
        for (let i = 0; i < lines.length;) {
            if (keep[i]) { i++; continue; }
            let j = i; while (j < lines.length && !keep[j]) j++;
            if (j - i < 3) for (let k = i; k < j; k++) keep[k] = true;
            i = j;
        }
        let skipped = 0;
        lines.forEach((l, i) => {
            if (!keep[i]) { skipped++; return; }
            if (skipped) { box.append(el('div', 'av-gap', `${skipped} unchanged lines not shown`)); skipped = 0; }
            box.append(lineEl(l));
        });
        if (skipped) box.append(el('div', 'av-gap', `${skipped} unchanged lines not shown`));
        return box;
    }
    function splitView(lines, row, before) {
        const grid = el('div', 'av-split');
        const left = el('div', 'av-col'), right = el('div', 'av-col');
        left.append(el('h4', 'av-colhead', `v${before.version} · ${whenOf(before.at).label}`));
        right.append(el('h4', 'av-colhead', `v${row.version} · ${whenOf(row.at).label}`));
        for (const l of lines) { if (l.t !== 'add') left.append(lineEl(l)); if (l.t !== 'del') right.append(lineEl(l)); }
        grid.append(left, right);
        return grid;
    }
    function leaveCompare() {
        st.comparing = false; st.gen++; st.abort?.abort();
        dialog.classList.remove('av-comparing', 'av-keep-wide');
        draw();
        hooks.reload(); // the version is drawn again, with its comments
        if (st.sheetWas) hooks.setSheet(true);
        st.sheetWas = false;
        bar.querySelector('.av-compare')?.focus();
    }

    return {
        /** A version of an artifact is on show: draw the bar when it is one of a series. `switched` says it was chosen from the bar (said once, and the keyboard stays on the bar). */
        async open(hex, server, { switched = false } = {}) {
            const same = st.rows && st.hex !== hex && st.server === server && st.rows.some(r => r.digest === hex);
            st.hex = hex; st.server = server; st.listOpen = false;
            if (!switched) { st.comparing = false; dialog.classList.remove('av-comparing', 'av-keep-wide'); }
            if (!same && !(st.rows && st.rows.some(r => r.digest === hex))) { st.rows = null; draw(); const gen = ++st.gen; const rows = await load(hex, server); if (gen !== st.gen || st.hex !== hex) return; st.rows = rows; }
            draw();
            if (switched && !bar.hidden) {
                const row = st.rows[cur()], w = whenOf(row.at);
                say(`Showing v${row.version} of ${st.rows.length}, ${w.label} (${w.full}), ${row.role || row.uploader || 'unknown'}`);
                bar.querySelector('.av-current')?.focus();
            }
        },
        close() { st.gen++; st.abort?.abort(); st.rows = null; st.hex = null; st.comparing = false; st.listOpen = false; dialog.classList.remove('av-comparing', 'av-has-versions', 'av-keep-wide'); bar.hidden = true; bar.replaceChildren(); },
        comparing: () => st.comparing,
    };
}
