/* Settings > Agents > Custom models (#5480): the model ids a person adds by hand for a provider, and the thinking levels each accepts. They are kept on this computer (GET and PUT /api/custom-models,
 * Operator only: the PUT replaces the whole list in one step) and the server adds them to the one catalog every model picker reads (GET /api/agent-models), so launch, team, workflow steps, the defaults and a running agent's model switcher all
 * show them. The form edits a draft and Apply saves it with the rest of Settings (applyCustomModels), the same way the update switches are saved. The server checks every id and level again; the
 * id rule here only lets the form say what is wrong before the save. */
import { requestJson } from './api.js';

const ROUTE = '/api/custom-models';
const PROVIDERS = [['claude', 'Claude'], ['codex', 'Codex'], ['agy', 'Antigravity'], ['pi', 'Pi']];
// the same rule as the server's agentmodels.ValidModelValue: one argument for --model, never flag-shaped, no spaces or control characters
const VALID_ID = /^[A-Za-z0-9][A-Za-z0-9._:/@[\]+-]{0,127}$/;
const MAX_NAME = 80;
/** The id rule and its words, for a place that adds one model without the Settings form (the model chip's "Add a model…", #5938). */
export const MODEL_ID_RULE = VALID_ID;
export const MODEL_ID_MISSING = 'Enter the model id the CLI takes after --model.';
export const MODEL_ID_INVALID = 'Use letters, numbers and . _ : / @ [ ] + - only, starting with a letter or number, up to 128 characters.';

let saved = {};           // what the server has, grouped by family: { family: [{ value, display_name, efforts }] }
let draft = {};           // what the form shows
let limits = { max_per_family: 50, efforts: {} };
let editing = null;       // { provider, index } while a form is open (index -1: a new model)
let host = null;
let loaded = false;

const mk = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
const clone = (o) => JSON.parse(JSON.stringify(o));
const label = (p) => (PROVIDERS.find(([k]) => k === p) || [p, p])[1];
const rows = (p) => (Array.isArray(draft[p]) ? draft[p] : (draft[p] = []));
const levelsOf = (p) => (Array.isArray(limits.efforts && limits.efforts[p]) ? limits.efforts[p] : []);

/** The reason an id is not accepted for a provider, or '' (the row being edited is not a duplicate of itself). */
export function idProblem(provider, value, skipIndex = -1) {
    if (!value) return 'Enter the model id the CLI takes after --model.';
    if (!VALID_ID.test(value)) return 'Use letters, numbers and . _ : / @ [ ] + - only, starting with a letter or number, up to 128 characters.';
    if (rows(provider).some((m, i) => i !== skipIndex && m.value === value)) return `${value} is already in the list for ${label(provider)}.`;
    return '';
}

/** The whole draft in the shape the server takes: one list of rows, each with its family. */
export function customModelsPayload() {
    const out = [];
    for (const [p] of PROVIDERS) for (const m of rows(p)) out.push({ family: p, value: m.value, display_name: m.display_name || '', efforts: [...(m.efforts || [])] });
    return { models: out };
}

/** The server's list of rows grouped by family. */
function group_by_family(list) {
    const out = {};
    for (const m of Array.isArray(list) ? list : []) if (m && PROVIDERS.some(([k]) => k === m.family)) (out[m.family] ||= []).push({ value: String(m.value || ''), display_name: String(m.display_name || ''), efforts: Array.isArray(m.efforts) ? m.efforts.map(String) : [] });
    return out;
}

/** Whether the draft differs from what the server has. */
export function customModelsDirty() { return loaded && JSON.stringify(customModelsPayload()) !== JSON.stringify(canonical(saved)); }

function canonical(models) { const d = draft; draft = models; const p = customModelsPayload(); draft = d; return p; }

function status() {
    const s = host && host.querySelector('.cm-status');
    if (s) s.textContent = customModelsDirty() ? 'Unsaved changes. Apply to save.' : '';
}

function chips(m) {
    const wrap = mk('span', 'cm-levels');
    if (m.efforts && m.efforts.length) for (const e of m.efforts) wrap.append(mk('span', 'cm-chip', e));
    return wrap;
}

function totalRows() { return PROVIDERS.reduce((n, [p]) => n + rows(p).length, 0); }
const full = (p) => rows(p).length >= (limits.max_per_family || 50);

