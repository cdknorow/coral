/* Adding one model to the operator's custom models from where the person is choosing a model (the model chip's menu and the launch dialog's picker, #5938): the same catalog write as Settings >
 * Agents > Custom models (GET and PUT /api/custom-models, Operator only; the PUT replaces the whole list), so the id rule, the limit and the refusals are one thing. It reads the whole list, adds
 * ONE row for the agent type's provider (thinking levels none: Settings has them) and writes the whole list back, and never writes after a read that failed. */
import { serverFetch } from './server_base.js';
import { importOnce } from './import_once.js';

const ROUTE = '/api/custom-models';
const FAMILIES = { claude: 'claude', codex: 'codex', agy: 'agy', antigravity: 'agy', gemini: 'agy', pi: 'pi' };

// #5996: through Coral Hub a non-owner's read of the list is refused (403): one read a minute per server tells it; only a refusal says no.
const _canAdd = new Map();
export const peekCanAdd = server => { const e = _canAdd.get(server || ''); return e && Date.now() - e.at < 6e4 ? e.ok : undefined; };
/** Once the machine says no, `onNo()` runs (once per ask); with an answer kept it does nothing. */
export function probeCanAdd(server, onNo) {
    if (peekCanAdd(server) !== undefined) return;
    serverFetch(server, ROUTE).then(r => r.status !== 401 && r.status !== 403, () => true).then(ok => { _canAdd.set(server || '', { at: Date.now(), ok }); if (!ok) onNo(); });
}

/** The provider whose custom models an agent type's CLI reads (the server's agentmodels families), '' for a type that has none. */
export function familyOf(agentType) { return FAMILIES[agentType] || ''; }

/** Add the model. `known` is the ids the picker already lists. Resolves { ok: true } or { ok: false, error } in words; nothing is written for a refusal. */
export async function addCustomModel({ server, agentType, id, name, known = [] }) {
    const family = familyOf(agentType);
    if (!family) return { ok: false, error: 'This agent type has no custom models.' };
    const { MODEL_ID_RULE, MODEL_ID_MISSING, MODEL_ID_INVALID } = await importOnce('./custom_models.js');
    id = String(id || '').trim(); name = String(name || '').trim();
    if (!id) return { ok: false, error: MODEL_ID_MISSING };
    if (!MODEL_ID_RULE.test(id)) return { ok: false, error: MODEL_ID_INVALID };
    if (known.includes(id)) return { ok: false, error: `${id} is already in the list.` };
    const refused = r => r.status === 401 || r.status === 403;
    const operatorOnly = { ok: false, error: 'Only the Operator can add models.' };
    try {
        const got = await serverFetch(server, ROUTE);
        if (refused(got)) return operatorOnly;
        const data = got.ok ? await got.json().catch(() => null) : null;
        if (!data || !Array.isArray(data.models)) return { ok: false, error: 'Could not read the custom models, so nothing was changed. Try again.' };
        if (data.models.some(m => m.family === family && m.value === id)) return { ok: false, error: `${id} is already in the list.` };
        const max = (data.limits && data.limits.max_per_family) || 50;
        if (data.models.filter(m => m.family === family).length >= max) return { ok: false, error: `At most ${max} custom models per provider. Remove one in Settings > Agents first.` };
        const put = await serverFetch(server, ROUTE, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ models: [...data.models, { family, value: id, display_name: name, efforts: [] }] }) });
        if (refused(put)) return operatorOnly;
        if (!put.ok) { const d = await put.json().catch(() => ({})); return { ok: false, error: (d && (d.message || d.error)) || 'The model was not added.' }; }
    } catch { return { ok: false, error: 'Could not reach Coral to add the model.' }; }
    return { ok: true };
}

/** The small form (model id, optional display name, Add, Cancel) as DOM, for a place that has room for it inline (the launch dialog's picker). `submit(id, name)` resolves '' when the model was added or an
 *  error in words; `cancel()` closes it. Escape cancels; an error is an alert and keeps what was typed; the form says it is busy while the write runs. Same classes as the chip menu's form. */
export function addModelForm({ prefix, submit, cancel }) {
    const mk = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
    const form = mk('form', 'model-chip-add-form'); form.noValidate = true; form.setAttribute('role', 'group'); form.setAttribute('aria-label', 'Add a model');
    const field = (label, cls, name, max) => { const l = mk('label', 'model-chip-add-field'); l.append(mk('span', null, label)); const i = mk('input', cls); i.type = 'text'; i.name = name; i.autocomplete = 'off'; i.maxLength = max; if (name === 'id') i.spellcheck = false; l.append(i); return { l, i }; };
    const idF = field('Model id', 'model-chip-add-id', 'id', 128), nameF = field('Display name (optional)', 'model-chip-add-name', 'name', 80);
    const help = mk('p', 'model-chip-add-help', 'The id the CLI takes after --model. Thinking levels can be added in Settings > Agents.'); help.id = `${prefix}-help`;
    idF.i.setAttribute('aria-describedby', help.id);
    const err = mk('p', 'model-chip-add-err'); err.id = `${prefix}-err`; err.hidden = true;
    const add = mk('button', 'btn btn-small btn-primary', 'Add'); add.type = 'submit';
    const no = mk('button', 'btn btn-small', 'Cancel'); no.type = 'button';
    const actions = mk('div', 'model-chip-add-actions'); actions.append(add, no);
    form.append(idF.l, nameF.l, help, err, actions);
    const busy = on => { form.setAttribute('aria-busy', String(on)); for (const c of [idF.i, nameF.i, add, no]) c.disabled = on; add.textContent = on ? 'Adding…' : 'Add'; };
    form.addEventListener('submit', async e => {
        e.preventDefault(); e.stopPropagation();
        err.hidden = true; err.removeAttribute('role'); idF.i.removeAttribute('aria-invalid'); busy(true);
        const msg = await submit(idF.i.value, nameF.i.value);
        if (!msg) return; // added: the caller closes the form
        busy(false); err.textContent = msg; err.hidden = false; err.setAttribute('role', 'alert'); idF.i.setAttribute('aria-invalid', 'true'); idF.i.setAttribute('aria-describedby', `${help.id} ${err.id}`); idF.i.focus();
    });
    no.addEventListener('click', () => cancel());
    form.addEventListener('keydown', e => { if (e.key === 'Escape' && !add.disabled) { e.preventDefault(); e.stopPropagation(); cancel(); } });
    return { form, focus: () => idF.i.focus() };
}
