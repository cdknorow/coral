/* Send to agent on an artifact comment (#6357, #6456): the owner picks one comment, then the agents on its board to ask, 1 to 10, and Coral posts one board message to each, from the owner, with the comment quoted as
 * untrusted text. This draws the picker that opens inside the comment's card (in the comments panel's own style) and what came of the ask; artifact_comments.js owns the state and the requests. Everything shown is text.
 * Local only: it is never offered through Coral Hub. An agent that is asleep or not running cannot be picked (the server refuses it too); one already asked into this thread says so; the agent's reply is a reply in the thread. */

export const MAX_AGENTS = 10;

// What the server's answer for one agent says to the person.
const RESULT_WORDS = {
    sent: 'Asked',
    already_asked: 'Already asked',
    asleep: 'Asleep: wake it first',
    unavailable: 'Not running',
    not_on_board: 'Not on this board',
    paused: 'The board is paused',
    failed: 'Could not be asked: try again',
};
export const resultWords = (r) => RESULT_WORDS[r.state] || r.reason || 'Not asked';

const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };

/** Whether an agent of the board's status list can be asked now, and why not: asleep and not running cannot (the server refuses them as well). */
export function askableState(a) {
    const av = String(a.availability || '');
    if (av === 'sleeping') return { can: false, why: 'Asleep: wake it first' };
    if (av === 'offline' || av === 'unavailable') return { can: false, why: 'Not running' };
    return { can: true, why: '' };
}

/** The agents of the board that can be named in a send: the status list's agents that have a role (the name the board knows them by) and are not terminals; ordered as listed. */
export function pickableAgents(list) {
    const out = [];
    for (const a of list || []) {
        const role = String(a.role || '').trim();
        if (!role || a.agent_type === 'terminal') continue;
        out.push({ role, name: String(a.name || role), type: String(a.agent_type || ''), ...askableState(a) });
    }
    return out;
}

/**
 * The block under a comment's actions. `x`: { board, agents: array | null (loading) | 'error', picked: Set of roles, asked: Map role(lowercase) -> text, results: array | null, busy, error, onToggle(role), onCancel(), onSend() }.
 * Returns the element; the caller puts it in the card and keeps focus where it was.
 */
export function buildSendBlock(x) {
    const box = el('div', 'ac-ask'); box.setAttribute('role', 'group');
    const title = el('p', 'ac-ask-title', `Send this comment to agents on ${x.board}`); title.id = `ac-ask-title-${x.id}`;
    box.setAttribute('aria-labelledby', title.id);
    box.append(title);
    box.append(el('p', 'ac-ask-note', 'Coral posts one message to each agent you pick, from you, with this comment quoted as text from someone else. Agents answer in this thread only.'));
    if (x.agents === null) { box.append(el('p', 'ac-ask-note', 'Looking for the agents on this board…')); }
    else if (x.agents === 'error') { box.append(el('p', 'ac-error', 'The agents of this board could not be read.')); }
    else if (!x.agents.length) { box.append(el('p', 'ac-ask-note', 'No agent on this board can be asked.')); }
    else {
        const list = el('ul', 'ac-ask-list');
        const full = x.picked.size >= MAX_AGENTS;
        for (const a of x.agents) {
            const li = el('li', 'ac-ask-agent');
            const label = el('label', 'ac-ask-label');
            const cb = document.createElement('input'); cb.type = 'checkbox'; cb.value = a.role;
            const asked = x.asked.get(a.role.toLowerCase());
            cb.checked = x.picked.has(a.role);
            cb.disabled = !a.can || !!asked || !!x.results || x.busy || (full && !cb.checked);
            cb.addEventListener('change', () => x.onToggle(a.role));
            label.append(cb, el('span', 'ac-ask-name', a.role));
            if (a.name && a.name.toLowerCase() !== a.role.toLowerCase()) label.append(el('span', 'ac-ask-sub', a.name));
            li.append(label);
            const res = x.results && x.results.find(r => r.agent.toLowerCase() === a.role.toLowerCase());
            const why = res ? resultWords(res) : (asked || (!a.can ? a.why : ''));
            if (why) { const s = el('span', 'ac-ask-state' + (res && res.state === 'sent' ? ' ac-ask-sent' : ''), why); li.append(s); li.dataset.state = res ? res.state : (asked ? 'asked' : 'unavailable'); }
            list.append(li);
        }
        box.append(list);
        if (full && !x.results) box.append(el('p', 'ac-ask-note', `Up to ${MAX_AGENTS} agents at a time.`));
    }
    const err = el('p', 'ac-error', x.error || ''); err.setAttribute('role', 'alert'); err.hidden = !x.error; box.append(err);
    const row = el('div', 'ac-composer-row');
    const cancel = el('button', 'btn ac-cancel', x.results ? 'Close' : 'Cancel'); cancel.type = 'button'; cancel.addEventListener('click', x.onCancel);
    row.append(cancel);
    if (!x.results) {
        const n = x.picked.size;
        const send = el('button', 'btn btn-primary ac-ask-go', n > 1 ? `Send to ${n} agents` : 'Send to agent'); send.type = 'button';
        send.disabled = n === 0 || x.busy;
        send.addEventListener('click', x.onSend);
        row.append(send);
    }
    box.append(row);
    return box;
}
