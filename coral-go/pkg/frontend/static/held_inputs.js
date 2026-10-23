/* Held messages the person should know about (#6473, the dashboard side of #6466): Coral holds an agent's automated input (board nudges, reminders) while a prompt or menu is open or the CLI's input box
 * is not on screen (held_inputs on the live row, #3578). A few seconds is the gate doing its job; past a minute Coral cannot see the input box and the messages are not getting through, so the row and the
 * agent view say so, with the terminal one click away. Nothing is delivered from here: there is no deliver-now button. The row has the count and no time, so the minute is counted from when this page first saw
 * the agent holding (a `held_since` on the row, when the server sends one, is the start instead). */
export const HELD_AFTER_MS = 60 * 1000;
const firstHeld = new Map(); // session_id -> ms when this page first saw it holding

const afterMs = () => { const o = Number(typeof window !== 'undefined' && window.__coralHeldAfterMs); return Number.isFinite(o) && o >= 0 && typeof window.__coralHeldAfterMs !== 'undefined' ? o : HELD_AFTER_MS; };

/** held_inputs as a safe non-negative integer. */
export function heldCount(row) {
    const n = Number(row && row.held_inputs);
    return Number.isFinite(n) && n > 0 ? Math.min(Math.floor(n), 999) : 0;
}

/** { n, text } when the agent has held input for more than a minute, else null. */
export function heldNotice(row, now = Date.now()) {
    const id = row && row.session_id;
    const n = heldCount(row);
    if (!id || n <= 0 || row.sleeping) { if (id) firstHeld.delete(id); return null; }
    let since = Date.parse(row.held_since || '');
    if (!Number.isFinite(since)) { if (!firstHeld.has(id)) firstHeld.set(id, now); since = firstHeld.get(id); }
    if (now - since <= afterMs()) return null;
    return { n, text: `${n} message${n === 1 ? '' : 's'} waiting: Coral can't see the input box` };
}

/** The agent view's line: the words and a Show terminal button, while the open agent holds; hidden otherwise. */
export function syncHeldNotice(row) {
    const el = document.getElementById('composer-held-notice');
    if (!el) return;
    const note = heldNotice(row);
    if (el.hidden === !!note) el.hidden = !note; // written only on a change: a feed message that changes nothing drawn writes nothing to the page (#5010's budget, perf_budgets check 7)
    const t = document.getElementById('composer-held-text');
    if (t && t.textContent !== (note ? note.text : '')) t.textContent = note ? note.text : '';
}
