/* The Model picker of the launch dialog and the restart form (#4066): a list of the models the agent type offers, "Default" first and
 * "Other…" last (which shows the free-text box). The list comes from GET /api/agents/models?type=… for the form's server; the last
 * answer is cached per server and type so the picker shows at once, then updates. With no list the plain text box stays. The text box
 * (.acf-model) remains the form's value: the picker writes it and reads it, so everything that reads the form is unchanged. */
import { serverFetch } from './server_base.js';
import { showToast, escapeAttr, escapeHtml } from './utils.js';
import { readModelsCache, writeModelsCache, clearModelsCache } from './model_labels.js';
import { familyOf, addCustomModel, addModelForm, probeCanAdd, peekCanAdd } from './custom_model_add.js';
import { importOnce } from './import_once.js';
const { _acfServer } = await importOnce('./launch_models.js');

const OTHER = '__other';
const ADD = '__add';
const POLL_MS = 3000;
const POLL_MAX = 5;

const models_has = (container, v) => (container._modelList || []).some(m => m.value === v.trim());   // a value the list already names

function parts(container) {
    return {
        input: container.querySelector('.acf-model'),
        wrap: container.querySelector('.acf-model-picker'),
        select: container.querySelector('.acf-model-select'),
        id: container.querySelector('.acf-model-id'),
        refresh: container.querySelector('.acf-model-refresh'),
        note: container.querySelector('.acf-model-note'),
        add: container.querySelector('.acf-model-add'),
    };
}

/** The markup of the picker (inside the Model label); hidden until a list is known. */
export function modelPickerHtml() {
    return `<span class="acf-model-picker" hidden>`
        + `<span class="acf-model-row"><select class="acf-model-select" aria-label="Model"></select>`
        + `<button type="button" class="acf-model-refresh" aria-label="Refresh the model list" title="Refresh the model list"><span class="material-icons" aria-hidden="true">refresh</span></button></span>`
        + `<span class="acf-model-id"></span><span class="acf-model-note" role="status"></span><span class="acf-model-add" hidden></span></span>`;
}

function fill(container, agentType, data) {
    const { input, wrap, select, id, note } = parts(container);
    container._thinkingLevels = data && Array.isArray(data.thinking) ? data.thinking : [];   // the levels the server passes this type's CLI (#4969)
    syncThinking(container);
    if (!input || !wrap || !select) return;
    const models = (data && Array.isArray(data.models) ? data.models : []).filter(m => m && typeof m.value === 'string' && m.value);
    container._modelList = models;
    if (!models.length) { wrap.hidden = true; input.hidden = false; return; }   // no list: the text box, as before
    select.innerHTML = `<option value="">Default</option>`
        + models.map(m => `<option value="${escapeAttr(m.value)}">${escapeHtml((m.display_name || m.value) + (m.custom === true ? ' (custom)' : ''))}</option>`).join('')
        + `<option value="${escapeAttr(OTHER)}">Other…</option>`
        + (familyOf(agentType) && peekCanAdd(_acfServer(container)) !== false ? `<option value="${escapeAttr(ADD)}">Add a model…</option>` : '');
    wrap.hidden = false;
    if (note) note.textContent = data && data.error ? data.error : (data && data.refreshing ? 'Updating the list…' : '');
    syncModelPicker(container);
    if (id) id.hidden = false;
}

/** Make the picker show what the text box holds (call after the box's value was set from code: `fromCode` forgets an earlier "Other…" choice). */
export function syncModelPicker(container, fromCode = true) {
    const { input, wrap, select, id } = parts(container);
    if (!input || !wrap || !select || wrap.hidden) return;
    if (fromCode) container._otherChosen = false;
    const v = input.value.trim();
    const models = container._modelList || [];
    const listed = (v === '' && !container._otherChosen) || models.some(m => m.value === v);
    // A model that is set (the one the agent runs) and that the list does not name shows as itself, in an option of its own
    // after the list, not under "Other…" (#5044: with the CLI silent the list is the built-in one and the running model is
    // rarely in it). "Other…" is the user's own choice, and what it holds stays free text.
    select.querySelectorAll('option[data-unlisted]').forEach(o => o.remove());
    const unlisted = !listed && v !== '' && !container._otherChosen;
    if (unlisted) {
        const opt = document.createElement('option');
        opt.value = v; opt.textContent = v; opt.dataset.unlisted = '1';
        select.insertBefore(opt, select.querySelector(`option[value="${OTHER}"]`));
    }
    const known = listed || unlisted;
    select.value = known ? v : OTHER;
    container._lastSel = select.value;   // what the list showed before "Add a model…" was chosen, to go back to
    input.hidden = known;
    if (id) {
        const hit = models.find(m => m.value === v);
        id.textContent = hit ? [hit.value, hit.description].filter(Boolean).join(' · ') : '';
    }
    syncThinking(container);   // the model decides which levels it takes
}

