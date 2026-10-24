/* A person's chat message held behind the agent's question (#6611, the dashboard half of #6610): while the agent waits on a permission or question box, the server holds the message and types it once no box
 * is left. The composer says so under the box, one line per held message: "Queued: <its first words>. Sends after you answer the question above." with Cancel. The line goes when the message is typed (its
 * ack), refused (its reason shows as the composer's error) or cancelled (its text comes back into the box). The list is this window's own sends that the server answered "held" and, when the live row
 * carries `held_messages` (the server's list, so a reload or a second window shows the same), that list. Nothing here types or sends: Cancel asks the server to drop the message. */
import { state } from './state.js';
import { sessionServer, serverFetch } from './server_base.js';
import { showToast } from './utils.js';

const sent = new Map();     // `${sid}|${id}` -> text, a send over the socket that has not been answered yet
const queued = new Map();   // sid -> Map(held id -> { id: held id, text, composerId, sending }): the server's id for the message is what Cancel names
const byComposer = new Map(); // `${sid}|${composer frame id}` -> held id: the ack and the refusal come by the composer's own frame id
const cancelling = new Set(); // `${sid}|${id}` whose cancel is under way: its refusal is the person's own doing, not an error
const key = (sid, id) => `${sid}|${id}`;
const WORDS = 40; // a long message stays on one line with its Cancel at the end
const open = new Set();      // `${sid}|${held id}` of the queued lines the person has tapped open to read whole (#6794); a line leaves it when it is sent or cancelled

/** The whole message on one line of words (what a tap on a clipped line shows). */
const wholeText = text => String(text || '').replace(/\s+/g, ' ').trim();

/** First words of a message, for the line and for Cancel's name. */
export function firstWords(text) {
    const t = String(text || '').replace(/\s+/g, ' ').trim();
    const c = [...t];
    return c.length > WORDS ? c.slice(0, WORDS).join('') + '…' : t;
}

/** A send over the socket (frame `id`): remembered until the server answers it, so a "held" answer knows what was held. */
export function rememberSend(sid, id, text) { if (sid) sent.set(key(sid, id), String(text || '')); }

function put(sid, heldId, composerId, text) {
    if (!sid || heldId === undefined || heldId === null) return;
    if (!queued.has(sid)) queued.set(sid, new Map());
    queued.get(sid).set(heldId, { id: heldId, composerId, text: String(text || ''), sending: false });
    if (composerId !== undefined && composerId !== null) byComposer.set(key(sid, composerId), heldId);
}

/** The server answered the send "held": over the socket by the composer's frame id (`id`, with the server's `heldId`), or an HTTP send that returned `held_id` (no composer id). */
export function noteHeld(sid, id, text, heldId) {
    const k = key(sid, id);
    put(sid, heldId !== undefined && heldId !== null ? heldId : id, id, text !== undefined ? text : sent.get(k));
    paint();
}

/** The message was typed, refused or cancelled (named by the composer's frame id): it is not held any more. */
export function noteDone(sid, id, how) {
    const k = key(sid, id);
    sent.delete(k);
    const heldId = byComposer.has(k) ? byComposer.get(k) : id;
    byComposer.delete(k);
    const m = queued.get(sid);
    open.delete(key(sid, heldId));
    if (m && m.delete(heldId)) {
        if (!m.size) queued.delete(sid);
        if (how === 'sent') say('Sent.'); // the message that waited was typed: the line going is not left unsaid
        else paint();
    }
}

let said = '', saidTimer = 0;
/** Said in the queued region (a status): what happened to a message that left the list. It stays for a few seconds, then goes. */
function say(text) {
    said = text;
    clearTimeout(saidTimer);
    saidTimer = setTimeout(() => { said = ''; paint(); }, 8000);
    paint();
}

/** Was this message's cancel the person's own (so its refusal is not an error)? */
export function wasCancelled(sid, id) { return cancelling.delete(key(sid, id)); }

/** The live row's own list (`held_messages: [{id, text}]`) is the truth for that agent when the server sends it. */
export function syncQueuedFromRow(row) {
    if (!row || !row.session_id || !Array.isArray(row.held_messages)) return;
    const next = new Map();
    const had = queued.get(row.session_id);
    for (const h of row.held_messages) if (h && typeof h.text === 'string' && h.text) next.set(h.id, { id: h.id, composerId: had && had.get(h.id) ? had.get(h.id).composerId : undefined, text: h.text, sending: !!h.sending });
    const same = had && had.size === next.size && [...next].every(([id, v]) => had.get(id) && had.get(id).text === v.text && had.get(id).sending === v.sending);
    if (same || (!had && !next.size)) return;
    if (next.size) queued.set(row.session_id, next); else queued.delete(row.session_id);
    paint();
}

function sessionOf(sid) { return (state.liveSessions || []).find(s => s && s.session_id === sid) || (state.currentSession && state.currentSession.session_id === sid ? state.currentSession : null); }