/** The one form: a new model picks its provider here (the first with room), an edited one keeps its own. */
function form(startProvider, index) {
    let provider = startProvider;
    const cur = index >= 0 ? rows(provider)[index] : { value: '', display_name: '', efforts: [] };
    const f = mk('form', 'cm-form'); f.noValidate = true;
    let pick = null;
    if (index < 0) {
        const pl = mk('label', 'cm-field'); pl.append(mk('span', null, 'Provider'));
        pick = mk('select', 'settings-input'); pick.id = 'cm-provider';
        for (const [k, name] of PROVIDERS) { const o = mk('option', null, name); o.value = k; o.disabled = full(k) && k !== provider; pick.append(o); }
        pick.value = provider; pl.append(pick); f.append(pl);
    } else f.append(mk('p', 'cm-editing', `${label(provider)} model`));
    const idLabel = mk('label', 'cm-field'); idLabel.append(mk('span', null, 'Model id'));
    const id = mk('input', 'settings-input settings-mono-input'); id.type = 'text'; id.value = cur.value; id.autocomplete = 'off'; id.spellcheck = false; id.maxLength = 128; id.id = 'cm-id';
    idLabel.append(id);
    const nameLabel = mk('label', 'cm-field'); nameLabel.append(mk('span', null, 'Display name (optional)'));
    const name = mk('input', 'settings-input'); name.type = 'text'; name.value = cur.display_name || ''; name.autocomplete = 'off'; name.maxLength = MAX_NAME; name.id = 'cm-name';
    nameLabel.append(name);
    const err = mk('p', 'cm-error'); err.setAttribute('role', 'alert'); err.hidden = true; err.id = 'cm-err';
    id.setAttribute('aria-describedby', err.id);
    f.append(idLabel, nameLabel);
    // the thinking levels a provider takes: drawn again when the provider changes
    let boxes = [];
    const levelsHost = mk('div', 'cm-levels-host');
    const drawLevels = () => {
        levelsHost.replaceChildren(); boxes = [];
        const lv = levelsOf(provider);
        if (lv.length) {
            const fs = mk('fieldset', 'cm-levels-field'); fs.append(mk('legend', null, 'Thinking levels this model accepts'));
            const had = index >= 0 ? (cur.efforts || []) : [];
            for (const l of lv) {
                const lab = mk('label', 'cm-level'); const cb = document.createElement('input'); cb.type = 'checkbox'; cb.value = l; cb.checked = had.includes(l);
                lab.append(cb, mk('span', null, l)); fs.append(lab); boxes.push(cb);
            }
            fs.append(mk('span', 'settings-hint', 'With none ticked the model offers no level choice and runs at the CLI\'s own default.'));
            levelsHost.append(fs);
        } else {
            levelsHost.append(mk('p', 'settings-hint', `Coral does not pass thinking levels to ${label(provider)} yet, so there is nothing to choose here.`));
        }
    };
    drawLevels();
    f.append(levelsHost);
    if (pick) pick.addEventListener('change', () => { provider = pick.value; drawLevels(); });
    const acts = mk('div', 'cm-actions');
    const ok = mk('button', 'btn btn-small', index >= 0 ? 'Update model' : 'Add model'); ok.type = 'submit';
    const cancel = mk('button', 'btn btn-small', 'Cancel'); cancel.type = 'button';
    acts.append(ok, cancel); f.append(err, acts);
    f.addEventListener('submit', (e) => {
        e.preventDefault();
        const value = id.value.trim();
        const problem = idProblem(provider, value, index);
        if (problem) { err.textContent = problem; err.hidden = false; id.setAttribute('aria-invalid', 'true'); id.focus(); return; }
        const entry = { value, display_name: name.value.trim().slice(0, MAX_NAME), efforts: boxes.filter((b) => b.checked).map((b) => b.value) };
        if (index >= 0) rows(provider)[index] = entry; else rows(provider).push(entry);
        editing = null; render(); focusAfter(provider, index >= 0 ? index : rows(provider).length - 1);
    });
    cancel.addEventListener('click', () => { editing = null; render(); focusAdd(); });
    f.addEventListener('keydown', (e) => { if (e.key === 'Escape') { e.stopPropagation(); cancel.click(); } });
    return { f, focus: () => (pick || id).focus() };
}

