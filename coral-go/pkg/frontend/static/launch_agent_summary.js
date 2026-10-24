/* New Agent's second step shows what the agent will use as compact rows, like Launch Team's members table (#6457): Provider (the radio group), Model, Thinking and Mode, each with its value and a Change that opens the section that sets it.
 * The values are read from the form's own fields (the sections stay the one place that sets them), so the rows can never say one thing and the launch another. */
import { modelName, levelName } from './model_labels.js';

const container = () => document.getElementById('launch-agent-acf');

/** Put the three value cells right for what the form holds now. */
export function syncAgentSummary() {
    const acf = container(), table = document.getElementById('launch-agent-summary');
    if (!acf || !table) return;
    const type = acf.querySelector('.acf-agent-type')?.value || 'claude';
    const cell = row => table.querySelector(`[data-row="${row}"] .las-value`);
    const model = (acf.querySelector('.acf-model')?.value || '').trim();
    const thinkingWrap = acf.querySelector('.acf-thinking-wrap');
    const level = !thinkingWrap || thinkingWrap.hidden ? '' : acf.querySelector('.acf-thinking')?.value || '';
    const modeSel = acf.querySelector('.acf-permission-mode');
    cell('model').textContent = model ? modelName(type, model) : 'CLI default';
    cell('thinking').textContent = level ? levelName(level) : 'CLI default';
    cell('mode').textContent = modeSel ? modeSel.options[modeSel.selectedIndex]?.textContent.trim() || '' : '';
    table.querySelector('[data-row="thinking"]').hidden = !!thinkingWrap?.hidden; // a provider with no thinking levels has no such row
}

/** Follow the form once: any change in it, and the model default arriving after it was drawn. */
export function wireAgentSummary() {
    const acf = container();
    if (!acf || acf.dataset.summaryWired) return;
    acf.dataset.summaryWired = '1';
    for (const t of ['input', 'change', 'acf-synced']) acf.addEventListener(t, () => syncAgentSummary());
}

/** Open the section of New Agent's menu that has the field (by its name in the menu: "Agent", "Permissions"). */
export function openAgentSection(name) {
    const options = document.getElementById('launch-more-options');
    if (options) options.open = true; // a phone keeps the sections under "More options": a tap on a summary row opens it with the section (#7066)
    for (const tab of document.querySelectorAll('#launch-agent-more .settings-tabs [role=tab]')) {
        if (tab.querySelector('.settings-tab-name')?.textContent.trim() === name) { tab.click(); return; }
    }
    const pick = document.querySelector('#launch-agent-more .settings-group-pick select');
    if (pick) { const o = [...pick.options].find(x => x.textContent.trim() === name); if (o) { pick.value = o.value; pick.dispatchEvent(new Event('change', { bubbles: true })); } }
}
