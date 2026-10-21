/* The dependency picker in the create and edit task dialogs (moved out of tasks.js, #4996; no behaviour change). */
import { state } from './state.js';
import { escapeHtml, escapeAttr } from './utils.js';

export function _renderDepPicker(containerId, selectedIds = [], excludeTaskId = null) {
    const container = document.getElementById(containerId);
    if (!container) return;
    const tasks = (state.currentBoardTasks || []).filter(t =>
        t.id !== excludeTaskId
    );
    const selected = new Set(selectedIds.map(Number));

    let html = `<div class="dep-picker-selected" id="${escapeAttr(containerId)}-tags"></div>`;
    html += `<div class="dep-picker-toggle"><button type="button" class="dep-picker-add-link" data-on-click="dep-picker-toggle" data-container="${escapeAttr(containerId)}" aria-expanded="false" aria-controls="${escapeAttr(containerId)}-list">+ Add dependency</button></div>`;
    html += `<div class="dep-picker-list" id="${escapeAttr(containerId)}-list" style="display:none">`;
    if (tasks.length === 0) {
        html += `<div class="dep-picker-empty">No eligible tasks</div>`;
    } else {
        tasks.forEach(t => {
            const checked = selected.has(t.id) ? ' checked' : '';
            html += `<label class="dep-picker-item"><input type="checkbox" value="${Number(t.id)}"${checked} data-on-change="dep-picker-change" data-container="${escapeAttr(containerId)}"> #${t.id} — ${escapeHtml(t.title || '')}</label>`;
        });
    }
    html += `</div>`;
    container.innerHTML = html;
    _updateDepTags(containerId);
}

export function _updateDepTags(containerId) {
    const container = document.getElementById(containerId);
    if (!container) return;
    const tagsEl = document.getElementById(`${containerId}-tags`);
    if (!tagsEl) return;
    const checked = container.querySelectorAll('input[type="checkbox"]:checked');
    if (checked.length === 0) {
        tagsEl.innerHTML = '';
        return;
    }
    const tags = Array.from(checked).map(cb => {
        const label = cb.parentElement.textContent.trim();
        return `<span class="dep-tag">${escapeHtml(label)} <button type="button" class="dep-tag-remove" data-on-click="dep-tag-remove" data-container="${escapeAttr(containerId)}" data-task-id="${Number(cb.value)}" aria-label="Remove dependency ${escapeAttr(label)}">&times;</button></span>`;
    }).join('');
    tagsEl.innerHTML = tags;
}

export function _getSelectedDeps(containerId) {
    const container = document.getElementById(containerId);
    if (!container) return [];
    return Array.from(container.querySelectorAll('input[type="checkbox"]:checked')).map(cb => parseInt(cb.value));
}
