/* Save for Later (#4510), the buttons on messages: a bookmark button on each message of an agent's Chat view (live or in History) and of a board, filled when
 * the message is saved. The Saved list and the jump back are in saved.js, which loads when the list is opened. Bookmarks live on this server (the Operator's own,
 * GET/POST/PATCH/DELETE /api/saved); an agent or board on a registered remote server has no button. */
import { state } from './state.js';
import { importOnce } from './import_once.js';
import { showToast, escapeAttr } from './utils.js';
import { isCloud, sessionServer, normServer } from './server_base.js';

// The bookmark is an inline SVG (the shape the Team Artifacts' Keep button uses), filled when saved: the Material Icons subset has no bookmark.
export const BOOKMARK_SVG = '<svg class="bm" width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 2.5h8v11L8 10.5l-4 3z"/></svg>';
export const SAVE_BUTTON = `<button type="button" class="chat-save-btn" title="Save for later" aria-label="Save for later" aria-pressed="false">${BOOKMARK_SVG}</button>`;
export const BOARD_SAVE_BUTTON = (ref, title, said) => `<button type="button" class="mb-save-btn" data-conv-ref="${escapeAttr(ref)}" data-conv-title="${escapeAttr(title)}" data-said="${escapeAttr(said)}" title="Save for later" aria-label="Save for later" aria-pressed="false">${BOOKMARK_SVG}</button>`;

// The conversation a message button belongs to: {kind, ref, agentType, title}, or null where there is nothing to save to (a remote server's).
export function conversationOf(btn) {
    if (btn.classList.contains('mb-save-btn')) {
        const ref = btn.dataset.convRef || '';
        if (!ref || ref.includes('/') || ref.includes('@')) return null; // a remote board's key carries its server
        return { kind: 'board', ref, agentType: '', title: btn.dataset.convTitle || ref };
    }
    const s = state.currentSession;
    if (!s) return null;
    if (s.type === 'history') return s.name ? { kind: 'agent', ref: s.name, agentType: s.agent_type || '', title: s.display_name || s.name } : null;
    if (s.type !== 'live' || !s.session_id) return null;
    if (normServer(sessionServer(s)) !== 'local') return null;
    return { kind: 'agent', ref: s.session_id, agentType: s.agent_type || '', title: s.display_name || s.name || s.session_id };
}

// `${kind}:${ref}` -> { items: Map(key -> bookmark), loaded: Promise }, kept while the conversation is on screen and refreshed when the list changes.
const sets = new Map();
const setKey = c => `${c.kind}:${c.ref}`;

function setFor(c) {
    const k = setKey(c);
    let e = sets.get(k);
    if (!e) {
        e = { items: new Map(), loaded: null };
        sets.set(k, e);
    }
    if (!e.loaded) {
        e.loaded = fetch(`/api/saved?kind=${encodeURIComponent(c.kind)}&conv=${encodeURIComponent(c.ref)}`).then(r => (r.ok ? r.json() : { items: [] }))
            .then(d => { e.items = new Map((d.items || []).map(i => [i.key, i])); return e; })
            .catch(() => { e.loaded = null; return e; });
    }
    return e.loaded; // resolves to the entry once the conversation's bookmarks are read
}

function paint(btn, saved) {
    btn.setAttribute('aria-pressed', saved ? 'true' : 'false');
    const label = saved ? 'Remove from saved' : 'Save for later';
    btn.setAttribute('aria-label', label);
    btn.title = label;
    btn.classList.toggle('saved', saved);
}

function keyOf(btn) {
    if (btn.classList.contains('mb-save-btn')) return btn.closest('[data-message-id]')?.dataset.messageId || '';
    return btn.closest('[data-msg-key]')?.dataset.msgKey || '';
}

/** Show each save button's state (filled when saved). A button with no conversation to save to is hidden. */
export async function syncSavedButtons(root) {
    if (!root || isCloud()) return;
    const btns = [...root.querySelectorAll('.chat-save-btn, .mb-save-btn')];
    if (!btns.length) return;
    const c = conversationOf(btns[0]);
    for (const b of btns) b.hidden = !conversationOf(b);
    if (!c) return;
    const e = await setFor(c);
    for (const b of btns) { const k = keyOf(b); if (k) paint(b, e.items.has(k)); }
}

/** The Saved list changed (done, removed, saved elsewhere): forget what is known and repaint the buttons on screen. */
export function savedChanged() {
    sets.clear();
    for (const root of watched()) syncSavedButtons(root);
}

