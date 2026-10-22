/* Comments in the artifact viewer (#5182, phase 1): a side panel (a bottom sheet on a phone) of the artifact's threads, highlights of what each is about, a Comment button by a text selection,
 * a composer, and the actions the server says this viewer may use. Everything shown is text (textContent, never HTML). The server is the gate: a control the viewer cannot use is hidden, a write
 * it refuses shows the server's one-line reason. Where a comment sits is artifact_anchor.js; the routes are artifact_comments_api.js. */
import { importOnce } from './import_once.js';
const { buildAnchor, locateQuote, indexText } = await importOnce('./artifact_anchor.js');
const api = await importOnce('./artifact_comments_api.js');
import { showToast } from './utils.js';
import { scrollBehavior } from './motion.js';
import { pushOverlay, popOverlay } from './overlay_history.js';
import { state } from './state.js';
import { boardOfPage } from './dock_page.js';
import { boardKeyName, sessionTeamKey } from './server_base.js';
import { browserLocal } from './safe_storage.js';

const OPEN_KEY = 'coral-artifact-comments-open'; // explicit choice shared by the viewer and docked preview, at every width

/** The team the artifact is opened from, as the bare board name the comments routes take: the team's page, the board page, or the open agent's own team; none otherwise (#5182). The viewer and the docked preview pane (#5311) both ask. */
export function commentBoard(opts) {
    if (opts && opts.board) return opts.board;
    const key = state.selectedTeam || boardOfPage() || sessionTeamKey(state.currentSession);
    return key ? boardKeyName(key) : undefined;
}

const BODY_MAX = 8192; // the server's cap on a comment (bytes; most text is one byte a character)
const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
const btn = (cls, text, label) => { const b = el('button', 'btn ' + cls, text); b.type = 'button'; if (label) b.setAttribute('aria-label', label); return b; };
const KIND = { owner: 'owner', hub: 'Coral Hub', agent: 'agent' };

const plural = (n, unit) => `${n} ${unit}${n === 1 ? '' : 's'} ago`;
function ago(iso) {
    const t = Date.parse(iso);
    if (!t) return '';
    const s = Math.max(0, (Date.now() - t) / 1000);
    if (s < 60) return 'just now';
    if (s < 3600) return plural(Math.floor(s / 60), 'minute');
    if (s < 86400) return plural(Math.floor(s / 3600), 'hour');
    if (s < 172800) return 'yesterday';
    return plural(Math.floor(s / 86400), 'day');
}

// An initials circle for an author, as Docs and Paper draw one: the first letters of the name's words, on one of a few fixed hues chosen by the author's id, so the same person is the same
// colour in every thread. Decoration only (the name is beside it): aria-hidden.
const AVATAR_HUES = [212, 262, 330, 8, 28, 150, 186, 48];
function avatar(c) {
    const name = c.authorName || c.authorId || '?';
    const parts = name.split(/[\s._-]+/).filter(Boolean);
    const initials = ((parts[0] || '?')[0] + (parts.length > 1 ? parts[1][0] : '')).toUpperCase();
    let h = 0; for (const ch of (c.authorId || name)) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
    const a = el('span', 'ac-avatar', initials); a.setAttribute('aria-hidden', 'true');
    a.style.setProperty('--ac-hue', String(AVATAR_HUES[h % AVATAR_HUES.length]));
    return a;
}

/** Wire comments into the viewer `dialog` (or, #5311, the docked preview pane): `body` is where the document is drawn, `host` the element the panel goes into. Returns { open, docRendered, close, toggle }.
 *  opts (the preview pane's, #5311; the viewer passes none): toggleHost: the owner puts `toggle` where it wants it (the viewer's header is the default); sheet(): whether the panel is a bottom sheet over the
 *  document now (the viewer: a phone's width; the pane: the pane's own width, so a narrow docked pane on a desktop gets the sheet too); shown(): whether the pane is on screen; observe: the element whose
 *  size decides sheet(). */
