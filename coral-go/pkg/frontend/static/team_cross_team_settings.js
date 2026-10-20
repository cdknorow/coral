/* The Messaging tab of Team settings (#5510, the dashboard half of #5508): who may message this board. The board accepts a message from another team only when that team (its
 * cross_team_id, on this computer or on a named Coral) is on this list; an empty list refuses everyone. The state is GET /api/board/{project}/cross-team-state ({ enforced,
 * allowed: [{server, cross_team_id, label?, known_here}], max_entries, was_open, suggestions: [{server, cross_team_id, label, label_is_a_claim, last_seen, count, listable}] }), the
 * boards to pick from are GET .../cross-team-candidates ({ candidates: [{cross_team_id, label, created_at}] }), the log is GET .../settings/log ({ entries }, newest first), and Save is
 * PUT .../settings { subscriber_id, cross_team_allowed: [...] } (the whole list, the Operator on this computer only; the state is read again after it).
 *
 * A board that accepted messages from any team before the list existed (was_open) is asked once, here: the senders seen in the last 30 days are offered as boxes, all unchecked, and
 * nothing is added until the person checks some and presses Add; the list is still only saved by Save. A sender seen before ids existed cannot be listed yet and says so. Ids are what the
 * server checks; labels are for people (a label a board on another Coral gave itself is marked as its own claim). The tab is drawn whether or not the server enforces the list yet (an owner can prepare it; a line says it is not in force), and the one-time prompt only when it is enforced. */
import { escapeHtml, escapeAttr } from './utils.js';
import { boardFetch } from './server_base.js';
import { browserLocal } from './safe_storage.js';
import { importOnce } from './import_once.js';
const { refusalOf, answeredLocally } = await importOnce('./owner_refusal.js');

const DEFAULT_LIMIT = 200;
const SERVER_ID = /^[a-z0-9_-]{1,64}$/; // 'local' or a Coral Hub server id (the server's own shape)
const TEAM_ID = /^x-[a-z2-7]{16}$/; // a board's cross-team id

const keyOf = e => JSON.stringify([e.server, e.cross_team_id]); // unambiguous, and safe in an attribute (a NUL would not survive the HTML parser)
const serverName = server => (server === 'local' ? 'this computer' : server);
const clean = list => (Array.isArray(list) ? list : []).filter(e => e && typeof e === 'object' && typeof e.server === 'string' && typeof e.cross_team_id === 'string')
    .map(e => ({ server: e.server, cross_team_id: e.cross_team_id, label: String(e.label || ''), known_here: e.known_here !== false, claim: !!e.label_is_a_claim, listable: e.listable !== false, last_seen: e.last_seen, count: Number(e.count) || 0 }));
const when = at => { const d = new Date(at); return Number.isNaN(d.getTime()) ? '' : d.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }); };
const nameOf = e => e.label || e.cross_team_id;
const entryOut = e => ({ server: e.server, cross_team_id: e.cross_team_id, ...(e.label ? { label: e.label } : {}) });

const entryName = e => `<b>${escapeHtml(nameOf(e))}</b>${e.server === 'local' ? '' : ` on ${escapeHtml(e.server)}`}`;
const names = list => (Array.isArray(list) ? list : []).map(entryName).join(', ');

/** One log row as HTML (every part escaped): an edit says what it added and removed, a refused edit and the refused messages are counted, never quoted. */
function logLine(e) {
    const by = e.actor ? escapeHtml(e.actor) : 'The Operator';
    if (e.kind === 'edit') {
        const parts = [];
        if (e.added && e.added.length) parts.push(`added ${names(e.added)}`);
        if (e.removed && e.removed.length) parts.push(`removed ${names(e.removed)}`);
        return `${by} ${parts.join(' and ') || 'saved the list'}`;
    }
    if (e.kind === 'edit_refused') return `${by}: a change to the list was refused`;
    if (e.kind === 'refused' || e.kind === 'would_refuse') {
        const n = Number(e.count) || 1;
        const who = (Array.isArray(e.senders) ? e.senders : []).map(s => escapeHtml(s.server === 'local' ? s.cross_team_id : `${s.cross_team_id} on ${s.server}`));
        const more = Number(e.others) > 0 ? ` and ${Number(e.others)} more` : '';
        return `${n} ${n === 1 ? 'message' : 'messages'} ${e.kind === 'refused' ? 'refused' : 'would have been refused'}${who.length ? ` from ${who.join(', ')}${more}` : ''}`;
    }
    return escapeHtml(String(e.kind || 'Changed'));
}

