/* The Model picker of the launch dialog and the restart form (#4066): a list of the models the agent type offers, "Default" first and
 * "Other…" last (which shows the free-text box). The list comes from GET /api/agents/models?type=… for the form's server; the last
 * answer is cached per server and type so the picker shows at once, then updates. With no list the plain text box stays. The text box
 * (.acf-model) remains the form's value: the picker writes it and reads it, so everything that reads the form is unchanged. */
import { serverFetch } from './server_base.js';
import { showToast, escapeAttr, escapeHtml } from './utils.js';
import { readModelsCache, writeModelsCache } from './model_labels.js';
import { importOnce } from './import_once.js';
const { _acfServer } = await importOnce('./launch_models.js');

const OTHER = '__other';
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
    };
}

/** The markup of the picker (inside the Model label); hidden until a list is known. */
export function modelPickerHtml() {
    return `<span class="acf-model-picker" hidden>`
        + `<span class="acf-model-row"><select class="acf-model-select" aria-label="Model"></select>`
        + `<button type="button" class="acf-model-refresh" aria-label="Refresh the model list" title="Refresh the model list"><span class="material-icons" aria-hidden="true">refresh</span></button></span>`
        + `<span class="acf-model-id"></span><span class="acf-model-note" role="status"></span></span>`;
}

function fill(container, agentType, data) {
    const { input, wrap, select, id, note } = parts(container);
    if (!input || !wrap || !select) return;
    const models = (data && Array.isArray(data.models) ? data.models : []).filter(m => m && typeof m.value === 'string' && m.value);
    container._modelList = models;
    if (!models.length) { wrap.hidden = true; input.hidden = false; return; }   // no list: the text box, as before
    select.innerHTML = `<option value="">Default</option>`
        + models.map(m => `<option value="${escapeAttr(m.value)}">${escapeHtml(m.display_name || m.value)}</option>`).join('')
        + `<option value="${escapeAttr(OTHER)}">Other…</option>`;
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
    const known = (v === '' && !container._otherChosen) || models.some(m => m.value === v);
    select.value = known ? v : OTHER;
    input.hidden = known;
    if (id) {
        const hit = models.find(m => m.value === v);
        id.textContent = hit ? [hit.value, hit.description].filter(Boolean).join(' · ') : '';
    }
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
    if (agentType === 'terminal') { wrap.hidden = true; return; }
    const cached = readModelsCache(_acfServer(container), agentType);
    if (cached) fill(container, agentType, cached); else { wrap.hidden = true; if (input) input.hidden = false; }
    for (let i = 0; i <= POLL_MAX; i++) {
        let refreshing = false;
        try { refreshing = applyAnswer(container, agentType, await fetchList(container, agentType)); } catch { /* keep what is shown */ }
        if (!refreshing || container._modelToken !== token || !container.isConnected) return;
        await new Promise(r => setTimeout(r, POLL_MS));
        if (container._modelToken !== token || !container.isConnected) return;
    }
}

/** Wire the picker's events once per form. */
export function wireModelPicker(container) {
    const { input, select, refresh } = parts(container);
    if (!select || select.dataset.wired) return;
    select.dataset.wired = '1';
    select.addEventListener('change', () => {
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