async function fetchList(container, agentType, { refresh = false } = {}) {
    const server = _acfServer(container);
    const url = `/api/agents/models${refresh ? '/refresh' : ''}?type=${encodeURIComponent(agentType)}`;
    const resp = await serverFetch(server, url, refresh ? { method: 'POST' } : undefined);
    if (refresh && resp.status === 429) { showToast('The list was just refreshed. Try again in 30 seconds.', true); return null; }
    if (refresh && (resp.status === 401 || resp.status === 403)) { showToast('Only the operator can refresh the model list.', true); return null; }
    if (!resp.ok) return null;
    const data = await resp.json().catch(() => null);
    if (!data || !Array.isArray(data.models)) return null;
    return { server, data };
}

function applyAnswer(container, agentType, got) {
    if (!got || !container.isConnected) return false;
    if ((container.querySelector('.acf-agent-type')?.value || 'claude') !== agentType) return false;   // the type changed meanwhile
    if (got.data.models.length) writeModelsCache(got.server, agentType, got.data);
    fill(container, agentType, got.data);
    return !!got.data.refreshing;
}

/** Load the list for the form's current agent type: the cached one at once, then the server's, polling while it says it is refreshing. */
export async function loadModelPicker(container, agentType) {
    const { wrap, input, select } = parts(container);
    if (!container || !wrap || !select) return;
    const token = container._modelToken = (container._modelToken || 0) + 1;
    closeAdd(container, false);
    if (agentType === 'terminal') { wrap.hidden = true; container._thinkingLevels = []; syncThinking(container); return; }
    const cached = readModelsCache(_acfServer(container), agentType);
    if (cached) fill(container, agentType, cached); else { wrap.hidden = true; if (input) input.hidden = false; }
    const server = _acfServer(container); // #5996: a hub grantee cannot add models
    if (familyOf(agentType)) probeCanAdd(server, () => { if (container._modelToken === token && container.isConnected) select.querySelector(`option[value="${ADD}"]`)?.remove(); });
    for (let i = 0; i <= POLL_MAX; i++) {
        let refreshing = false;
        try { refreshing = applyAnswer(container, agentType, await fetchList(container, agentType)); } catch { /* keep what is shown */ }
        if (!refreshing || container._modelToken !== token || !container.isConnected) return;
        await new Promise(r => setTimeout(r, POLL_MS));
        if (container._modelToken !== token || !container.isConnected) return;
    }
}

/** "Add a model…": the small form under the list (shared with the model chip's menu, custom_model_add.js). A new model goes in the operator's custom models and is chosen for this form. */
function openAdd(container) {
    const { select, add } = parts(container);
    if (!add) return;
    select.value = container._lastSel || '';   // the list keeps showing what it showed: the form is the next step, not a model
    if (add._form) { add._form.focus(); return; }
    const agentType = container.querySelector('.acf-agent-type')?.value || 'claude';
    const f = addModelForm({
        prefix: `acf-model-add-${Math.random().toString(36).slice(2, 8)}`,
        cancel: () => { closeAdd(container, true); },
        submit: async (id, name) => {
            const res = await addCustomModel({ server: _acfServer(container), agentType, id, name, known: (container._modelList || []).map(m => m.value) });
            if (!res.ok) return res.error;
            clearModelsCache();
            await loadModelPicker(container, agentType);   // the catalog has it now (this also closes the form)
            const { input: box, select: sel } = parts(container);
            box.value = id.trim(); container._otherChosen = false;
            box.dispatchEvent(new Event('input', { bubbles: true }));   // the form's own "the user chose a model" bookkeeping
            syncModelPicker(container, false);
            showToast(`Model added: ${name.trim() || id.trim()}`);
            sel.focus();
            return '';
        },
    });
    add._form = f; add.replaceChildren(f.form); add.hidden = false; f.focus();
}

function closeAdd(container, focusSelect) {
    const { select, add } = parts(container);
    if (!add || !add._form) return;
    add._form = null; add.replaceChildren(); add.hidden = true;
    if (focusSelect && select) select.focus();
}

/** Wire the picker's events once per form. */
export function wireModelPicker(container) {
    const { input, select, refresh } = parts(container);
    if (!select || select.dataset.wired) return;
    select.dataset.wired = '1';
    select.addEventListener('change', () => {
        if (select.value === ADD) { openAdd(container); return; }
        container._otherChosen = select.value === OTHER;   // an empty box under Other… stays Other…, not Default
        if (select.value === OTHER) { if (models_has(container, input.value)) input.value = ''; input.hidden = false; input.focus(); }
        else { input.value = select.value; input.hidden = true; }
        input.dispatchEvent(new Event('input', { bubbles: true }));   // the form's own "the user chose a model" bookkeeping
        syncModelPicker(container, false);
    });
    refresh.addEventListener('click', async () => {
        const agentType = container.querySelector('.acf-agent-type')?.value || 'claude';
        refresh.disabled = true; refresh.setAttribute('aria-busy', 'true');
        try {
            const got = await fetchList(container, agentType, { refresh: true });
            if (got) {
                applyAnswer(container, agentType, got);
                if (got.data.refreshing) await loadModelPicker(container, agentType);
            }
        } catch { showToast('Could not reach Coral to refresh the list.', true); }
        refresh.disabled = false; refresh.removeAttribute('aria-busy');
    });
}