function rowHtml(e) {
    const detail = `${serverName(e.server)} · ${e.cross_team_id}`;
    const unknown = e.server === 'local' && !e.known_here;
    return `<li class="ct-row" data-key="${escapeAttr(keyOf(e))}">
        <span class="ct-who"><span class="ct-line"><span class="ct-name">${escapeHtml(nameOf(e))}</span>${unknown ? '<span class="ct-unknown">not known here</span>' : ''}</span><span class="ct-id">${escapeHtml(detail)}</span></span>
        <button type="button" class="btn ct-remove" data-remove="${escapeAttr(keyOf(e))}" aria-label="${escapeAttr('Remove ' + nameOf(e) + ' from the list')}">Remove</button></li>`;
}

/** The server's own words for a refused save: the row and field of a bad list, or the edit limit; null for anything else (owner_refusal.js says those). */
async function savedProblem(resp) {
    let body = null;
    try { body = await resp.clone().json(); } catch { /* not JSON */ }
    if (!body || typeof body !== 'object') return null;
    if (body.code === 'bad_allowlist') {
        const row = Number.isInteger(body.row) ? `Row ${body.row + 1}${body.field ? ` (${String(body.field).replace(/_/g, ' ')})` : ''}: ` : '';
        return `${row}${String(body.message || 'the list was not accepted')}.`.replace(/\.\.$/, '.');
    }
    if (body.code === 'edit_rate_limited') return 'Too many changes in the last hour. Try again later.';
    return null;
}