/** Ask the server to drop one held message (DELETE .../held/{held id}). Its refusal comes by the composer's frame id: that one is the person's own doing, not an error. */
export async function cancelQueued(sid, heldId) {
    const s = sessionOf(sid);
    if (!s) return;
    const e = queued.get(sid) && queued.get(sid).get(heldId);
    const own = key(sid, e && e.composerId !== undefined ? e.composerId : heldId);
    cancelling.add(own);
    try {
        const r = await serverFetch(sessionServer(s), `/api/sessions/live/${encodeURIComponent(s.name)}/held/${encodeURIComponent(heldId)}`, { method: 'DELETE' });
        if (r.status === 409) { cancelling.delete(own); showToast('The message is being typed now, so it cannot be cancelled.', true); return; }
        if (!r.ok && r.status !== 404) { cancelling.delete(own); showToast('Could not cancel the queued message. It may have been sent already.', true); return; }
        const text = e ? e.text : '';
        if (e && e.composerId !== undefined) noteDone(sid, e.composerId); else { const m = queued.get(sid); if (m) { m.delete(heldId); if (!m.size) queued.delete(sid); } paint(); }
        setTimeout(() => cancelling.delete(own), 10000); // the refusal that follows is read as the person's own; the mark is not kept
        if (r.ok) { giveBack(sid, text); say('Cancelled. The message is back in the box.'); } // its text comes back
        else say('It was sent already, so it could not be cancelled.'); // a 404 says it is gone already (typed or refused): nothing comes back
    } catch (err) { cancelling.delete(own); showToast('Could not cancel the queued message. Check the connection.', true); }
}

/** A cancelled message's text goes back into the composer's box when the box is empty (otherwise it stays in the person's draft store, as a refused send's does). */
function giveBack(sid, text) {
    const input = document.getElementById('command-input');
    if (text && input && state.currentSession && state.currentSession.session_id === sid && !input.value) { input.value = text; input.dispatchEvent(new Event('input', { bubbles: true })); }
}

let lastShown = '';
/** Draw the open agent's queued lines in the region that is always in the page (#6611): one head line, a line per message, and what was last said. The region is never hidden (a region that
 *  appears with its words in the same moment is often not read): it is empty and zero-sized when nothing is queued. Lines are added and taken away one by one, not redrawn, so a reader hears
 *  what is new. Written only on a change (#5010's budget). */
export function paint() {
    const box = document.getElementById('composer-queued');
    if (!box) return;
    const sid = state.currentSession && state.currentSession.session_id;
    const items = sid && queued.get(sid) ? [...queued.get(sid).values()] : [];
    const sig = sid + '|' + said + '|' + items.map(i => `${i.id}:${i.sending ? 1 : 0}:${open.has(key(sid, i.id)) ? 1 : 0}:${i.text}`).join('\u0001');
    if (sig === lastShown) return;
    lastShown = sig;
    const kids = [...box.children];
    const head = kids.find(k => k.classList.contains('composer-queued-head'));
    const rows = new Map(kids.filter(k => k.classList.contains('composer-queued-item')).map(k => [k.dataset.key, k]));
    const noteEl = kids.find(k => k.classList.contains('composer-queued-note'));
    const waiting = items.some(i => !i.sending); // the head says when they send: not needed once every one is being typed
    if (waiting && !head) { const h = document.createElement('div'); h.className = 'composer-queued-head'; h.textContent = 'Queued. Sends after you answer the question above:'; box.prepend(h); }
    if (!waiting && head) head.remove();
    const want = new Set(items.map(i => String(i.id)));
    for (const [k, el] of rows) if (!want.has(k)) { el.remove(); open.delete(key(sid, k)); }
    let after = box.querySelector('.composer-queued-head');
    for (const i of items) {
        let el = rows.get(String(i.id));
        const clipped = [...wholeText(i.text)].length > WORDS, isOpen = clipped && open.has(key(sid, i.id));
        const text = `${i.sending ? 'Sending: ' : ''}“${isOpen ? wholeText(i.text) : firstWords(i.text)}”`;
        if (!el) {
            el = document.createElement('div'); el.className = 'composer-queued-item'; el.dataset.key = String(i.id);
            const t = document.createElement('span'); t.className = 'composer-queued-text'; el.append(t);
            (after ? after.after(el) : box.prepend(el));
        }
        const t = el.querySelector('.composer-queued-text');
        if (t.textContent !== text) t.textContent = text;
        // a line clipped to its first words opens on a tap or Enter/Space to the whole message, and closes the same way; a short one is plain text
        if (clipped && !t.hasAttribute('role')) {
            t.setAttribute('role', 'button'); t.tabIndex = 0;
            const flip = () => { const k = key(sid, i.id); if (open.has(k)) open.delete(k); else open.add(k); paint(); };
            t.addEventListener('click', flip);
            t.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); flip(); } });
        }
        if (clipped) t.setAttribute('aria-expanded', String(isOpen));
        else if (t.hasAttribute('role')) { t.removeAttribute('role'); t.removeAttribute('aria-expanded'); t.removeAttribute('tabindex'); }
        const cancel = el.querySelector('.composer-queued-cancel');
        if (i.sending && cancel) cancel.remove(); // once it is being typed there is nothing left to cancel
        if (!i.sending && !cancel) {
            const b = document.createElement('button'); b.type = 'button'; b.className = 'cni-link composer-queued-cancel'; b.textContent = 'Cancel';
            b.setAttribute('aria-label', `Cancel the queued message: ${firstWords(i.text)}`);
            b.addEventListener('click', () => cancelQueued(sid, i.id));
            el.append(b);
        }
        after = el;
    }
    if (said && !noteEl) { const n = document.createElement('div'); n.className = 'composer-queued-note'; n.textContent = said; box.append(n); }
    else if (said && noteEl && noteEl.textContent !== said) noteEl.textContent = said;
    else if (!said && noteEl) noteEl.remove();
}