const WATCHED = ['live-history-messages', 'history-messages', 'mb-messages'];
const watched = () => WATCHED.map(id => document.getElementById(id)).filter(Boolean);

let pending = new Set();
function schedule(root) {
    if (!pending.size) requestAnimationFrame(() => { const roots = [...pending]; pending = new Set(); roots.forEach(syncSavedButtons); });
    pending.add(root);
}

// Messages come in by many routes (a page, a poll, older pages above): a rendered message gets its state when it appears.
function watch() {
    for (const root of watched()) {
        if (root._savedWatch) continue;
        root._savedWatch = new MutationObserver(() => schedule(root));
        root._savedWatch.observe(root, { childList: true, subtree: true });
    }
}
if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', watch); else watch();

// The top bar's Saved button shows how many are open (#saved-btn .saved-count); refreshed at start and whenever the list changes.
let countTimer = 0;
export function setSavedCount(n) {
    const b = document.getElementById('saved-btn');
    if (!b) return;
    const badge = b.querySelector('.saved-count');
    if (badge) { badge.textContent = n > 0 ? (n > 99 ? '99+' : String(n)) : ''; badge.hidden = !(n > 0); }
    b.setAttribute('aria-label', n > 0 ? `Saved for later, ${n} open` : 'Saved for later');
    b.classList.toggle('on', n > 0);
    const tc = document.getElementById('saved-tab-count');
    if (tc) tc.textContent = n > 0 ? (n > 99 ? '99+' : String(n)) : '';
}
export function refreshSavedCount() {
    clearTimeout(countTimer);
    countTimer = setTimeout(() => {
        if (isCloud()) return;
        fetch('/api/saved?limit=0').then(r => (r.ok ? r.json() : null)).then(d => { if (d) setSavedCount(d.open || 0); }).catch(() => {});
    }, 50);
}
document.addEventListener('coral:saved-changed', refreshSavedCount);
if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', refreshSavedCount); else refreshSavedCount();

function postBody(c, key, btn) {
    const text = btn.closest('.mb-message')?.querySelector('.mb-message-body')?.innerText || btn.closest('.chat-bubble')?.querySelector('.message-text')?.innerText || '';
    return { kind: c.kind, ref: c.ref, key, agent_type: c.agentType, title: c.title, snippet: text.slice(0, 1000), said_at: btn.dataset.said || btn.closest('[data-said]')?.dataset.said || '' };
}

async function save(c, key, body) {
    const r = await fetch('/api/saved', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const out = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(out.error || `HTTP ${r.status}`);
    (await setFor(c)).items.set(key, out);
    return out;
}

document.addEventListener('click', async (e) => {
    const btn = e.target.closest && e.target.closest('.chat-save-btn, .mb-save-btn');
    if (!btn) return;
    e.preventDefault();
    const c = conversationOf(btn);
    const key = keyOf(btn);
    if (!c || !key) return;
    const entry = await setFor(c);
    const have = entry.items.get(key);
    const body = have ? { kind: have.kind, ref: have.ref, key: have.key, agent_type: have.agent_type, title: have.title, snippet: have.snippet, said_at: have.said_at } : postBody(c, key, btn);
    btn.disabled = true;
    try {
        if (have) {
            const r = await fetch(`/api/saved/${have.id}`, { method: 'DELETE' });
            if (!r.ok && r.status !== 404) throw new Error(`HTTP ${r.status}`);
            entry.items.delete(key);
            paint(btn, false);
            showToast('Removed from saved.', false, 6000, { action: { label: 'Undo', onClick: async () => {
                try { await save(c, key, body); syncSavedButtons(btn.closest('#live-history-messages, #history-messages, #mb-messages')); document.dispatchEvent(new CustomEvent('coral:saved-changed')); } catch (err) { showToast(`Could not save the message again: ${err.message}`, true); }
            } } });
        } else {
            await save(c, key, body);
            paint(btn, true);
            showToast('Saved.', false, 5000, { action: { label: 'View saved', onClick: () => importOnce('./saved.js').then(m => m.openSaved()).catch(() => {}) } });
        }
        document.dispatchEvent(new CustomEvent('coral:saved-changed'));
    } catch (err) {
        showToast(`${have ? 'Could not take the message off the list' : 'Could not save the message'}: ${err.message}`, true);
    } finally {
        btn.disabled = false;
        btn.focus({ preventScroll: true });
    }
});
