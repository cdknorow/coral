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
export function noteDone(sid, id) {
    const k = key(sid, id);
    sent.delete(k);
    const heldId = byComposer.has(k) ? byComposer.get(k) : id;
    byComposer.delete(k);
    const m = queued.get(sid);
    if (m && m.delete(heldId)) { if (!m.size) queued.delete(sid); paint(); }
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
        if (r.ok) giveBack(sid, text); // cancelled: its text comes back; a 404 says it is gone already (typed or refused), so nothing comes back
    } catch (err) { cancelling.delete(own); showToast('Could not cancel the queued message. Check the connection.', true); }
}

/** A cancelled message's text goes back into the composer's box when the box is empty (otherwise it stays in the person's draft store, as a refused send's does). */
function giveBack(sid, text) {
    const input = document.getElementById('command-input');
    if (text && input && state.currentSession && state.currentSession.session_id === sid && !input.value) { input.value = text; input.dispatchEvent(new Event('input', { bubbles: true })); }
}

let lastShown = '';
/** Draw the open agent's queued lines (written only when they change: a feed message that changes nothing drawn writes nothing to the page, #5010). */
export function paint() {
    const box = document.getElementById('composer-queued');
    if (!box) return;
    const sid = state.currentSession && state.currentSession.session_id;
    const items = sid && queued.get(sid) ? [...queued.get(sid).values()] : [];
    const sig = sid + '|' + items.map(i => `${i.id}:${i.sending ? 1 : 0}:${i.text}`).join('\u0001');
    if (sig === lastShown) return;
    lastShown = sig;
    if (box.hidden === !!items.length) box.hidden = !items.length;
    const many = items.length > 1;
    const row = (i, text) => {
        const li = document.createElement('div'); li.className = 'composer-queued-item';
        const t = document.createElement('span'); t.className = 'composer-queued-text'; t.textContent = i.sending ? text.replace(/^Queued: /, 'Sending: ').replace(/\. Sends after.*$/, '.') : text;
        li.append(t);
        if (!i.sending) { // once it is being typed there is nothing left to cancel
            const b = document.createElement('button'); b.type = 'button'; b.className = 'cni-link composer-queued-cancel'; b.textContent = 'Cancel';
            b.setAttribute('aria-label', `Cancel the queued message: ${firstWords(i.text)}`);
            b.addEventListener('click', () => cancelQueued(sid, i.id));
            li.append(b);
        }
        return li;
    };
    // One message says it all on its line; several say when they send once, above them.
    const out = many ? [Object.assign(document.createElement('div'), { className: 'composer-queued-head', textContent: 'Queued. Sends after you answer the question above:' }), ...items.map(i => row(i, `“${firstWords(i.text)}”`))]
        : items.map(i => row(i, `Queued: “${firstWords(i.text)}”. Sends after you answer the question above.`));
    box.replaceChildren(...out);
}