// ── Thinking level (#4969) ───────────────────────────────────────────────
// A Thinking select beside the Model: the levels the server passes this agent type's CLI (the models reply's `thinking`), narrowed to
// the chosen model's own `efforts` when the catalog lists them (the CLI clamps a level its model does not take, so the form only
// offers levels it accepts). Hidden when the type takes none. The form's value is `thinkingValue`: undefined until the user chooses or
// the form is given a level, so a restart that was not asked to change it leaves the agent's level alone.
const LEVEL_NAMES = { low: 'Low', medium: 'Medium', high: 'High', xhigh: 'Extra high', max: 'Max' };

/** The markup of the Thinking select (after the Model label's row). */
export function thinkingPickerHtml() {
    return `<label class="acf-thinking-wrap" hidden>Thinking <span style="color:var(--text-muted);font-weight:normal">(optional)</span>:`
        + `<select class="acf-thinking" aria-label="Thinking level"></select>`
        + `<span class="acf-thinking-note" role="status"></span><span class="acf-thinking-ran" role="status"></span></label>`;
}

function thinkingParts(container) {
    return { wrap: container.querySelector('.acf-thinking-wrap'), select: container.querySelector('.acf-thinking'), note: container.querySelector('.acf-thinking-note'), ran: container.querySelector('.acf-thinking-ran') };
}

/** What the agent last ran at when that is not the level the form holds (a restart keeps the stored one): "The agent last ran at Max (changed inside the CLI)." */
function ranNote(r) {
    if (!r || !r.actual || r.actual === (r.stored || '')) return '';
    return `The agent last ran at ${LEVEL_NAMES[r.actual] || r.actual}${r.stored ? ' (changed inside the CLI)' : ''}.`;
}

/** Tell the form what the agent's transcript shows it running at, beside the level it was given (setThinking): the two can differ after a change made inside the CLI. */
export function setThinkingActual(container, stored, actual) {
    container._thinkingRan = actual ? { stored: stored || '', actual: String(actual) } : null;
    syncThinking(container);
}

/** Show the levels this type and model take; keep the choice when it is still offered, else fall back to Default and say so. */
export function syncThinking(container) {
    const { wrap, select, note, ran } = thinkingParts(container);
    if (!wrap || !select) return;
    if (ran) ran.textContent = ranNote(container._thinkingRan);
    const agentType = container.querySelector('.acf-agent-type')?.value || 'claude';
    const typeLevels = agentType === 'terminal' ? [] : (container._thinkingLevels || []);
    const model = (container.querySelector('.acf-model')?.value || '').trim();
    const hit = (container._modelList || []).find(m => m.value === model);
    // a custom model (#5480) lists exactly the levels its owner ticked: none ticked means no level choice (a built-in model with no list takes every level the type has)
    const efforts = hit && hit.custom === true ? (Array.isArray(hit.efforts) ? hit.efforts : []) : (hit && Array.isArray(hit.efforts) && hit.efforts.length ? hit.efforts : null);
    const allowed = efforts ? typeLevels.filter(l => efforts.includes(l)) : typeLevels;
    if (!allowed.length) { wrap.hidden = true; if (note) note.textContent = ''; container.dispatchEvent(new Event('acf-synced', { bubbles: true })); return; } // (a summary of the form follows it showing or hiding)
    const wanted = container._thinkingWanted !== undefined ? container._thinkingWanted : select.value;
    select.innerHTML = `<option value="">Default</option>` + allowed.map(l => `<option value="${escapeAttr(l)}">${escapeHtml(LEVEL_NAMES[l] || l)}</option>`).join('');
    const keep = wanted === '' || allowed.includes(wanted);
    select.value = keep ? wanted : '';
    if (note) note.textContent = keep ? '' : `${LEVEL_NAMES[wanted] || wanted} is not offered for ${model || 'this model'}: back to Default.`;
    wrap.hidden = false;
    container.dispatchEvent(new Event('acf-synced', { bubbles: true }));
}

/** The form's thinking level: undefined when it was not chosen or given and so should not be sent, else "" (Default) or a level. */
export function thinkingValue(container) {
    const { wrap, select } = thinkingParts(container);
    if (!wrap || !select || wrap.hidden || container.dataset.acfThinkingSet !== 'true') return undefined;
    return select.value;
}

/** Give the form a level (from a saved config or the running agent); undefined or null forgets the choice. */
export function setThinking(container, level) {
    const { select } = thinkingParts(container);
    if (!select) return;
    if (level === undefined || level === null) { container._thinkingWanted = undefined; delete container.dataset.acfThinkingSet; select.value = ''; syncThinking(container); return; }
    container._thinkingWanted = String(level);
    container.dataset.acfThinkingSet = 'true';
    syncThinking(container);
}

/** Wire the select once per form: a user's choice is remembered and counts as set. */
export function wireThinking(container) {
    const { select } = thinkingParts(container);
    if (!select || select.dataset.wired) return;
    select.dataset.wired = '1';
    select.addEventListener('change', () => {
        container._thinkingWanted = select.value;
        container.dataset.acfThinkingSet = 'true';
        syncThinking(container);
    });
}
