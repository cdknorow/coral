/* "Who can read this" (#5577, note #5576 v2): a small disclosure in the artifact viewer and in an Artifacts row's details, for the Operator on this computer
 * (or, through Coral Hub, the computer's owner). It reads GET /api/artifacts/{id}/readers: the owners that hold the artifact, their teams, the teams linked
 * through Coral Hub, and the server's plain note that this is a Coral control, not an operating-system boundary. The route is owner only: anyone else is
 * refused, and then nothing is drawn (readersAllowed answers false, once for the page). Every word from the server is set as text. */
import { serverUrl } from './server_base.js';

const HEX_RE = /^[a-f0-9]{64}$/;
const KINDS = { operator: 'the Operator on this computer', hub_user: 'Coral Hub user' };
const clean = (v, max = 120) => String(v == null ? '' : v).replace(/[\u0000-\u001f\u007f-\u009f​-‏‪-‮⁦-⁩]/g, '').trim().slice(0, max);
let _allowed; // undefined: not asked yet; a promise of true (owner) or false (refused or no answer), kept for the page

async function fetchReaders(hex, server) {
    const r = await fetch(serverUrl(`/api/artifacts/${hex}/readers`, server), { cache: 'no-store' });
    if (!r.ok) return null;
    const data = await r.json().catch(() => null);
    return data && typeof data === 'object' && Array.isArray(data.owners) ? data : null;
}

/** Whether the person may see who can read an artifact: asked once with this artifact, then remembered (a refusal is the same for every artifact). */
export function readersAllowed(hex, server) {
    if (!HEX_RE.test(String(hex || ''))) return Promise.resolve(false);
    return (_allowed ??= fetchReaders(hex, server).then(d => !!d, () => false));
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
    summary.textContent = 'Who can read this';
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
            if (!data) { body.textContent = 'Who can read this is not available here.'; loaded = false; return; }
            body.replaceChildren(...lines(data).map(t => { const p = document.createElement('p'); p.textContent = t; return p; }));
            if (typeof data.note === 'string' && data.note.trim()) { const n = document.createElement('p'); n.className = 'artifact-readers-note'; n.textContent = clean(data.note, 400); body.append(n); }
        } catch { body.textContent = 'Who can read this is not available here.'; loaded = false; }
    });
    return d;
}

export function _resetForTests() { _allowed = undefined; }
