/* Moved out of render.js as it was (#3795): no behaviour change. */
import { escapeAttr } from './utils.js';
import { redrawLiveList } from './list_hooks.js';
import { browserLocal } from './safe_storage.js';

// ── Board accent colors (localStorage) ────────────────────────────────
// The stored colour goes into style attributes (the full list and the icon rail), so only a
// #rrggbb colour is ever returned: anything else in storage (an older or hand-edited value, a
// value some other script wrote) shows as no colour (#4195).
const _boardColorRe = /^#[0-9a-f]{6}$/i;

export function _getBoardColor(boardName) {
    try {
        const colors = JSON.parse(browserLocal.get('coral-board-colors') || '{}');
        const c = Object.prototype.hasOwnProperty.call(colors, boardName) ? colors[boardName] : null;
        return typeof c === 'string' && _boardColorRe.test(c) ? c : null;
    } catch { return null; }
}

function _setBoardColor(boardName, color) {
    try {
        const colors = JSON.parse(browserLocal.get('coral-board-colors') || '{}');
        if (color) {
            colors[boardName] = color;
        } else {
            delete colors[boardName];
        }
        browserLocal.set('coral-board-colors', JSON.stringify(colors));
    } catch { /* ignore */ }
}

const _colorSwatches = [
    '#81a1c1', '#98c379', '#c678dd', '#d08770', '#e06c75',
    '#56b6c2', '#e5c07b', '#8fbcbb', '#58a6ff', '#f778ba',
    '#4aba60', '#d19a66', '#e05545', '#d2a0ee', '#61afef',
    '#e0e0e4', '#8c8c94', '#5a5a62',
];

export function setBoardAccentColor(boardName) {
    // Remove any existing picker
    const existing = document.getElementById('board-color-picker');
    if (existing) { existing.remove(); return; }

    const current = _getBoardColor(boardName) || '#58a6ff';
    const picker = document.createElement('div');
    picker.id = 'board-color-picker';
    picker.className = 'color-picker-popover';
    picker.innerHTML = `
        <div class="color-picker-swatches">
            ${_colorSwatches.map(c =>
                `<button class="color-swatch${c === current ? ' active' : ''}" style="background:${c}" data-color="${escapeAttr(c)}" title="${escapeAttr(c)}"></button>`
            ).join('')}
        </div>
        <div class="color-picker-custom">
            <input type="text" class="color-picker-hex" value="${escapeAttr(current)}" placeholder="#hex" maxlength="7">
            <button class="btn btn-small color-picker-apply">Apply</button>
            <button class="btn btn-small color-picker-reset">Reset</button>
        </div>
    `;
    document.body.appendChild(picker);

    // Position near the clicked element
    const rect = event?.target?.getBoundingClientRect();
    if (rect) {
        picker.style.top = Math.min(rect.bottom + 4, window.innerHeight - 200) + 'px';
        picker.style.left = Math.min(rect.left, window.innerWidth - 220) + 'px';
    }

    const apply = (color) => {
        _setBoardColor(boardName, color);
        redrawLiveList();
        picker.remove();
    };

    // Swatch clicks
    picker.querySelectorAll('.color-swatch').forEach(btn => {
        btn.addEventListener('click', (e) => { e.stopPropagation(); apply(btn.dataset.color); });
    });

    // Custom hex apply
    picker.querySelector('.color-picker-apply').addEventListener('click', (e) => {
        e.stopPropagation();
        const hex = picker.querySelector('.color-picker-hex').value.trim();
        if (/^#[0-9a-fA-F]{6}$/.test(hex)) apply(hex);
    });

    // Reset
    picker.querySelector('.color-picker-reset').addEventListener('click', (e) => {
        e.stopPropagation();
        _setBoardColor(boardName, null);
        redrawLiveList();
        picker.remove();
    });

    // Close on outside click
    setTimeout(() => {
        document.addEventListener('click', function handler(e) {
            if (!picker.contains(e.target)) { picker.remove(); document.removeEventListener('click', handler); }
        });
    }, 0);
}
