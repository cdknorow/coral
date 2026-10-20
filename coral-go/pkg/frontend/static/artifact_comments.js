/* Comments in the artifact viewer (#5182, phase 1): a side panel (a bottom sheet on a phone) of the artifact's threads, highlights of what each is about, a Comment button by a text selection,
 * a composer, and the actions the server says this viewer may use. Everything shown is text (textContent, never HTML). The server is the gate: a control the viewer cannot use is hidden, a write
 * it refuses shows the server's one-line reason. Where a comment sits is artifact_anchor.js; the routes are artifact_comments_api.js. */
import { importOnce } from './import_once.js';
const { buildAnchor, locateQuote, indexText } = await importOnce('./artifact_anchor.js');
const api = await importOnce('./artifact_comments_api.js');
import { showToast } from './utils.js';
import { scrollBehavior } from './motion.js';
import { pushOverlay, popOverlay } from './overlay_history.js';

const BODY_MAX = 8192; // the server's cap on a comment (bytes; most text is one byte a character)
const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
const btn = (cls, text, label) => { const b = el('button', 'btn ' + cls, text); b.type = 'button'; if (label) b.setAttribute('aria-label', label); return b; };
const KIND = { owner: 'owner', hub: 'Coral Hub', agent: 'agent' };

function ago(iso) {
    const t = Date.parse(iso);
    if (!t) return '';
    const s = Math.max(0, (Date.now() - t) / 1000);
    if (s < 60) return 'just now';
    if (s < 3600) return `${Math.floor(s / 60)} min ago`;
    if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
    if (s < 172800) return 'yesterday';
    return `${Math.floor(s / 86400)} days ago`;
}

