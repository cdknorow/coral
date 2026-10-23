/* A person's chat message held behind the agent's question (#6611, the dashboard half of #6610): while the agent waits on a permission or question box, the server holds the message and types it once no box
 * is left. The composer says so under the box, one line per held message: "Queued: <its first words>. Sends after you answer the question above." with Cancel. The line goes when the message is typed (its
 * ack), refused (its reason shows as the composer's error) or cancelled (its text comes back into the box). The list is this window's own sends that the server answered "held" and, when the live row
 * carries `held_messages` (the server's list, so a reload or a second window shows the same), that list. Nothing here types or sends: Cancel asks the server to drop the message. */
import { state } from './state.js';
import { sessionServer, serverFetch } from './server_base.js';
import { showToast } from './utils.js';

const sent = new Map();     // `${sid}|${id}` -> text, a send over the socket that has not been answered yet
const queued = new Map();   // sid -> Map(id -> { id, text })
const cancelling = new Set(); // `${sid}|${id}` whose cancel is under way: its refusal is the person's own doing, not an error
const key = (sid, id) => `${sid}|${id}`;
const WORDS = 60;

/** First words of a message, for the line and for Cancel's name. */
export function firstWords(text) {
    const t = String(text || '').replace(/\s+/g, ' ').trim();
    const c = [...t];
    return c.length > WORDS ? c.slice(0, WORDS).join('') + '…' : t;
}

/** A send over the socket (frame `id`): remembered until the server answers it, so a "held" answer knows what was held. */
export function rememberSend(sid, id, text) { if (sid) sent.set(key(sid, id), String(text || '')); }

function put(sid, id, text) {
    if (!sid || id === undefined || id === null) return;
    if (!queued.has(sid)) queued.set(sid, new Map());
    queued.get(sid).set(id, { id, text: String(text || '') });
}

/** The server answered the send "held" (WebSocket frame `id`, or an HTTP send that returned `held_id`). */
export function noteHeld(sid, id, text) {
    const k = key(sid, id);
    put(sid, id, text !== undefined ? text : sent.get(k));
    paint();
}

/** The message was typed, refused or cancelled: it is not held any more. */
export function noteDone(sid, id) {
    sent.delete(key(sid, id));
    const m = queued.get(sid);
    if (m && m.delete(id)) { if (!m.size) queued.delete(sid); paint(); }
}

/** Was this message's cancel the person's own (so its refusal is not an error)? */
export function wasCancelled(sid, id) { return cancelling.delete(key(sid, id)); }

/** The live row's own list (`held_messages: [{id, text}]`) is the truth for that agent when the server sends it. */
export function syncQueuedFromRow(row) {
    if (!row || !row.session_id || !Array.isArray(row.held_messages)) return;
    const next = new Map();
    for (const h of row.held_messages) if (h && typeof h.text === 'string' && h.text) next.set(h.id, { id: h.id, text: h.text });
    const had = queued.get(row.session_id);
    const same = had && had.size === next.size && [...next].every(([id, v]) => had.get(id) && had.get(id).text === v.text);
    if (same || (!had && !next.size)) return;
    if (next.size) queued.set(row.session_id, next); else queued.delete(row.session_id);
    paint();
}

function sessionOf(sid) { return (state.liveSessions || []).find(s => s && s.session_id === sid) || (state.currentSession && state.currentSession.session_id === sid ? state.currentSession : null); }

/** Ask the server to drop one held message; its text comes back into the box when the refusal arrives (terminal_gone.js / controls.js put it back). */
export async function cancelQueued(sid, id) {
    const s = sessionOf(sid);
    if (!s) return;
    const k = key(sid, id);
    cancelling.add(k);
    try {
        const r = await serverFetch(sessionServer(s), `/api/sessions/live/${encodeURIComponent(s.name)}/held/${encodeURIComponent(id)}`, { method: 'DELETE' });
        if (!r.ok && r.status !== 404) { cancelling.delete(k); showToast('Could not cancel the queued message. It may have been sent already.', true); return; }
        const text = (queued.get(sid) && queued.get(sid).get(id) || {}).text || sent.get(k) || '';
        noteDone(sid, id);
        setTimeout(() => cancelling.delete(k), 10000); // the refusal that follows is read as the person's own; the mark is not kept
        giveBack(sid, text);
    } catch (e) { cancelling.delete(k); showToast('Could not cancel the queued message. Check the connection.', true); }
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
    const sig = sid + '|' + items.map(i => `${i.id}:${i.text}`).join('\u0001');
    if (sig === lastShown) return;
    lastShown = sig;
    if (box.hidden === !!items.length) box.hidden = !items.length;
    box.replaceChildren(...items.map(i => {
        const li = document.createElement('div'); li.className = 'composer-queued-item';
        const t = document.createElement('span'); t.className = 'composer-queued-text';
        t.textContent = `Queued: “${firstWords(i.text)}”. Sends after you answer the question above.`;
        const b = document.createElement('button'); b.type = 'button'; b.className = 'cni-link composer-queued-cancel'; b.textContent = 'Cancel';
        b.setAttribute('aria-label', `Cancel the queued message: ${firstWords(i.text)}`);
        b.addEventListener('click', () => cancelQueued(sid, i.id));
        li.append(t, b);
        return li;
    }));
}