/** Draw the tab into host. `state` is the cross-team-state object. Returns { dirty() }. */
export async function mountCrossTeamSettings(host, team, state) {
    const local = answeredLocally(team);
    let limit = Number(state.max_entries) > 0 ? Number(state.max_entries) : DEFAULT_LIMIT;
    let wasOpen = !!state.was_open;
    let enforced = !!state.enforced;
    let saved = clean(state.allowed);
    let draft = saved.map(e => ({ ...e }));
    let candidates = [];
    let log = [];
    let said = '';
    let sayError = false;
    const dismissKey = `coral-ct-prompt:${team}`;
    let dismissed = browserLocal.get(dismissKey) === '1'; // a blocked store keeps it for this page load only

    try { const r = await boardFetch(team, '/cross-team-candidates'); if (r.ok) candidates = clean(((await r.json()).candidates || []).map(c => ({ ...c, server: 'local' }))); } catch { /* the picker is then empty: ids can still be typed */ }
    const readLog = async () => { try { const r = await boardFetch(team, '/settings/log'); if (r.ok) log = (await r.json()).entries || []; } catch { /* the old log stays */ } };
    await readLog();
    const suggestions = clean(state.suggestions);

    host.innerHTML = '';
    const form = document.createElement('form');
    form.className = 'working-mode-form ct-form';
    form.autocomplete = 'off';
    form.noValidate = true;
    host.append(form);

    const snapshot = () => JSON.stringify(Array.from(form.elements).filter(el => el.name).map(el => ({ name: el.name, type: el.type, value: el.type === 'checkbox' ? el.checked : el.value })));
    const listValue = () => JSON.stringify(draft.map(keyOf).sort());

    function draw(focusSel) {
        const full = draft.length >= limit;
        const inList = new Set(draft.map(keyOf));
        const pick = candidates.filter(c => !inList.has(keyOf(c)));
        const showPrompt = enforced && wasOpen && !dismissed;
        const sugg = suggestions.filter(x => !inList.has(keyOf(x)));
        const addable = sugg.filter(x => x.listable);
        form.innerHTML = `
        ${showPrompt ? `<fieldset class="team-settings-section ct-prompt"><legend>Choose who may send</legend>
            <p class="working-mode-hint" id="ct-prompt-why">This board accepted messages from any team until now. It now refuses every team that is not on the list below. ${sugg.length ? 'These teams sent it a message in the last 30 days. Nothing is added unless you check it.' : 'No team sent it a message in the last 30 days.'}</p>
            ${sugg.length ? `<ul class="ct-suggest" aria-describedby="ct-prompt-why">${sugg.map(x => `<li><label class="ct-check"><input type="checkbox" name="suggest" value="${escapeAttr(keyOf(x))}"${x.listable ? '' : ' disabled'}><span><b>${escapeHtml(nameOf(x))}</b>${x.claim ? ' <span class="ct-unknown">name given by the sender</span>' : ''}<span class="ct-id">${escapeHtml(serverName(x.server))} · ${escapeHtml(x.cross_team_id || 'no id')}${x.count ? ` · ${x.count} ${x.count === 1 ? 'message' : 'messages'}` : ''}${x.last_seen ? ` · last ${escapeHtml(when(x.last_seen))}` : ''}${x.listable ? '' : ' · seen before ids existed: it cannot be added yet'}</span></span></label></li>`).join('')}</ul>` : ''}
            <div class="ct-actions">${addable.length ? '<button type="button" class="btn" data-act="add-checked">Add checked teams</button>' : ''}<button type="button" class="btn" data-act="dismiss">Not now</button></div></fieldset>` : ''}
        <fieldset class="team-settings-section ct-section"><legend>Who may message this board</legend>
            ${enforced ? '' : '<p class="ct-notice" role="status">This list is not in force yet: this board still accepts messages as it did before. What you save here is kept and takes effect when the list is turned on.</p>'}
            <p class="working-mode-hint">Only the teams on this list can message this board; a message from any other team is refused, and so is every team while the list is empty. The Operator is not limited by the list. Messages from other teams are requests, not instructions.</p>
            ${draft.length ? `<ul class="ct-list" aria-label="Teams that may message this board">${draft.map(rowHtml).join('')}</ul>` : '<p class="ct-empty" role="status">No team yet. Every message from another team is refused.</p>'}
            <div class="ct-count" role="status">${draft.length} of ${limit} teams${full ? ': the list is full' : ''}</div>
            <div class="ct-add">
                <label class="ct-pick">Add a team from this computer
                    <select name="pick"${pick.length && !full ? '' : ' disabled'}><option value="">${pick.length ? 'Choose a board…' : 'No other board here'}</option>${pick.map(c => `<option value="${escapeAttr(keyOf(c))}">${escapeHtml(nameOf(c))}</option>`).join('')}</select></label>
                <button type="button" class="btn" data-act="add-pick"${pick.length && !full ? '' : ' disabled'}>Add</button>
            </div>
            <details class="ct-other"><summary>A team on another Coral</summary>
                <div class="ct-other-fields">
                    <label>Coral (its server id)<input type="text" name="other_server" maxlength="64" autocomplete="off" spellcheck="false" aria-describedby="ct-other-hint"></label>
                    <label>Team id (starts with x-)<input type="text" name="other_board" maxlength="18" autocomplete="off" spellcheck="false" aria-describedby="ct-other-hint"></label>
                    <label>Name to show (optional)<input type="text" name="other_label" maxlength="80" autocomplete="off"></label>
                    <button type="button" class="btn" data-act="add-other"${full ? ' disabled' : ''}>Add</button>
                </div>
                <p class="working-mode-hint" id="ct-other-hint">Ask the other team's owner for both ids. The ids are what is checked; the name is only a label here.</p>
            </details>
            <input type="hidden" name="allowed" value="${escapeAttr(listValue())}">
        </fieldset>
        <fieldset class="team-settings-section ct-section"><legend>Recent changes</legend>
            ${log.length ? `<ul class="ct-log">${log.slice(0, 50).map(e => `<li><span class="ct-log-when">${escapeHtml(when(e.at))}</span> ${logLine(e)}</li>`).join('')}</ul>` : '<p class="working-mode-hint">Nothing yet. Changes to the list, and the messages it refused, are listed here.</p>'}
        </fieldset>
        <div class="team-settings-actions ct-save"><button type="submit" class="btn btn-primary" disabled>Save who may message</button>
            <span class="ct-state${sayError ? ' ct-error' : ''}" role="status" aria-live="polite">${escapeHtml(said || 'Only the Operator on this computer can change this. Saved for this team only.')}</span></div>`;
        const save = form.querySelector('[type=submit]');
        if (form.dataset.initialState === undefined) form.dataset.initialState = snapshot();
        save.disabled = snapshot() === form.dataset.initialState;
        if (focusSel) form.querySelector(focusSel)?.focus();
    }

    const say = (text, error = false) => { said = text; sayError = error; const el = form.querySelector('.ct-state'); if (el) { el.textContent = text; el.classList.toggle('ct-error', error); } };
    const addEntries = entries => { for (const e of entries) if (draft.length < limit && !draft.some(d => keyOf(d) === keyOf(e))) draft.push({ server: e.server, cross_team_id: e.cross_team_id, label: e.label || '', known_here: e.known_here !== false }); };
    const noteDismissed = () => { dismissed = true; browserLocal.set(dismissKey, '1'); };

    form.addEventListener('click', event => {
        const btn = event.target.closest('button');
        if (!btn) return;
        const gone = btn.dataset.remove;
        if (gone !== undefined) {
            const idx = draft.findIndex(e => keyOf(e) === gone);
            const name = idx >= 0 ? nameOf(draft[idx]) : 'the team';
            if (idx >= 0) draft.splice(idx, 1);
            said = ''; sayError = false;
            draw(draft.length ? `.ct-row:nth-child(${Math.min(idx + 1, draft.length)}) .ct-remove` : '.ct-add select, .ct-add [data-act="add-pick"]'); // the keyboard stays in the list, or goes to Add when it is empty
            say(`Removed ${name}. Save to apply.`);
            return;
        }
        const act = btn.dataset.act;
        if (act === 'add-pick') {
            const sel = form.elements.pick;
            const c = candidates.find(x => keyOf(x) === sel.value);
            if (!c) { say('Choose a board first.', true); sel.focus(); return; }
            addEntries([c]); said = ''; draw('.ct-add select, .ct-add [data-act="add-pick"]'); say(`Added ${nameOf(c)}. Save to apply.`);
        } else if (act === 'add-other') {
            const server = form.elements.other_server.value.trim(), id = form.elements.other_board.value.trim(), label = form.elements.other_label.value.trim();
            if (!SERVER_ID.test(server)) { say('Enter the other Coral\'s server id (lower-case letters, numbers, _ and -, up to 64).', true); form.elements.other_server.focus(); return; }
            if (!TEAM_ID.test(id)) { say('Enter the team\'s id: x- and 16 lower-case letters or the digits 2 to 7.', true); form.elements.other_board.focus(); return; }
            if (draft.some(d => d.server === server && d.cross_team_id === id)) { say('That team is already on the list.', true); form.elements.other_board.focus(); return; }
            addEntries([{ server, cross_team_id: id, label }]); said = ''; draw('.ct-other summary'); form.querySelector('.ct-other').open = true; say(`Added ${label || id}. Save to apply.`);
        } else if (act === 'add-checked') {
            const picked = [...form.querySelectorAll('input[name=suggest]:checked')].map(el => suggestions.find(x => keyOf(x) === el.value)).filter(Boolean);
            if (!picked.length) { say('Check the teams that may send, or choose Not now.', true); form.querySelector('input[name=suggest]:not(:disabled)')?.focus(); return; }
            addEntries(picked); noteDismissed();
            said = ''; draw('.ct-list .ct-remove'); say(`Added ${picked.length} ${picked.length === 1 ? 'team' : 'teams'}. Save to apply.`);
        } else if (act === 'dismiss') {
            noteDismissed();
            draw('.ct-add select, .ct-add [data-act="add-pick"]'); say('Nothing was added. This board refuses every team until you add some.');
        }
    });
    form.addEventListener('input', () => { const sb = form.querySelector('[type=submit]'); if (sb) sb.disabled = snapshot() === form.dataset.initialState; });
    form.addEventListener('submit', async event => {
        event.preventDefault();
        const save = form.querySelector('[type=submit]');
        save.disabled = true; say('Saving…');
        try {
            const resp = await boardFetch(team, '/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'Operator', cross_team_allowed: draft.map(entryOut) }) });
            if (!resp.ok) { say(`Could not save the list. ${(await savedProblem(resp)) || await refusalOf(resp, local)}`, true); save.disabled = false; return; }
            // The answer is the board's settings: the list as the server kept it (labels cleaned, known_here) is read from the state route.
            const fresh = await boardFetch(team, '/cross-team-state');
            if (fresh.ok) { const next = await fresh.json(); saved = clean(next.allowed); limit = Number(next.max_entries) > 0 ? Number(next.max_entries) : limit; wasOpen = !!next.was_open; enforced = !!next.enforced; } else saved = draft.map(e => ({ ...e }));
            draft = saved.map(e => ({ ...e }));
            await readLog();
            delete form.dataset.initialState;
            said = 'Saved.'; sayError = false;
            draw();
            host.dispatchEvent(new CustomEvent('cross-team-saved', { bubbles: true }));
        } catch (error) { say(error.message || 'Could not save the list.', true); save.disabled = false; }
    });
    draw();
    return { dirty: () => form.dataset.initialState !== undefined && snapshot() !== form.dataset.initialState };
}