let panelSeq = 0; // one id per panel (the viewer's and the docked pane's)
export function attachComments(dialog, body, host, opts = {}) {
    const st = { hex: null, server: undefined, board: undefined, source: '', text: false, data: null, showResolved: false, active: null, composing: null, busy: false, error: '', ranges: new Map(), orphan: new Set(), gone: false };
    const panel = el('aside', 'ac-panel'); panel.setAttribute('aria-label', 'Comments'); panel.hidden = true;
    panel.id = `ac-panel-${++panelSeq}`; // the toggle names it (aria-controls): where the opened comments are
    const toggle = btn('ac-toggle', 'Comments', 'Comments: show the comments'); toggle.hidden = true; toggle.setAttribute('aria-expanded', 'false');
    toggle.setAttribute('aria-controls', panel.id);
    const selBtn = btn('ac-selbtn', 'Add comment', 'Add comment on the selected text'); selBtn.hidden = true;
    host.append(panel); if (!opts.toggleHost) dialog.querySelector('.artifact-viewer-header')?.insertBefore(toggle, dialog.querySelector('.artifact-viewer-download'));
    dialog.append(selBtn);
    // A live region for what the panel does (a comment posted, resolved, the one stepped to): outside the panel, which is out of the accessibility tree while the phone's sheet is shut.
    const live = el('div', 'ac-live'); live.setAttribute('role', 'status'); live.setAttribute('aria-live', 'polite'); live.setAttribute('aria-atomic', 'true');
    dialog.append(live);
    let liveFlip = false;
    const announce = (msg) => { liveFlip = !liveFlip; live.textContent = msg + (liveFlip ? '' : '\u00a0'); }; // the same words twice are still a change
    // The document is a region a keyboard reaches; ] and [ step through the comments from it. (While an artifact is on show: the docked pane's body also draws files and images.)
    const region = (on) => { if (on) { body.tabIndex = 0; body.setAttribute('role', 'region'); body.setAttribute('aria-label', 'Document'); body.setAttribute('aria-keyshortcuts', '] ['); } else { for (const a of ['tabindex', 'role', 'aria-label', 'aria-keyshortcuts']) body.removeAttribute(a); } };
    const phone = window.matchMedia('(max-width: 767px)');
    const sheet = opts.sheet || (() => phone.matches); // the panel is a bottom sheet over the document
    const isOpen = () => panel.classList.contains('ac-sheet-open');
    const remember = (open) => browserLocal.set(OPEN_KEY, open ? 'open' : 'closed');
    const remembered = () => { const choice = browserLocal.get(OPEN_KEY); return choice === 'open' ? true : choice === 'closed' ? false : null; };
    const syncOn = () => dialog.classList.toggle('ac-on', !!st.shownNow && (phone.matches || isOpen()));
    const shown = opts.shown || (() => !dialog.hidden);
    // Available on both the side column and the bottom sheet; never reserve a
    // column the person cannot hide.
    const syncToggle = () => { toggle.hidden = !st.shownNow; };
    // The drawn text changes after the comments were placed (a code block coloured once highlight.js has loaded): their ranges follow it, a moment after it settles (#5311).
    let relocateTimer = 0;
    new MutationObserver(() => { clearTimeout(relocateTimer); relocateTimer = setTimeout(() => { if (st.data && st.text) paint(true); }, 150); }).observe(body, { childList: true, subtree: true, characterData: true });

    // ── the data
    const threads = () => {
        const all = (st.data?.comments || []);
        const replies = new Map();
        for (const c of all) if (c.parentId != null && !c.deleted) { if (!replies.has(c.parentId)) replies.set(c.parentId, []); replies.get(c.parentId).push(c); }
        return all.filter(c => c.parentId == null && (!c.deleted || replies.has(c.id))).map(c => ({ c, replies: replies.get(c.id) || [] }));
    };
    const openCount = () => threads().filter(t => !t.c.deleted && !t.c.resolved).length;
    async function refresh() {
        if (!st.hex || !st.text) return;
        const hex = st.hex;
        try {
            const data = await api.listComments(hex, st.server, st.board);
            if (hex !== st.hex) return;
            st.data = data; st.gone = false;
            if (!st.visibilitySet) {
                st.visibilitySet = true;
                setSheet(remembered() ?? openCount() > 0);
            }
        } catch (e) {
            if (hex !== st.hex) return;
            st.gone = true; st.data = null; // no comments routes here (an older Coral, a hub that does not carry them): the viewer is as it was
        }
        render(); paint();
    }

    // ── highlights: the quote found again in what is drawn
    const textNodes = () => { const out = []; const w = document.createTreeWalker(body, NodeFilter.SHOW_TEXT); for (let n = w.nextNode(); n; n = w.nextNode()) out.push(n); return out; };
    // The index of the drawn text, kept while the text nodes are the ones it was made from (a megabyte takes a while to index, #5255); drawn again when any node was replaced, added, removed or changed in length.
    let docIx = null;
    function docIndex() {
        const nodes = textNodes();
        if (docIx && docIx.nodes.length === nodes.length && nodes.every((n, i) => n === docIx.nodes[i] && n.nodeValue.length === docIx.lens[i])) return docIx.ix;
        docIx = { nodes, lens: nodes.map(n => n.nodeValue.length), ix: indexText(nodes) };
        return docIx.ix;
    }
    // Where each comment's quote sits now. Needed when the comments or the document change, not when a comment is made current.
    function locate() {
        st.ranges.clear(); st.orphan.clear();
        if (!st.data) return;
        const ix = docIndex();
        for (const { c } of threads()) {
            if (c.deleted || !c.anchor || c.anchor.whole) continue;
            const hit = locateQuote(ix.text, c.anchor);
            if (!hit) { st.orphan.add(c.id); continue; }
            const first = ix.at(hit.start), last = ix.at(hit.end - 1);
            const r = document.createRange();
            r.setStart(first.node, first.offset);
            r.setEnd(last.node, last.offset + 1);
            st.ranges.set(c.id, r);
        }
    }
    function highlight() {
        const hl = window.CSS && CSS.highlights;
        if (hl) { hl.delete('ac-comment'); hl.delete('ac-active'); }
        if (!st.data || !hl || typeof Highlight !== 'function') return;
        const open = [];
        for (const { c } of threads()) { const r = st.ranges.get(c.id); if (r && !c.resolved) open.push(r); }
        if (open.length) hl.set('ac-comment', new Highlight(...open));
        const a = st.ranges.get(st.active);
        if (a) hl.set('ac-active', new Highlight(a));
    }
    // The thread the pointer is on (card or text): its text is lit a little, the card too.
    function hover(id) {
        const hl = window.CSS && CSS.highlights;
        panel.querySelectorAll('.ac-card.ac-hover').forEach(n => n.classList.remove('ac-hover'));
        if (hl) hl.delete('ac-hover');
        if (id == null) return;
        panel.querySelector(`.ac-card[data-id="${id}"]`)?.classList.add('ac-hover');
        const r = st.ranges.get(id);
        if (hl && r && typeof Highlight === 'function') hl.set('ac-hover', new Highlight(r));
    }
    function paint(relocate = true) {
        if (relocate) locate();
        highlight();
        render();
    }

    // ── the panel
    // What has focus in the panel, so a redraw (every action redraws it) puts it back on the same control, or, when that control is gone (a posted comment's composer), on the current card.
    function focusKey() {
        const a = document.activeElement;
        if (!a || !panel.contains(a)) return null;
        const card = a.closest('.ac-card, .ac-reply');
        const cls = [...a.classList].find(c => c.startsWith('ac-')) || a.tagName;
        return { card: card ? card.dataset.id : '', cls, text: (a.textContent || '').trim(), isCard: a.classList.contains('ac-card') };
    }
    function restoreFocus(k) {
        if (!k) return;
        const scope = k.card ? panel.querySelector(`.ac-card[data-id="${k.card}"], .ac-reply[data-id="${k.card}"]`) : panel;
        const cand = scope ? [...scope.querySelectorAll('button, textarea, a')].filter(n => n.classList.contains(k.cls) && (n.textContent || '').trim() === k.text) : [];
        const target = cand[0] || panel.querySelector('.ac-card.ac-active') || panel.querySelector('.ac-filter[aria-pressed="true"]');
        if (target) { if (!target.matches('button, textarea, a')) target.tabIndex = -1; target.focus({ preventScroll: true }); }
    }
    function render() {
        const kept = focusKey();
        panel.replaceChildren();
        const show = st.text && !st.gone && !!st.data;
        panel.hidden = !show; st.shownNow = show; syncToggle();
        syncOn();
        paintToggle();
        if (!show) return;
        const list = threads();
        const head = el('div', 'ac-head');
        const h = el('h3', 'ac-title', 'Comments'); h.append(el('span', 'ac-count', String(openCount())));
        head.append(h);
        // Resolved threads are out of the way, as in Docs: one toggle brings them back (and only while there are any).
        const nResolved = list.filter(t => t.c.resolved && !t.c.deleted).length;
        if (nResolved || st.showResolved) {
            const sr = btn('ac-showresolved', st.showResolved ? 'Hide resolved' : `Show resolved (${nResolved})`); sr.setAttribute('aria-pressed', String(st.showResolved));
            sr.addEventListener('click', () => { st.showResolved = !st.showResolved; render(); });
            head.append(sr);
        }
        const close = btn('ac-sheet-close', '✕', 'Hide the comments'); close.addEventListener('click', () => { userSet(false); toggle.focus(); }); head.append(close);
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
        const shown = list.filter(t => st.showResolved || !t.c.resolved);
        for (const t of shown) wrap.append(card(t));
        if (!shown.length) wrap.append(el('p', 'ac-empty', 'No open comments. Select some text to add one.'));
        panel.append(wrap);
        if (st.composing && st.composing.anchor) panel.append(composer());
        else if (d.canComment && !d.locked) {
            const whole = btn('ac-whole', 'Comment on the whole document'); whole.addEventListener('click', () => { st.composing = { anchor: { whole: true } }; st.error = ''; render(); panel.querySelector('.ac-input')?.focus(); });
            panel.append(whole);
        }
        const act = panel.querySelector('.ac-card.ac-active'); if (act && st.scrollCard) { st.scrollCard = false; act.scrollIntoView({ block: 'nearest', behavior: scrollBehavior() }); }
        syncInert();
        restoreFocus(kept);
    }

    // One entry of a thread (the comment, then each reply), as Docs and Paper draw it: an avatar, the name in bold, a dot and the time, the text under it. On the thread's first entry the state (lines,
    // resolved, text changed) follows the time and Resolve sits at the right; a thin line joins the avatars down the thread (css). What this viewer may do to the entry (Edit, Delete) shows
    // when the card is hovered or has focus, always on a touch screen.
    function entry(c, top) {
        const e = el('div', 'ac-entry' + (top ? '' : ' ac-reply')); e.dataset.id = String(c.id);
        e.append(avatar(c));
        const main = el('div', 'ac-entry-main');
        const m = el('div', 'ac-meta');
        m.append(el('span', 'ac-author', c.authorName || c.authorId || 'unknown'));
        if (KIND[c.authorKind]) m.append(el('span', 'ac-kind', KIND[c.authorKind]));
        const dot = el('span', 'ac-dot', '·'); dot.setAttribute('aria-hidden', 'true'); m.append(dot);
        const time = el('span', 'ac-time', ago(c.createdAt)); time.title = c.createdAt; m.append(time);
        if (c.editedAt) m.append(el('span', 'ac-pill', 'edited'));
        let resolveBtn = null;
        if (top) {
            const a = c.anchor || {};
            if (a.start_line) m.append(el('span', 'ac-lines', a.end_line && a.end_line !== a.start_line ? `lines ${a.start_line}-${a.end_line}` : `line ${a.start_line}`));
            if (c.resolved) m.append(el('span', 'ac-pill ac-pill-resolved', 'resolved'));
            if (st.orphan.has(c.id)) m.append(el('span', 'ac-pill ac-pill-orphan', 'text changed'));
            if (!c.deleted && c.can.resolve) {
                const r = act(c.resolved ? 'Reopen' : 'Resolve', () => run(() => (c.resolved ? api.reopenComment : api.resolveComment)(st.hex, st.server, c.id), c.resolved ? 'Comment reopened' : 'Comment resolved'));
                r.classList.add('ac-resolve'); r.setAttribute('aria-label', `${c.resolved ? 'Reopen' : 'Resolve'} the thread by ${c.authorName || c.authorId || 'unknown'}`);
                resolveBtn = r;
            }
        }
        if (resolveBtn) { const row = el('div', 'ac-headrow'); row.append(m, resolveBtn); main.append(row); } // Resolve at the top right: the name line wraps beside it, never under it
        else main.append(m);
        main.append(editing(c) || el('p', 'ac-body' + (c.deleted ? ' ac-deleted' : ''), c.deleted ? 'Comment deleted' : c.body));
        main.append(actions(c, top));
        e.append(main);
        return e;
    }
    function card({ c, replies }) {
        const li = el('li', 'ac-card' + (st.active === c.id ? ' ac-active' : '') + (c.resolved ? ' ac-resolved' : '')); li.dataset.id = String(c.id); li.setAttribute('aria-label', `Comment by ${c.authorName || c.authorId || 'unknown'}`); li.setAttribute('role', 'group');
        const a = c.anchor || {};
        const q = el('blockquote', 'ac-quote' + (st.orphan.has(c.id) ? ' ac-orphan' : ''), a.whole ? 'The whole document' : a.quote || '');
        li.append(q);
        li.append(entry(c, true));
        for (const r of replies) li.append(entry(r, false));
        // The reply box at the foot of the card, where Docs has it; it opens the composer in place.
        if (!c.deleted && c.can.reply && !(st.composing && st.composing.parentId === c.id)) {
            const rb = btn('ac-replybox', 'Reply…', `Reply to the comment by ${c.authorName || c.authorId || 'unknown'}`);
            rb.addEventListener('click', () => { st.composing = { parentId: c.id }; st.error = ''; render(); panel.querySelector('.ac-input')?.focus(); });
            li.append(rb);
        }
        if (st.composing && st.composing.parentId === c.id) li.append(composer());
        li.addEventListener('click', (e) => { if (e.target.closest('button, textarea')) return; activate(c.id, true); });
        // The card and its highlight light each other while the pointer is on either (a click makes them current).
        li.addEventListener('pointerenter', () => hover(c.id)); li.addEventListener('pointerleave', () => hover(null));
        return li;
    }
    function actions(c, top) {
        const row = el('div', 'ac-actions');
        if (c.deleted) return row;
        if (c.can.edit) row.append(act('Edit', () => { st.composing = { editId: c.id }; st.error = ''; render(); panel.querySelector('.ac-input')?.focus(); }));
        if (c.can.delete) row.append(act('Delete', () => confirmThen('Delete this comment?', () => run(() => api.deleteComment(st.hex, st.server, c.id), 'Comment deleted'))));
        // not an .ac-act: it writes nothing, so a read-only viewer has it too
        if (top && st.ranges.has(c.id)) { const j = btn('ac-jump', 'Show in text', 'Show this comment\'s text in the document'); j.addEventListener('click', () => jumpTo(c.id)); row.append(j); }
        return row;
    }
    const act = (text, fn) => { const b = btn('ac-act', text); b.addEventListener('click', fn); return b; };
    function confirmThen(msg, fn) { if (window.showConfirmModal) window.showConfirmModal('Delete comment', msg, fn); else fn(); }
    async function run(fn, said) {
        if (st.busy) return;
        st.busy = true;
        try { await fn(); st.error = ''; st.composing = null; await refresh(); if (said) announce(said); }
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
            if (editOf) run(() => api.editComment(st.hex, st.server, editOf.id, text), 'Comment saved');
            else run(async () => { const c = await api.createComment(st.hex, st.server, { body: text, anchor: cm.anchor, parentId: cm.parentId, board: st.board }); st.active = c.id; st.scrollCard = true; }, cm.parentId != null ? 'Reply posted' : 'Comment posted');
        });
        ta.addEventListener('keydown', (e) => { if (e.key === 'Escape') { e.stopPropagation(); cancel.click(); } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) box.requestSubmit(); });
        return box;
    }

    // ── a comment's text from its card, and the next or previous comment from the document, by keyboard
    function jumpTo(id) {
        activate(id, true);
        if (sheet()) setSheet(false);
        body.focus({ preventScroll: true });
    }
    function step(dir) {
        const ids = [...panel.querySelectorAll('.ac-card')].map(c => Number(c.dataset.id)).filter(id => st.ranges.has(id));
        if (!ids.length) { announce('No comments on this text'); return; }
        const at = ids.indexOf(st.active);
        const id = ids[at < 0 ? (dir > 0 ? 0 : ids.length - 1) : (at + dir + ids.length) % ids.length];
        activate(id, true); st.scrollCard = true; paint();
        const c = threads().find(t => t.c.id === id);
        announce(`Comment ${ids.indexOf(id) + 1} of ${ids.length}: ${c ? c.c.body.slice(0, 80) : ''}`);
    }
    body.addEventListener('keydown', (e) => {
        if (e.target !== body || e.ctrlKey || e.metaKey || e.altKey || !st.data) return;
        if (e.key === ']') { e.preventDefault(); step(1); } else if (e.key === '[') { e.preventDefault(); step(-1); }
    });
    // A hidden panel is out of reach at every width; an open sheet also removes the document behind it from Tab and screen readers.
    function syncInert() {
        const open = panel.classList.contains('ac-sheet-open');
        panel.inert = !st.shownNow || !open;
        body.inert = !!st.shownNow && sheet() && open;
    }
    phone.addEventListener('change', () => { syncInert(); syncOn(); });
    if (opts.observe && typeof ResizeObserver === 'function') new ResizeObserver(() => { syncInert(); syncToggle(); paintToggle(); }).observe(opts.observe); // the pane's width decides whether the panel is a sheet

    // ── a card or a highlight is made current, and the other follows
    function activate(id, fromCard) {
        st.active = id; st.scrollCard = !fromCard;
        paint(false);
        const r = st.ranges.get(id);
        if (fromCard && r) (r.startContainer.parentElement || body).scrollIntoView({ block: 'center', behavior: scrollBehavior() });
    }
    // The comment whose text is under the point, if any.
    function rangeIdAt(x, y) {
        const pos = document.caretPositionFromPoint ? document.caretPositionFromPoint(x, y) : null;
        const range = pos ? null : (document.caretRangeFromPoint ? document.caretRangeFromPoint(x, y) : null);
        const node = pos ? pos.offsetNode : range && range.startContainer, off = pos ? pos.offset : range && range.startOffset;
        if (!node) return null;
        for (const [id, r] of st.ranges) { try { if (r.isPointInRange(node, off)) return id; } catch { /* a range gone with its nodes */ } }
        return null;
    }
    body.addEventListener('click', (e) => {
        if (!st.data || !st.ranges.size) return;
        const id = rangeIdAt(e.clientX, e.clientY);
        if (id != null && window.getSelection().isCollapsed) { st.active = id; st.scrollCard = true; paint(false); userSet(true); } // a commented passage opens the panel at its thread
    });
    let hoverFrame = 0;
    body.addEventListener('mousemove', (e) => {
        if (!st.data || !st.ranges.size || hoverFrame) return;
        hoverFrame = requestAnimationFrame(() => { hoverFrame = 0; hover(rangeIdAt(e.clientX, e.clientY)); });
    });
    body.addEventListener('mouseleave', () => hover(null));

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
    document.addEventListener('selectionchange', () => { if (!shown()) return; clearTimeout(selTimer); selTimer = setTimeout(syncSelButton, 120); });
    selBtn.addEventListener('mousedown', (e) => e.preventDefault()); // the selection stays while the button is pressed
    selBtn.addEventListener('click', () => {
        const s = selectionAnchor();
        if (!s) return;
        st.composing = { anchor: s.anchor }; st.error = ''; selBtn.hidden = true;
        window.getSelection().removeAllRanges();
        userSet(true); render(); // the new comment opens the panel
        panel.querySelector('.ac-input')?.focus();
    });

    // ── the phone's sheet
    let sheetOverlay = 0; // the sheet's entry in overlay_history.js while it is open on a phone (#5219)
    function setSheet(open, { popped = false } = {}) {
        if (open === panel.classList.contains('ac-sheet-open')) return;
        panel.classList.toggle('ac-sheet-open', open);
        syncInert();
        syncOn();
        paintToggle();
        if (open && !sheetOverlay && window.innerWidth <= 767) sheetOverlay = pushOverlay(() => setSheet(false, { popped: true }));
        else if (!open && sheetOverlay) { const id = sheetOverlay; sheetOverlay = 0; if (!popped) popOverlay(id); }
    }
    function paintToggle() {
        const open = panel.classList.contains('ac-sheet-open');
        toggle.setAttribute('aria-expanded', String(open));
        const n = openCount();
        toggle.textContent = n ? `Comments (${n})` : 'Comments';
        toggle.setAttribute('aria-label', toggle.textContent);
    }
    // Remember explicit actions; the count-based initial default never writes a preference.
    function userSet(open) { setSheet(open); remember(open); }
    toggle.addEventListener('click', () => userSet(!isOpen()));
    // Escape closes the docked pane's open sheet and puts focus back on its toggle (#5371); the composer's own Escape (cancel) stops the key before it gets here, and the viewer's dialog keeps Escape for itself.
    panel.addEventListener('keydown', (e) => {
        if (e.key !== 'Escape' || e.defaultPrevented || !opts.sheet || !sheet() || !panel.classList.contains('ac-sheet-open')) return;
        e.preventDefault(); e.stopPropagation(); setSheet(false); toggle.focus();
    });

    return {
        /** The Comments button in either layout: the owner places it when toggleHost was given. */
        toggle,
        /** A new artifact is on show. */
        open(hex, server, board) { region(false); { const hl = window.CSS && CSS.highlights; if (hl) { hl.delete('ac-comment'); hl.delete('ac-active'); } } docIx = null; st.hex = hex; st.server = server; st.board = board; st.data = null; st.active = null; st.composing = null; st.error = ''; st.text = false; st.gone = false; st.filter = 'open'; st.visibilitySet = false; render(); setSheet(false); },
        /** The document was drawn: `text` says whether it is text the comments apply to. */
        docRendered(source, text) { st.source = source || ''; st.text = !!text; if (!st.text) { render(); return; } region(true); refresh(); },
        close() { hover(null); setSheet(false); region(false); docIx = null; st.ranges.clear(); st.hex = null; st.data = null; clearTimeout(selTimer); selBtn.hidden = true; const hl = window.CSS && CSS.highlights; if (hl) { hl.delete('ac-comment'); hl.delete('ac-active'); } render(); },
    };
}
