/* The agent choice in New Agent's first step (#6199, Codex Test's first-run walk): a new person had to open More options > Agent to find Codex; the hidden default was Claude. The four agents are a radio group under the
 * folder, the ones this computer has first, the others marked "not installed". It is a second view of the form's own agent select (the select stays in More options, and the two follow each other), so everything that
 * reacts to that select (the missing-CLI line, the model list, the permission mode) still does. When nothing was ever chosen as the default and Claude is missing while another agent is installed, that one is selected. */
import { serverFetch } from './server_base.js';

const TYPES = [['claude', 'Claude'], ['codex', 'Codex'], ['agy', 'Antigravity'], ['pi', 'Pi']];
const ALIAS = { gemini: 'agy', antigravity: 'agy' };
const PROBE_MS = 1500; // a CLI that is slow to answer is "unknown": it keeps its place and is not marked

/** Whether the server has that agent's CLI: true, false, or null when it could not say. */
async function probe(server, type) {
    try {
        const r = await serverFetch(server, `/api/system/cli-check?type=${encodeURIComponent(type)}`);
        if (!r.ok) return null;
        const d = await r.json();
        return typeof d.found === 'boolean' ? d.found : null;
    } catch { return null; }
}

/** Fill `group` (the element) with the radios for the agent select of `form`. */
export async function buildAgentPick({ group, form, server, implicitDefault }) {
    const select = form && form.querySelector('.acf-agent-type');
    if (!group || !select) return;
    const token = (group._pickToken = (group._pickToken || 0) + 1);
    group.hidden = true;
    form.classList.remove('has-agent-pick');
    group.querySelectorAll('.launch-agent-opt').forEach(e => e.remove()); // the "Agent:" label stays
    const found = await Promise.all(TYPES.map(([t]) => Promise.race([probe(server, t), new Promise(r => setTimeout(() => r(null), PROBE_MS))])));
    if (group._pickToken !== token || !group.isConnected) return; // another New Agent opened meanwhile
    const order = TYPES.map((_, i) => i).sort((a, b) => (found[b] === true) - (found[a] === true) || a - b); // installed first, then the rest as they were
    const sync = () => { const v = ALIAS[select.value] || select.value; for (const r of group.querySelectorAll('input')) r.checked = r.value === v; };
    for (const i of order) {
        const [type, label] = TYPES[i];
        const wrap = document.createElement('label');
        wrap.className = 'launch-agent-opt';
        const input = document.createElement('input');
        input.type = 'radio';
        input.name = 'launch-agent-pick';
        input.value = type;
        input.addEventListener('change', () => { if (!input.checked) return; select.value = type; select.dispatchEvent(new Event('change', { bubbles: true })); });
        wrap.append(input, document.createTextNode(label));
        if (found[i] === false) { const miss = document.createElement('span'); miss.className = 'launch-agent-missing'; miss.textContent = ' not installed'; wrap.append(miss); }
        group.append(wrap);
    }
    select.addEventListener('change', sync);
    // nothing was ever chosen and the built-in default is not here: start on the first agent that is
    if (implicitDefault && (ALIAS[select.value] || select.value) === 'claude' && found[0] === false) {
        const first = order.find(i => found[i] === true);
        if (first !== undefined) { select.value = TYPES[first][0]; select.dispatchEvent(new Event('change', { bubbles: true })); }
    }
    sync();
    group.hidden = false;
    form.classList.add('has-agent-pick'); // the group is the one place that names the agent: the form's own select stays (hidden by css) and follows it (#6407)
}
