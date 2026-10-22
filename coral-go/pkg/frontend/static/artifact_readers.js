/* "Who can read this" (#5577, note #5576 v2): a small disclosure in the artifact viewer and in an Artifacts row's details, for the Operator on this computer
 * (or, through Coral Hub, the computer's owner). It reads GET /api/artifacts/{id}/readers: the owners that hold the artifact, their teams, the teams linked
 * through Coral Hub, and the server's plain note that this is a Coral control, not an operating-system boundary. The route is owner only: anyone else is
 * refused, and then nothing is drawn (readersAllowed answers false, once for the page). Every word from the server is set as text. */
import { serverUrl } from './server_base.js';

const HEX_RE = /^[a-f0-9]{64}$/;
const KINDS = { operator: 'the Operator on this computer', hub_user: 'Coral Hub user' };
const clean = (v, max = 120) => String(v == null ? '' : v).replace(/[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u202a-\u202e\u2066-\u2069]/g, '').trim().slice(0, max);
let _allowed; // undefined: not asked yet; a promise of true (owner) or false (refused or no answer), kept for the page

async function fetchReaders(hex, server) {
    const r = await fetch(serverUrl(`/api/artifacts/${hex}/readers`, server), { cache: 'no-store' });
    if (!r.ok) return null;
    const data = await r.json().catch(() => null);
    return data && typeof data === 'object' && Array.isArray(data.owners) ? data : null;
}

/** true (may see it), false (refused: the route is owner only), or null (no answer: a network error, a 404 for this artifact, a server fault). */
async function probe(hex, server) {
    try {
        const r = await fetch(serverUrl(`/api/artifacts/${hex}/readers`, server), { cache: 'no-store' });
        if (r.ok) return true;
        return r.status === 401 || r.status === 403 ? false : null;
    } catch { return null; }
}

/** Whether the person may see who can read an artifact: asked with this artifact, and a definite answer (yes, or refused) is remembered for the page. No answer is not remembered, so one failure does not hide it for every artifact until a reload. */
export function readersAllowed(hex, server) {
    if (!HEX_RE.test(String(hex || ''))) return Promise.resolve(false);
    if (_allowed !== undefined) return _allowed;
    const asked = probe(hex, server).then(v => { if (v === null) { if (_allowed === asked) _allowed = undefined; return false; } return v; });
    return (_allowed = asked);
}

function lines(data) {
    const out = [];
    if (typeof data.machine_owner === 'string' && data.machine_owner.trim()) out.push(`On this computer, the Operator ${clean(data.machine_owner)}.`);
    for (const o of data.owners.slice(0, 50)) {
        if (!o || typeof o !== 'object') continue;
        const who = KINDS[o.kind] ? `${KINDS[o.kind]}${o.kind === 'hub_user' ? ' ' + clean(o.id) : ''}` : clean(o.id) || 'an owner';
        const teams = (Array.isArray(o.teams) ? o.teams : []).slice(0, 50).map(t => clean(t && (t.label || t.id))).filter(Boolean);
        const n = Number(o.records) || 0;
        out.push(`Held by ${who}${n ? ` (${n} ${n === 1 ? 'record' : 'records'})` : ''}${teams.length ? `, on ${teams.join(', ')}` : ''}. ${clean(o.reason) ? clean(o.reason).replace(/^./, c => c.toUpperCase()) + '.' : ''}`.trim());
    }
    const linked = (Array.isArray(data.linked_teams) ? data.linked_teams : []).slice(0, 50).map(t => clean(t && (t.label || t.id))).filter(Boolean);
    if (linked.length) out.push(`Teams linked through Coral Hub that can read it: ${linked.join(', ')}.`);
    return out;
}

/** The disclosure for an artifact: closed to begin with, loaded when it is opened. */
export function readersDisclosure(hex, server) {
    const d = document.createElement('details');
    d.className = 'artifact-readers';
    const summary = document.createElement('summary');
    const chevron = document.createElement('span'); // turns when open, as More options does
    chevron.className = 'material-icons artifact-readers-chevron'; chevron.setAttribute('aria-hidden', 'true'); chevron.textContent = 'expand_more';
    summary.append(chevron, 'Who can read this');
    const body = document.createElement('div');
    body.className = 'artifact-readers-body';
    body.setAttribute('role', 'status');
    d.append(summary, body);
    let loaded = false;
    d.addEventListener('toggle', async () => {
        if (!d.open || loaded) return;
        loaded = true;
        body.textContent = 'Loading…';
        try {
            const data = await fetchReaders(hex, server);
            if (!data) { d.remove(); return; } // the machine cannot say now: draw nothing, not a line about it (#5991)
            body.replaceChildren(...lines(data).map(t => { const p = document.createElement('p'); p.textContent = t; return p; }));
            if (typeof data.note === 'string' && data.note.trim()) { const n = document.createElement('p'); n.className = 'artifact-readers-note'; n.textContent = clean(data.note, 400); body.append(n); }
        } catch { d.remove(); }
    });
    return d;
}

export function _resetForTests() { _allowed = undefined; }