function focusAdd() { const b = host && host.querySelector('[data-cm-add]'); if (b) b.focus(); }
function focusAfter(p, i) { const b = host && host.querySelector(`[data-cm-edit="${p}:${i}"]`); if (b) b.focus(); else focusAdd(); }

function render() {
    if (!host) return;
    host.replaceChildren();
    const list = mk('ul', 'cm-list');
    for (const [provider] of PROVIDERS) rows(provider).forEach((m, i) => {
        const li = mk('li', 'cm-row'); li.dataset.provider = provider;
        if (editing && editing.provider === provider && editing.index === i) { const fm = form(provider, i); li.append(fm.f); list.append(li); queueMicrotask(fm.focus); return; }
        const text = mk('span', 'cm-text'); text.append(mk('span', 'cm-provider', label(provider)), mk('code', 'cm-id', m.value)); if (m.display_name) text.append(mk('span', 'cm-name', m.display_name));
        const edit = mk('button', 'btn btn-small', 'Edit'); edit.type = 'button'; edit.dataset.cmEdit = `${provider}:${i}`; edit.setAttribute('aria-label', `Edit ${m.value}`);
        edit.addEventListener('click', () => { editing = { provider, index: i }; render(); });
        const rm = mk('button', 'btn btn-small', 'Remove'); rm.type = 'button'; rm.setAttribute('aria-label', `Remove ${m.value}`);
        rm.addEventListener('click', () => { rows(provider).splice(i, 1); if (editing && editing.provider === provider) editing = null; render(); focusAdd(); });
        li.append(text, chips(m), edit, rm); list.append(li);
    });
    const adding = editing && editing.index === -1;
    if (!totalRows() && !adding) list.append(mk('li', 'cm-empty', 'None added.'));
    host.append(list);
    if (adding) { const fm = form(editing.provider, -1); host.append(fm.f); queueMicrotask(fm.focus); }
    else {
        const add = mk('button', 'btn btn-small cm-add', 'Add a model'); add.type = 'button'; add.dataset.cmAdd = '1';
        add.disabled = PROVIDERS.every(([p]) => full(p));
        add.addEventListener('click', () => { editing = { provider: (PROVIDERS.find(([p]) => !full(p)) || PROVIDERS[0])[0], index: -1 }; render(); });
        host.append(add);
    }
    const st = mk('p', 'cm-status settings-hint'); st.setAttribute('role', 'status'); st.setAttribute('aria-live', 'polite');
    host.append(st);
    status();
}

/** Draws the section into #settings-custom-models from the server's list; the row stays hidden when the server has no such route. */
export async function initCustomModels() {
    host = document.getElementById('settings-custom-models');
    const row = document.getElementById('settings-custom-models-row');
    if (!host) return;
    let data;
    try { data = await requestJson('GET', ROUTE); } catch { loaded = false; if (row) row.hidden = true; return; }
    // dirty is decided here, after the read and before `saved` is replaced: an edit made while the read was in flight, like one made before the refill, must not be thrown away (closing the dialog does)
    const kept = loaded && customModelsDirty();
    saved = group_by_family(data && data.models);
    limits = { max_per_family: 50, efforts: {}, ...(data && data.limits) };
    if (!kept) { draft = clone(saved); editing = null; }
    loaded = true;
    if (row) row.hidden = false;
    render();
}

/** Saves the draft when it changed (Apply). Resolves true when something was saved; throws with the server's reason when it refuses. */
export async function applyCustomModels() {
    if (!customModelsDirty()) return false;
    let data;
    try { data = await requestJson('PUT', ROUTE, customModelsPayload()); } catch (e) {
        // the server names the row and the field that it refused ("custom model 2 (codex "--x"), value: ..."): Settings shows that, nothing was saved
        const d = (e && e.data) || {};
        e.userMessage = typeof d.error === 'string' && d.error ? d.error : (typeof d.message === 'string' ? d.message : 'the custom models were not saved');
        throw e;
    }
    saved = data && Array.isArray(data.models) ? group_by_family(data.models) : clone(draft);
    draft = clone(saved); editing = null; render();
    return true;
}

/** The dialog was closed: unsaved changes are dropped, so the next opening starts from what the server has. */
export function discardCustomModels() {
    draft = clone(saved); editing = null;
    if (host) render();
}