/** Wire comments into the viewer `dialog`: `body` is where the document is drawn, `host` the element the panel goes into. Returns { open, docRendered, close }. */
export function attachComments(dialog, body, host) {
    const st = { hex: null, server: undefined, board: undefined, source: '', text: false, data: null, filter: 'open', active: null, composing: null, busy: false, error: '', ranges: new Map(), orphan: new Set(), gone: false };
    const panel = el('aside', 'ac-panel'); panel.setAttribute('aria-label', 'Comments'); panel.hidden = true;
    const toggle = btn('ac-toggle', 'Comments', 'Show the comments'); toggle.hidden = true; toggle.setAttribute('aria-expanded', 'false');
    const selBtn = btn('ac-selbtn', 'Comment', 'Comment on the selected text'); selBtn.hidden = true;
    host.append(panel); dialog.querySelector('.artifact-viewer-header')?.insertBefore(toggle, dialog.querySelector('.artifact-viewer-download'));
    dialog.append(selBtn);

    // ── the data
    const threads = () => {
        const all = (st.data?.comments || []);
        const replies = new Map();
        for (const c of all) if (c.parentId != null && !c.deleted) { if (!replies.has(c.parentId)) replies.set(c.parentId, []); replies.get(c.parentId).push(c); }
        return all.filter(c => c.parentId == null && (!c.deleted || replies.has(c.id))).map(c => ({ c, replies: replies.get(c.id) || [] }));
    };
    async function refresh() {
        if (!st.hex || !st.text) return;
        const hex = st.hex;
        try {
            const data = await api.listComments(hex, st.server, st.board);
            if (hex !== st.hex) return;
            st.data = data; st.gone = false;
        } catch (e) {
            if (hex !== st.hex) return;
            st.gone = true; st.data = null; // no comments routes here (an older Coral, a hub that does not carry them): the viewer is as it was
        }
        render(); paint();
    }

    // ── highlights: the quote found again in what is drawn
    const textNodes = () => { const out = []; const w = document.createTreeWalker(body, NodeFilter.SHOW_TEXT); for (let n = w.nextNode(); n; n = w.nextNode()) out.push(n); return out; };
    function paint() {
        st.ranges.clear(); st.orphan.clear();
        const hl = window.CSS && CSS.highlights;
        if (hl) { hl.delete('ac-comment'); hl.delete('ac-active'); }
        if (!st.data) return;
        const ix = indexText(textNodes());
        const open = [];
        for (const { c } of threads()) {
            if (c.deleted || !c.anchor || c.anchor.whole) continue;
            const hit = locateQuote(ix.text, c.anchor);
            if (!hit) { st.orphan.add(c.id); continue; }
            const r = document.createRange();
            r.setStart(ix.at[hit.start].node, ix.at[hit.start].offset);
            const last = ix.at[hit.end - 1];
            r.setEnd(last.node, last.offset + 1);
            st.ranges.set(c.id, r);
            if (!c.resolved) open.push(r);
        }
        if (hl && typeof Highlight === 'function') {
            if (open.length) hl.set('ac-comment', new Highlight(...open));
            const a = st.ranges.get(st.active);
            if (a) hl.set('ac-active', new Highlight(a));
        }
        render();
    }

    // ── the panel
    function render() {
        panel.replaceChildren();
        const show = st.text && !st.gone && !!st.data;
        panel.hidden = !show; toggle.hidden = !show;
        dialog.classList.toggle('ac-on', show);
        paintToggle();
        if (!show) return;
        const list = threads();
        const head = el('div', 'ac-head');
        const h = el('h3', 'ac-title', 'Comments'); h.append(el('span', 'ac-count', String(list.filter(t => !t.c.deleted).length)));
        const filters = el('div', 'ac-filters'); filters.setAttribute('role', 'group'); filters.setAttribute('aria-label', 'Show comments');
        for (const [k, label] of [['open', 'Open'], ['resolved', 'Resolved'], ['all', 'All']]) {
            const b = btn('ac-filter', label); b.setAttribute('aria-pressed', String(st.filter === k));
            b.addEventListener('click', () => { st.filter = k; render(); });
            filters.append(b);
        }
        head.append(h, filters);
        const close = btn('ac-sheet-close', '✕', 'Hide the comments'); close.addEventListener('click', () => setSheet(false)); head.append(close);
        panel.append(head);
        const d = st.data;
        if (d.locked) panel.append(el('p', 'ac-note ac-locked', 'Comments are locked: read only.'));
        else if (!d.canComment) panel.append(el('p', 'ac-note ac-readonly', d.reason || 'You can read the comments here but not write them.'));
        if (d.canLock) {
            const lock = btn('ac-lock', d.locked ? 'Unlock comments' : 'Lock comments');
            lock.addEventListener('click', async () => { try { await api.setLocked(st.hex, st.server, !d.locked); await refresh(); } catch (e) { showToast(e.message, true); } });
            panel.append(lock);
        }
        const wrap = el('ul', 'ac-list');
        const shown = list.filter(t => st.filter === 'all' || (st.filter === 'resolved' ? t.c.resolved : !t.c.resolved));
        for (const t of shown) wrap.append(card(t));
        if (!shown.length) wrap.append(el('p', 'ac-empty', st.filter === 'open' ? 'No open comments. Select some text to add one.' : 'Nothing here.'));
        panel.append(wrap);
        if (st.composing && st.composing.anchor) panel.append(composer());
        else if (d.canComment && !d.locked) {
            const whole = btn('ac-whole', 'Comment on the whole document'); whole.addEventListener('click', () => { st.composing = { anchor: { whole: true } }; st.error = ''; render(); panel.querySelector('.ac-input')?.focus(); });
            panel.append(whole);
        }
        const act = panel.querySelector('.ac-card.ac-active'); if (act && st.scrollCard) { st.scrollCard = false; act.scrollIntoView({ block: 'nearest', behavior: scrollBehavior() }); }
    }

    function meta(c, extra) {
        const m = el('div', 'ac-meta');
        m.append(el('span', 'ac-author', c.authorName || c.authorId || 'unknown'));
        if (KIND[c.authorKind]) m.append(el('span', 'ac-kind', KIND[c.authorKind]));
        const time = el('span', 'ac-time', ago(c.createdAt)); time.title = c.createdAt; m.append(time);
        if (extra) for (const x of extra) m.append(x);
        if (c.editedAt) m.append(el('span', 'ac-pill', 'edited'));
        return m;
    }
    function card({ c, replies }) {
        const li = el('li', 'ac-card' + (st.active === c.id ? ' ac-active' : '') + (c.resolved ? ' ac-resolved' : '')); li.dataset.id = String(c.id);
        const a = c.anchor || {};
        const q = el('blockquote', 'ac-quote' + (st.orphan.has(c.id) ? ' ac-orphan' : ''), a.whole ? 'The whole document' : a.quote || '');
        li.append(q);
        const extra = [];
        if (a.start_line) extra.push(el('span', 'ac-lines', a.end_line && a.end_line !== a.start_line ? `lines ${a.start_line}-${a.end_line}` : `line ${a.start_line}`));
        if (c.resolved) extra.push(el('span', 'ac-pill ac-pill-resolved', 'resolved'));
        if (st.orphan.has(c.id)) extra.push(el('span', 'ac-pill ac-pill-orphan', 'text changed'));
        li.append(meta(c, extra));
        li.append(editing(c) || el('p', 'ac-body' + (c.deleted ? ' ac-deleted' : ''), c.deleted ? 'Comment deleted' : c.body));
        for (const r of replies) {
            const rep = el('div', 'ac-reply'); rep.dataset.id = String(r.id);
            rep.append(meta(r)); rep.append(editing(r) || el('p', 'ac-body', r.body)); rep.append(actions(r, false));
            li.append(rep);
        }
        li.append(actions(c, true));
        if (st.composing && st.composing.parentId === c.id) li.append(composer());
        li.addEventListener('click', (e) => { if (e.target.closest('button, textarea')) return; activate(c.id, true); });
        return li;
    }
    function actions(c, top) {
        const row = el('div', 'ac-actions');
        if (c.deleted) return row;
        if (top && c.can.reply) row.append(act('Reply', () => { st.composing = { parentId: c.id }; st.error = ''; render(); panel.querySelector('.ac-input')?.focus(); }));
        if (top && c.can.resolve) row.append(act(c.resolved ? 'Reopen' : 'Resolve', () => run(() => (c.resolved ? api.reopenComment : api.resolveComment)(st.hex, st.server, c.id))));
        if (c.can.edit) row.append(act('Edit', () => { st.composing = { editId: c.id }; st.error = ''; render(); panel.querySelector('.ac-input')?.focus(); }));
        if (c.can.delete) row.append(act('Delete', () => confirmThen('Delete this comment?', () => run(() => api.deleteComment(st.hex, st.server, c.id)))));
        return row;
    }
    const act = (text, fn) => { const b = btn('ac-act', text); b.addEventListener('click', fn); return b; };
    function confirmThen(msg, fn) { if (window.showConfirmModal) window.showConfirmModal('Delete comment', msg, fn); else fn(); }
    async function run(fn) {
        if (st.busy) return;
        st.busy = true;
        try { await fn(); st.error = ''; st.composing = null; await refresh(); }
        catch (e) { st.error = e.message || 'The change was refused.'; showToast(st.error, true); render(); }
        finally { st.busy = false; }
    }

    // the editor of an existing comment, in place of its text
    function editing(c) {
        if (!st.composing || st.composing.editId !== c.id) return null;
        return composer(c);
    }
    function composer(editOf) {
        const cm = st.composing;
        const box = el('form', 'ac-composer'); box.noValidate = true;
        if (cm.anchor) { const a = cm.anchor; const q = el('blockquote', 'ac-quote', a.whole ? 'The whole document' : a.quote); box.append(q); }
        const ta = el('textarea', 'ac-input'); ta.rows = 3; ta.maxLength = BODY_MAX; ta.value = editOf ? editOf.body : (cm.draft || '');
        ta.setAttribute('aria-label', editOf ? 'Edit your comment' : cm.parentId != null ? 'Your reply' : 'Your comment'); ta.placeholder = cm.parentId != null ? 'Write a reply…' : 'Add a comment…';
        const count = el('span', 'ac-counter', `${ta.value.length} / ${BODY_MAX}`);
        ta.addEventListener('input', () => { count.textContent = `${ta.value.length} / ${BODY_MAX}`; cm.draft = ta.value; });
        const cancel = btn('ac-cancel', 'Cancel'); cancel.addEventListener('click', () => { st.composing = null; st.error = ''; render(); });
        const send = btn('btn-primary ac-send', editOf ? 'Save' : cm.parentId != null ? 'Reply' : 'Comment'); send.type = 'submit';
        const err = el('p', 'ac-error', st.error); err.setAttribute('role', 'alert'); err.hidden = !st.error;
        const row = el('div', 'ac-composer-row'); row.append(count, cancel, send);
        box.append(ta, err, row);
        box.addEventListener('submit', (e) => {
            e.preventDefault();
            const text = ta.value.trim();
            if (!text) { st.error = 'Write something first.'; err.textContent = st.error; err.hidden = false; return; }
            send.disabled = true;
            if (editOf) run(() => api.editComment(st.hex, st.server, editOf.id, text));
            else run(async () => { const c = await api.createComment(st.hex, st.server, { body: text, anchor: cm.anchor, parentId: cm.parentId, board: st.board }); st.active = c.id; st.scrollCard = true; });
        });
        ta.addEventListener('keydown', (e) => { if (e.key === 'Escape') { e.stopPropagation(); cancel.click(); } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) box.requestSubmit(); });
        return box;
    }

    // ── a card or a highlight is made current, and the other follows
    function activate(id, fromCard) {
        st.active = id; st.scrollCard = !fromCard;
        paint();
        const r = st.ranges.get(id);
        if (fromCard && r) (r.startContainer.parentElement || body).scrollIntoView({ block: 'center', behavior: scrollBehavior() });
    }
    body.addEventListener('click', (e) => {
        if (!st.data || !st.ranges.size) return;
        const pos = document.caretPositionFromPoint ? document.caretPositionFromPoint(e.clientX, e.clientY) : null;
        const range = pos ? null : (document.caretRangeFromPoint ? document.caretRangeFromPoint(e.clientX, e.clientY) : null);
        const node = pos ? pos.offsetNode : range && range.startContainer, off = pos ? pos.offset : range && range.startOffset;
        if (!node) return;
        for (const [id, r] of st.ranges) { try { if (r.isPointInRange(node, off)) { if (window.getSelection().isCollapsed) { st.active = id; st.scrollCard = true; paint(); if (window.matchMedia('(max-width: 767px)').matches) setSheet(true); } return; } } catch { /* a range gone with its nodes */ } }
    });

    // ── the Comment button by a selection
    let selTimer = 0;
    function selectionAnchor() {
        const sel = window.getSelection();
        if (!st.data || !st.data.canComment || st.data.locked || !sel || sel.isCollapsed || !sel.rangeCount) return null;
        const r = sel.getRangeAt(0);
        if (!body.contains(r.commonAncestorContainer)) return null;
        const before = document.createRange(); before.setStart(body, 0); before.setEnd(r.startContainer, r.startOffset);
        const after = document.createRange(); after.setStart(r.endContainer, r.endOffset); after.setEnd(body, body.childNodes.length);
        const a = buildAnchor(before.toString(), r.toString(), after.toString(), st.source);
        return a ? { anchor: a, rect: r.getBoundingClientRect() } : null;
    }
    function syncSelButton() {
        const s = selectionAnchor();
        selBtn.hidden = !s;
        if (!s) return;
        const x = Math.min(Math.max(8, s.rect.right - 40), window.innerWidth - 120), y = Math.max(8, Math.min(s.rect.bottom + 6, window.innerHeight - 60));
        selBtn.style.left = x + 'px'; selBtn.style.top = y + 'px';
    }
    document.addEventListener('selectionchange', () => { if (dialog.hidden) return; clearTimeout(selTimer); selTimer = setTimeout(syncSelButton, 120); });
    selBtn.addEventListener('mousedown', (e) => e.preventDefault()); // the selection stays while the button is pressed
    selBtn.addEventListener('click', () => {
        const s = selectionAnchor();
        if (!s) return;
        st.composing = { anchor: s.anchor }; st.error = ''; selBtn.hidden = true;
        window.getSelection().removeAllRanges();
        setSheet(true); render();
        panel.querySelector('.ac-input')?.focus();
    });

    // ── the phone's sheet
    let sheetOverlay = 0; // the sheet's entry in overlay_history.js while it is open on a phone (#5219)
    function setSheet(open, { popped = false } = {}) {
        if (open === panel.classList.contains('ac-sheet-open')) return;
        panel.classList.toggle('ac-sheet-open', open);
        paintToggle();
        if (open && !sheetOverlay && window.innerWidth <= 767) sheetOverlay = pushOverlay(() => setSheet(false, { popped: true }));
        else if (!open && sheetOverlay) { const id = sheetOverlay; sheetOverlay = 0; if (!popped) popOverlay(id); }
    }
    function paintToggle() {
        const open = panel.classList.contains('ac-sheet-open');
        toggle.setAttribute('aria-expanded', String(open));
        toggle.textContent = open ? 'Document' : `Comments · ${st.data ? threads().filter(t => !t.c.deleted).length : 0}`;
        toggle.setAttribute('aria-label', open ? 'Show the document' : 'Show the comments');
    }
    toggle.addEventListener('click', () => setSheet(!panel.classList.contains('ac-sheet-open')));

    return {
        /** A new artifact is on show. */
        open(hex, server, board) { st.hex = hex; st.server = server; st.board = board; st.data = null; st.active = null; st.composing = null; st.error = ''; st.text = false; st.gone = false; st.filter = 'open'; render(); setSheet(false); },
        /** The document was drawn: `text` says whether it is text the comments apply to. */
        docRendered(source, text) { st.source = source || ''; st.text = !!text; if (!st.text) { render(); return; } refresh(); },
        close() { setSheet(false); st.hex = null; st.data = null; clearTimeout(selTimer); selBtn.hidden = true; const hl = window.CSS && CSS.highlights; if (hl) { hl.delete('ac-comment'); hl.delete('ac-active'); } render(); },
    };
}
