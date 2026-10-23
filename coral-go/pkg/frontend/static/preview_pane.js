/* Bottom preview pane — tabbed file/artifact viewer below the terminal/chat area */

import { isPhoneLayout } from './phone_layout.js';
import { state } from './state.js';
import { registerUnsavedWork } from './update_banner.js';
import { escapeHtml, escapeAttr, showToast, isImagePath, isMediaPath, renderImagePanes, renderMediaPanes, DOMPURIFY_CONFIG, sanitizeHtml, renderMarkdownStrict, highlightCodeIn, keepFragmentsInside } from './utils.js';
import { getCm, getLangExtension, getLangFromPath, DIFF_CONFIG } from './cm_util.js';
import { loadCodeMirror, loadHighlight, showVendorLoading, showVendorLoadError } from './vendor_loader.js';
import { makeDraggable } from './draggable.js';
import { fitTerminal } from './xterm_renderer.js';
import { toggleStarFile, isFileStarred } from './changed_files.js';
import { renderArtifact, resolveArtifactSource } from './artifact_preview.js';
import { serverUrl, currentServer, isCloud } from './server_base.js';
import { fetchImageSrc } from './image_source.js';
import { wireDownloads } from './artifact_download.js';
import { retryOnce } from './overload.js';
import { agentName, apiQs } from './agent_file_api.js';
import { trapTab } from './dialog_focus.js';
import { pushOverlay, popOverlay } from './overlay_history.js';
import { browserLocal } from './safe_storage.js';
import { importOnce } from './import_once.js';

const ICON_MAP = {
    javascript: '{ }', typescript: '{ }', jsx: '{ }', tsx: '{ }',
    json: '{ }', css: '⬡', html: '◇', markdown: '¶',
    python: '⌘', go: '⌘', rust: '⌘', java: '⌘', c: '⌘', cpp: '⌘',
    sql: '⊞', yaml: '≡', toml: '≡', xml: '◇', svg: '◇',
    shell: '$', bash: '$', zsh: '$', dockerfile: '▣',
};

function _fileIcon(tab) {
    if (tab.kind === 'artifact') return '❖';
    if (tab.kind === 'chat-image') return '▣';
    return ICON_MAP[getLangFromPath(tab.filepath)] || '◻';
}

function _fileName(filepath) {
    const i = filepath.lastIndexOf('/');
    return i === -1 ? filepath : filepath.substring(i + 1);
}

function _tabTitle(tab) {
    if (tab.kind === 'artifact') return tab.title || tab.options.filename || 'Artifact';
    if (tab.kind === 'chat-image') return tab.filepath; // the image's label (its name in the chat)
    return _fileName(tab.filepath);
}

function _fileUrl(endpoint, filepath, extra) {
    const qs = apiQs(filepath);
    if (extra) for (const [k, v] of Object.entries(extra)) qs.set(k, v);
    return serverUrl(`/api/sessions/live/${encodeURIComponent(agentName())}/${endpoint}?${qs}`, currentServer());
}

// Tab: { id, kind: 'file'|'artifact', filepath, line, mode: 'preview'|'edit'|'diff',
//        content, savedContent, originalContent, hasDiff, canDiff, renderGen,
//        pinned, options?, title?, abort? }
// Unpinned tabs are previews: at most one exists, and the next plain open replaces it.
// A tab is pinned by double-clicking it, opening the same file twice, or editing it.
let _tabs = [];
let _activeId = null;
let _seq = 0;
let _paneHeight = null;
let _paneWidth = null;
let _sessionKey = null;   // agent whose tabs are showing
const _stash = new Map(); // session key -> { tabs, activeId } for agents in the background
let _dock = 'bottom';      // 'bottom' (under the side panel tabs) | 'column' (full-height column)
let _phoneFull = false;     // a phone's preview covers the screen (#5173); not saved
let _fullOverlay = 0;       // its entry in overlay_history.js, while it is open
let _fullOpener = null;     // what had focus when it opened
let _maximised = false;    // the column covers Chat (#4931): not saved (a reload opens with Chat); ends when the pane is hidden, moves to the bottom, or another agent is shown

const DOCK_ICONS = {
    // Shown while docked at the bottom: switch to a full-height column.
    bottom: '<svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="1.5" y="2.5" width="13" height="11" rx="1.5"/><path d="M6 2.5v11"/></svg>',
    // Shown while docked as a column: switch back to the bottom.
    column: '<svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="1.5" y="2.5" width="13" height="11" rx="1.5"/><path d="M1.5 9h13"/></svg>',
};

// A maximised preview shows these in the tab bar (a toggle, #4931): maximise = the pane takes the whole centre, Chat hidden behind it; restore = the old split.
const MAX_ICON_OFF = '<svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9.5 2.5h4v4M6.5 13.5h-4v-4M13.5 2.5l-4.5 4.5M2.5 13.5 7 9"/></svg>';
const MAX_ICON_ON = '<svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M13.5 6.5h-4v-4M2.5 9.5h4v4M9.5 6.5l4-4M6.5 9.5l-4 4"/></svg>';

// The column's width (#4931). What is saved is the person's wish; what is shown is that wish held to what the window has room for, every time it is placed, so a width saved in a big window, a
// corrupted value or a narrow window can never leave a pane that cannot be seen or dragged: at least COLUMN_MIN wide, and Chat keeps at least CHAT_MIN. (The side panel is outside .live-body.)
const COLUMN_MIN = 240, COLUMN_DEFAULT = 480, CHAT_MIN = 240, HANDLE_W = 1;
function _maxColumnWidth() {
    const body = document.querySelector('.live-body');
    const room = body ? body.getBoundingClientRect().width : window.innerWidth;
    return Math.max(COLUMN_MIN, Math.floor(room - CHAT_MIN - HANDLE_W));
}
function _clampColumnWidth(w) {
    w = Number(w);
    if (!Number.isFinite(w) || w <= 0) w = COLUMN_DEFAULT;
    return Math.round(Math.min(Math.max(w, COLUMN_MIN), _maxColumnWidth()));
}
function _clampPaneHeight(h) {
    h = Number(h);
    const side = document.getElementById('agentic-state');
    const max = Math.max(100, Math.floor((side ? side.getBoundingClientRect().height : window.innerHeight) * 0.7));
    if (!Number.isFinite(h) || h <= 0) return null;
    return Math.round(Math.min(Math.max(h, 100), max));
}
const _savedNumber = key => { const n = parseInt(browserLocal.get(key), 10); return Number.isFinite(n) && n > 0 ? n : null; };

const _isMobile = () => isPhoneLayout();
const _effectiveDock = () => (_isMobile() ? 'bottom' : _dock);
let _cmView = null;        // editor or merge view for the active tab
let _fallbackEditor = null;

export function initPreviewPane() {
    const handle = document.getElementById('preview-pane-resize-handle');
    if (!handle) return;

    _paneHeight = _savedNumber('coral-preview-pane-height');

    makeDraggable(handle, {
        cursor: 'row-resize',
        onMove: (e) => {
            const container = document.getElementById('agentic-state');
            if (!container) return null;
            const rect = container.getBoundingClientRect();
            const newHeight = rect.bottom - e.clientY;
            const min = 100;
            const max = rect.height * 0.7;
            return Math.min(Math.max(newHeight, min), max);
        },
        onFlush: (v) => {
            const pane = document.getElementById('preview-pane');
            if (pane) pane.style.height = v + 'px';
        },
        onEnd: (v) => {
            if (v != null) {
                _paneHeight = Math.round(v);
                browserLocal.set('coral-preview-pane-height', _paneHeight);
            }
        },
    });

    _paneWidth = _savedNumber('coral-preview-column-width');
    if (browserLocal.get('coral-preview-pane-dock') === 'column') _dock = 'column';

    const colHandle = document.getElementById('preview-column-resize-handle');
    const column = document.getElementById('preview-column');
    const liveBody = document.querySelector('.live-body');
    if (colHandle && column && liveBody) {
        makeDraggable(colHandle, {
            cursor: 'col-resize',
            onMove: (e) => {
                // The room is .live-body's: the side panel is outside it (it was taken off again here, so the limit fell to the minimum and the pane could only be dragged narrower, #4931).
                return Math.min(Math.max(column.getBoundingClientRect().right - e.clientX, COLUMN_MIN), _maxColumnWidth());
            },
            onFlush: (v) => { column.style.width = v + 'px'; _syncColumnHandle(v); },
            onEnd: (v) => {
                if (v != null) {
                    _paneWidth = Math.round(v);
                    browserLocal.set('coral-preview-column-width', _paneWidth);
                }
            },
        });
        // The handle by keyboard (a separator with a value): Left widens, Right narrows, Home and End go to the ends; kept where the window has room.
        colHandle.addEventListener('keydown', (e) => {
            const step = e.shiftKey ? 96 : 24;
            const now = column.getBoundingClientRect().width;
            const next = e.key === 'ArrowLeft' ? now + step : e.key === 'ArrowRight' ? now - step : e.key === 'Home' ? COLUMN_MIN : e.key === 'End' ? _maxColumnWidth() : null;
            if (next === null) return;
            e.preventDefault();
            _paneWidth = _clampColumnWidth(next);
            browserLocal.set('coral-preview-column-width', _paneWidth);
            _applyColumnWidth();
            fitTerminal();
        });
        // The room changes with the window, the side panel opening or shutting, and the sidebar's width: the width shown is held to it again each time.
        if (typeof ResizeObserver === 'function') new ResizeObserver(() => { if (_tabs.length && _effectiveDock() === 'column' && !_maximised) _applyColumnWidth(); }).observe(liveBody);
    }

    document.getElementById('preview-max-btn')?.addEventListener('click', () => (_isMobile() ? _setPhoneFull(!_phoneFull) : _setMaximised(!_maximised)));
    document.getElementById('preview-full-back')?.addEventListener('click', () => _setPhoneFull(false));
    // Escape gives the maximised pane back its place, unless something in it (the editor's own search box) already used the key.
    document.addEventListener('keydown', (e) => {
        if (_phoneFull && e.key === 'Tab' && !e.defaultPrevented) { trapTab(document.getElementById('preview-pane'), e); return; } // focus stays inside the full-screen preview
        if (e.key !== 'Escape' || !(_maximised || _phoneFull) || e.defaultPrevented) return;
        if ([...document.querySelectorAll('.modal')].some(m => m.style.display && m.style.display !== 'none')) return; // a dialog has the key
        if (_phoneFull) _setPhoneFull(false); else _setMaximised(false);
        e.preventDefault();
        document.getElementById('preview-max-btn')?.focus();
    });

    document.getElementById('preview-dock-btn')?.addEventListener('click', () => {
        _dock = _dock === 'column' ? 'bottom' : 'column';
        browserLocal.set('coral-preview-pane-dock', _dock);
        _placePane();
    });
    window.addEventListener('resize', () => { if (_tabs.length) _placePane(); });
    _placePane();
    _setMaximised(false); // paints the toggle's icon and label

    // Toolbar buttons are re-rendered per tab, so listen on the container.
    document.getElementById('preview-toolbar')?.addEventListener('click', _onToolbarClick);
}

/** Show the column at the width wished for, held to the room there is (the wish itself is kept for when the window grows back). */
function _applyColumnWidth() {
    const column = document.getElementById('preview-column');
    if (!column) return;
    const w = _clampColumnWidth(_paneWidth);
    column.style.width = w + 'px';
    _syncColumnHandle(w);
}

function _syncColumnHandle(w) {
    const h = document.getElementById('preview-column-resize-handle');
    if (!h) return;
    h.setAttribute('aria-valuemin', String(COLUMN_MIN));
    h.setAttribute('aria-valuemax', String(_maxColumnWidth()));
    h.setAttribute('aria-valuenow', String(Math.round(w)));
}

/** Maximise the column over Chat, or give Chat its place back (#4931). The width the column had is untouched: restoring is taking one class off. */
function _setMaximised(on) {
    on = !!on && _effectiveDock() === 'column' && _tabs.length > 0;
    const body = document.querySelector('.live-body');
    const changed = on !== _maximised;
    _maximised = on;
    if (body) body.classList.toggle('preview-maximised', on);
    _paintMaxButton();
    if (changed) fitTerminal(); // the chat or terminal got its room back or lost it
}

// The toggle in the tab bar: maximise over the chat (the column), or full screen (a phone).
function _paintMaxButton() {
    const btn = document.getElementById('preview-max-btn');
    if (!btn) return;
    const phone = _isMobile(), on = phone ? _phoneFull : _maximised;
    btn.innerHTML = on ? MAX_ICON_ON : MAX_ICON_OFF;
    btn.title = phone ? (on ? 'Leave full screen (Escape)' : 'Show the preview full screen') : (on ? 'Restore the preview beside the chat (Escape)' : 'Maximise the preview over the chat');
    btn.setAttribute('aria-label', btn.title);
    btn.setAttribute('aria-pressed', String(on));
}

/** A phone's preview takes the whole screen (#5173): the pane leaves the half of the side panel it sits in for a fixed layer over the page, with a Back button, and Escape or the
 *  browser's Back closes it. Nothing about the tabs changes, so closing it returns to the sheet as it was. */
function _setPhoneFull(on, { popped = false } = {}) {
    on = !!on && _isMobile() && _tabs.length > 0;
    const pane = document.getElementById('preview-pane');
    const btn = document.getElementById('preview-max-btn');
    if (!pane || on === _phoneFull) return;
    _phoneFull = on;
    pane.classList.toggle('preview-phone-full', on);
    if (on) {
        pane.setAttribute('role', 'dialog'); pane.setAttribute('aria-modal', 'true'); pane.setAttribute('aria-label', 'Preview');
        _fullOpener = document.activeElement;
        _fullOverlay = pushOverlay(() => _setPhoneFull(false, { popped: true })); // Back closes it, in its order among the overlays (overlay_history.js)
    } else {
        pane.removeAttribute('role'); pane.removeAttribute('aria-modal'); pane.removeAttribute('aria-label');
        if (_fullOverlay) { const id = _fullOverlay; _fullOverlay = 0; if (!popped) popOverlay(id); }
    }
    _paintMaxButton();
    if (on) document.getElementById('preview-full-back')?.focus();
    else { const o = _fullOpener; _fullOpener = null; if (o && o.isConnected) o.focus({ preventScroll: true }); }
}

/** Move the pane between the side panel (bottom) and its own column, then show it if it has tabs. */
function _placePane() {
    const pane = document.getElementById('preview-pane');
    const bottomHandle = document.getElementById('preview-pane-resize-handle');
    const column = document.getElementById('preview-column');
    const colHandle = document.getElementById('preview-column-resize-handle');
    const side = document.getElementById('agentic-state');
    if (!pane || !bottomHandle || !column || !colHandle || !side) return;

    const dock = _effectiveDock();
    const visible = _tabs.length > 0;
    if (dock === 'column') {
        if (pane.parentElement !== column) column.appendChild(pane);
        pane.classList.add('docked-column');
        pane.style.height = '';
    } else {
        if (pane.parentElement !== side) side.appendChild(pane);
        pane.classList.remove('docked-column');
        const h = _clampPaneHeight(_paneHeight);
        if (h) pane.style.height = h + 'px';
    }
    column.style.display = dock === 'column' && visible ? '' : 'none';
    colHandle.style.display = dock === 'column' && visible ? '' : 'none';
    bottomHandle.style.display = dock === 'bottom' && visible ? '' : 'none';
    pane.style.display = visible ? '' : 'none';
    if (dock === 'column' && visible) _applyColumnWidth();
    // Maximise is the column's: nothing to cover at the bottom, on a phone (the pane is full screen there) or with no tabs.
    if (_maximised && !(dock === 'column' && visible)) _setMaximised(false);
    if (_phoneFull && !(_isMobile() && visible)) _setPhoneFull(false); // the last tab closed, or the window grew past a phone
    const maxBtn = document.getElementById('preview-max-btn');
    if (maxBtn) { maxBtn.hidden = _isMobile() ? !visible : !(dock === 'column' && visible); _paintMaxButton(); }

    const btn = document.getElementById('preview-dock-btn');
    if (btn) {
        btn.innerHTML = DOCK_ICONS[dock];
        btn.title = dock === 'column' ? 'Dock preview at the bottom of the side panel' : 'Dock preview as a full-height column';
        btn.setAttribute('aria-label', btn.title);
    }
}

function _showPane() {
    if (_effectiveDock() === 'bottom') {
        const agenticState = document.getElementById('agentic-state');
        if (agenticState && agenticState.classList.contains('collapsed')) {
            agenticState.classList.remove('collapsed');
            document.getElementById('agentic-collapse-btn')?.classList.remove('collapsed');
        }
        if (_isMobile()) window.toggleAgenticPanel?.(true);
    }
    _placePane();
}

function _hidePane() {
    _comments?.close(); // no tab, no comments (#5311)
    _placePane();
}

function _activeTab() {
    return _tabs.find(t => t.id === _activeId) || null;
}

function _isDirty(tab) {
    return tab.kind === 'file' && tab.content !== null && tab.savedContent !== null && tab.content !== tab.savedContent;
}

// A reload after a server update waits for unsaved file edits (update_banner.js, #3751).
registerUnsavedWork(() => _tabs.some(_isDirty));

/* ── Tab bar ───────────────────────────────────────────────── */

function _renderTabs() {
    const bar = document.getElementById('preview-tabs');
    if (!bar) return;

    let html = '';
    for (const tab of _tabs) {
        const active = tab.id === _activeId ? ' active' : '';
        const title = tab.kind === 'artifact' ? (tab.options.filename || tab.filepath) : tab.filepath;
        const preview = tab.pinned ? '' : ' preview';
        html += `<div class="preview-tab${active}${preview}" data-tab-id="${escapeAttr(tab.id)}" title="${tab.pinned ? '' : 'Preview — double-click to keep open'}">
            <span class="preview-tab-icon">${_fileIcon(tab)}</span>
            <span class="preview-tab-name" title="${escapeAttr(title)}">${escapeHtml(_tabTitle(tab))}${_isDirty(tab) ? ' •' : ''}</span>
            <button class="preview-tab-close" data-tab-id="${escapeAttr(tab.id)}" title="Close">&times;</button>
        </div>`;
    }
    bar.innerHTML = html;

    bar.querySelectorAll('.preview-tab').forEach(el => {
        el.addEventListener('click', (e) => {
            if (e.target.closest('.preview-tab-close')) return;
            switchTab(el.dataset.tabId);
        });
    });
    bar.querySelectorAll('.preview-tab').forEach(el => {
        el.addEventListener('dblclick', (e) => {
            if (e.target.closest('.preview-tab-close')) return;
            _pin(_tabs.find(t => t.id === el.dataset.tabId));
        });
    });
    bar.querySelectorAll('.preview-tab-close').forEach(btn => {
        btn.addEventListener('click', (e) => {
            e.stopPropagation();
            closeTab(btn.dataset.tabId);
        });
    });
}

/* ── Toolbar ───────────────────────────────────────────────── */

function _modeBtn(mode, icon, title, tab) {
    return `<button class="inline-preview-mode-btn${tab.mode === mode ? ' active' : ''}" data-action="mode" data-mode="${escapeAttr(mode)}" title="${escapeAttr(title)}" aria-label="${escapeAttr(title)}"><span class="material-icons" aria-hidden="true">${icon}</span></button>`;
}

function _renderToolbar(tab) {
    const bar = document.getElementById('preview-toolbar');
    if (!bar) return;
    if (!tab) { bar.innerHTML = ''; return; }

    if (tab.kind === 'artifact') {
        const src = resolveArtifactSource(tab.filepath, tab.options);
        // The managed-artifact url is server-relative; point it at the viewed server (the hub path) for the link/download.
        const dlUrl = src ? (src.externalURL ? src.url : serverUrl(src.url, src.server)) : '';
        const link = src
            ? `<a class="team-artifacts-action" href="${escapeAttr(dlUrl)}" ${src.externalURL ? 'target="_blank" rel="noopener noreferrer"' : `download="${escapeAttr(tab.options.filename || '')}" data-artifact-download="${escapeAttr(dlUrl)}" data-artifact-name="${escapeAttr(tab.options.filename || '')}"`}>${src.externalURL ? 'Open link' : 'Download'}</a>`
            : '';
        bar.innerHTML = `<span class="preview-toolbar-path" title="${escapeAttr(tab.options.filename || tab.filepath)}">${escapeHtml(tab.options.filename || 'Artifact preview')}</span>
            <div class="inline-preview-actions">${link}</div>`;
        wireDownloads(bar);
        return;
    }

    if (tab.kind === 'chat-image') { // an image the chat showed (#5028): its name, nothing to edit or star
        const unshare = tab.unshare ? `<div class="inline-preview-actions"><button type="button" class="inline-preview-mode-btn preview-unshare-btn" data-action="unshare" aria-label="Remove shared copy of ${escapeAttr(tab.filepath)}" title="Remove shared copy"><span class="material-icons" aria-hidden="true">delete</span></button></div>` : '';
        bar.innerHTML = `<span class="preview-toolbar-path" title="${escapeAttr(tab.filepath)}">${escapeHtml(tab.filepath)}</span>${unshare}`;
        return;
    }

    const starred = isFileStarred(tab.filepath);
    const binary = isImagePath(tab.filepath) || isMediaPath(tab.filepath);
    const buttons = [
        `<button class="inline-preview-mode-btn file-star-btn${starred ? ' starred' : ''}" data-action="star" title="${starred ? 'Unstar' : 'Star'}">${starred ? '★' : '☆'}</button>`,
        tab.canDiff ? _modeBtn('diff', 'difference', 'Diff', tab) : '',
        _modeBtn('preview', 'visibility', 'Preview', tab),
        binary ? '' : _modeBtn('edit', 'edit', 'Edit', tab),
        !binary && tab.mode === 'edit' ? `<button class="inline-preview-save" data-action="save">Save</button>` : '',
    ].join('');
    bar.innerHTML = `<span class="preview-toolbar-path" title="${escapeAttr(tab.filepath)}">${escapeHtml(tab.filepath)}</span>
        <div class="inline-preview-actions">${buttons}</div>`;
}

function _onToolbarClick(e) {
    const btn = e.target.closest('[data-action]');
    const tab = _activeTab();
    if (!btn || !tab) return;
    if (btn.dataset.action === 'mode') setMode(tab, btn.dataset.mode);
    else if (btn.dataset.action === 'star') {
        toggleStarFile(tab.filepath);
        _renderToolbar(tab);
    } else if (btn.dataset.action === 'unshare') tab.unshare?.(() => closeTab(tab.id));
    else if (btn.dataset.action === 'save') saveTab(tab, btn);
}

/* ── Open / switch / close ─────────────────────────────────── */

function _pin(tab) {
    if (!tab || tab.pinned) return;
    tab.pinned = true;
    _renderTabs();
}

/** Add a tab, replacing the current preview (unpinned) tab in place if there is one. */
function _addTab(tab) {
    const i = _tabs.findIndex(t => !t.pinned);
    if (i === -1) { _tabs.push(tab); return; }
    _disposeTab(_tabs[i]);
    _tabs[i] = tab;
}

function _newTab(props) {
    return {
        id: 'ptab-' + (++_seq), kind: 'file', filepath: '', line: 0, mode: 'preview',
        content: null, savedContent: null, originalContent: null, hasDiff: false,
        canDiff: false, renderGen: 0, pinned: false, ...props,
    };
}

function _activate(tab) {
    _captureEditor();
    _activeId = tab.id;
    _renderTabs();
    _showPane();
    _render(tab);
}

/**
 * Open a repository file in a tab.
 * opts.mode: initial mode ('preview' | 'edit' | 'diff').
 * opts.diff: file comes from the changed-files list, so the Diff mode is offered.
 */
export function openPreviewTab(filepath, line, opts = {}) {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    if (!filepath) return;

    const mode = opts.mode || (opts.diff ? 'diff' : 'preview');
    let tab = _tabs.find(t => t.kind === 'file' && t.filepath === filepath);
    if (tab) {
        _pin(tab); // opening a preview a second time keeps it
        tab.line = line || 0;
        if (opts.diff) tab.canDiff = true;
        // Unsaved edits win over whatever is on disk; otherwise re-read the file.
        if (!_isDirty(tab)) { tab.content = null; tab.originalContent = null; }
        tab.mode = mode;
    } else {
        // Opening straight into the editor is an intent to work on the file.
        tab = _newTab({ filepath, line: line || 0, mode, canDiff: !!opts.diff, pinned: mode === 'edit' });
        _addTab(tab);
    }
    if (tab.mode === 'diff' && !tab.canDiff) tab.mode = 'preview';
    _activate(tab);
}

/** Open a chat chip's image (its thumbnail was clicked, #5028, #5032) in a tab: `label` is the chip's name, `path` the server-relative route that serves the image
 *  (the session's attachment, or an upload), `server` the one it is on. */
export function openChatImageTab(label, path, server, unshare = null) {
    if (!label || !path) return;
    let tab = _tabs.find(t => t.kind === 'chat-image' && t.path === path && t.server === server);
    if (tab) _pin(tab); else { tab = _newTab({ kind: 'chat-image', filepath: label, path, server }); _addTab(tab); }
    tab.unshare = unshare; // the remove-shared-copy action of an image pasted in the dashboard, or null
    _activate(tab);
}

/** Open a Coral artifact (coral:// URI, team artifact or linked URL) in a tab. */
export function openArtifactTab(uri, options = {}) {
    // Artifacts open from a selected team too, with no agent selected.
    if (!state.selectedTeam && (!state.currentSession || state.currentSession.type !== 'live')) return;
    if (!resolveArtifactSource(uri, options)) return;
    const key = options.contentURL || options.externalURL || uri;
    let tab = _tabs.find(t => t.kind === 'artifact' && t.artifactKey === key);
    if (tab) {
        _pin(tab);
    } else {
        tab = _newTab({ kind: 'artifact', filepath: uri, options, artifactKey: key });
        _addTab(tab);
    }
    _activate(tab);
    if (options.phoneFull && _isMobile()) _setPhoneFull(true); // a person's own open (a link, the Artifacts tab) gives an artifact a phone's whole screen, not a strip under the side panel; Back returns to the chat (#6116)
}

function switchTab(tabId) {
    const tab = _tabs.find(t => t.id === tabId);
    if (!tab || tab.id === _activeId) return;
    _activate(tab);
}

function _disposeTab(tab) {
    tab.abort?.abort();
    tab.abort = null;
    tab.renderGen++;
}

// A tab with unsaved edits asks first, in the confirm dialog; the tab is looked up again once
// answered, since tabs can change while the dialog is open.
export function closeTab(tabId) {
    const tab = _tabs.find(t => t.id === tabId);
    if (!tab) return;
    if (_isDirty(tab)) {
        window.showConfirmModal('Discard Changes', `Discard unsaved changes to ${_fileName(tab.filepath)}?`, () => _closeTabNow(tabId));
        return;
    }
    _closeTabNow(tabId);
}

function _closeTabNow(tabId) {
    const idx = _tabs.findIndex(t => t.id === tabId);
    if (idx === -1) return;
    const tab = _tabs[idx];
    if (tab.id === _activeId) { _destroyEditor(); }
    _disposeTab(tab);
    _tabs.splice(idx, 1);

    if (_tabs.length === 0) {
        _activeId = null;
        _renderTabs();
        _renderToolbar(null);
        _body()?.replaceChildren(); // drop frames/media so nothing keeps running in the hidden pane
        _hidePane();
        return;
    }

    if (_activeId === tabId) {
        const next = _tabs[Math.min(idx, _tabs.length - 1)];
        _activeId = next.id;
        _renderTabs();
        _render(next);
    } else {
        _renderTabs();
    }
}

export function closeAllTabs() {
    _destroyEditor();
    _tabs.forEach(_disposeTab);
    _tabs = [];
    _activeId = null;
    _renderTabs();
    _renderToolbar(null);
    _body()?.replaceChildren();
    _hidePane();
}

/* ── Rendering ─────────────────────────────────────────────── */

function _body() {
    return document.getElementById('preview-body');
}

function _destroyEditor() {
    if (_cmView) { _cmView.destroy(); _cmView = null; }
    _fallbackEditor = null;
}

/** Copy edits out of the live editor into the tab before it is torn down. */
function _captureEditor() {
    const tab = _activeTab();
    if (!tab || tab.kind !== 'file' || tab.mode !== 'edit') return;
    if (_cmView) tab.content = _cmView.state.doc.toString();
    else if (_fallbackEditor) tab.content = _fallbackEditor.value;
}

function _setBody(html) {
    const body = _body();
    if (body) body.innerHTML = html;
}

// The comments of a Coral artifact on show in the pane (#5311): the viewer's own component (artifact_comments.js), one instance for the pane, opened for the artifact tab that is showing and closed for
// any other tab, or none. Beside the document when the pane is 720 px wide or more (maximised, or a column), a bottom sheet over it below that, as on a phone.
const SHEET_BELOW = 720; // px: the same number as the container query in artifact-comments.css
const ARTIFACT_URI = /^coral:\/\/artifacts\/([0-9a-f]{64})$/;
let _comments = null;
async function _commentsApi() {
    if (_comments) return _comments;
    const { attachComments } = await importOnce('./artifact_comments.js');
    const pane = document.getElementById('preview-pane'), main = document.getElementById('preview-main'), body = _body();
    if (_comments || !pane || !main || !body) return _comments;
    return (_comments = attachComments(pane, body, main, { toggleHost: true, observe: main, sheet: () => main.clientWidth < SHEET_BELOW, shown: () => pane.style.display !== 'none' }));
}

async function _render(tab) {
    const body = _body();
    if (!body) return;
    _destroyEditor();
    const gen = ++tab.renderGen;
    const current = () => _activeId === tab.id && tab.renderGen === gen;
    _renderToolbar(tab);
    const hex = tab.kind === 'artifact' ? (ARTIFACT_URI.exec(tab.filepath || '') || [])[1] : '';
    if (!hex) _comments?.close(); // this tab has no comments (a file, an image, a linked page)

    if (tab.kind === 'artifact') {
        tab.abort?.abort();
        tab.abort = new AbortController();
        let comments = null;
        if (hex) {
            try {
                comments = await _commentsApi();
                const { commentBoard } = await importOnce('./artifact_comments.js');
                if (comments && current()) {
                    comments.open(hex, tab.options && tab.options.server, commentBoard(tab.options));
                    document.getElementById('preview-toolbar')?.append(comments.toggle);
                } else comments = null;
            } catch (e) { comments = null; console.error('[coral] comments', e); } // the document is shown without them
        }
        await renderArtifact(body, tab.filepath, tab.options, {
            signal: tab.abort.signal,
            isStale: () => !current(),
            // An artifact is a file an agent wrote: its Markdown uses the strict renderer (raw HTML shown as text, #3540).
            renderContent: (el, content, path) => {
                if (getLangFromPath(path) === 'markdown') (el.innerHTML = `<div class="preview-pane-markdown">${renderMarkdownStrict(content)}</div>`, highlightCodeIn(el), keepFragmentsInside(el));
                else _renderContentView(el, content, path, 0, false);
                if (comments && current()) comments.docRendered(content, true); // text the comments apply to (#5311)
            },
            openEntry: (entry) => openArtifactTab(entry.uri, { filename: entry.name, mediaType: entry.media_type }),
            setMeta: ({ title }) => { tab.title = title; _renderTabs(); },
        });
        return;
    }

    if (tab.kind === 'chat-image') {
        const url = serverUrl(tab.path, tab.server);
        const missing = 'Image not found';
        renderImagePanes(body, [isCloud() ? { load: () => fetchImageSrc(url, { name: tab.filepath }), missing } : { url, missing }]);
        return;
    }

    const binaryKind = isImagePath(tab.filepath) ? 'image' : isMediaPath(tab.filepath) ? 'media' : null;
    if (binaryKind) {
        const render = binaryKind === 'image' ? renderImagePanes : renderMediaPanes;
        // Through the hub the URL must not carry a cache-buster (the machine's route table does not know `t`), so
        // there the bytes are fetched with cache: 'no-store' and shown from a blob:/data: URL (image_source.js).
        const cloud = isCloud();
        const t = Date.now(); // the file may have changed since the last look
        const pane = (label, endpoint, missing) => {
            const url = _fileUrl(endpoint, tab.filepath, cloud ? { raw: 1 } : { raw: 1, t });
            return cloud && binaryKind === 'image'
                ? { label, load: () => fetchImageSrc(url, { name: tab.filepath }), missing }
                : { label, url, missing };
        };
        if (tab.mode === 'diff') {
            render(body, [pane('Before', 'file-original', 'New file'), pane('After', 'file-content', 'Deleted')]);
        } else {
            render(body, [pane(undefined, 'file-content', binaryKind === 'image' ? 'Image not found' : 'Media not found')]);
        }
        return;
    }

    if (tab.content === null) {
        _setBody('<div class="preview-pane-loading">Loading…</div>');
        if (!agentName()) return;
        try {
            const resp = await retryOnce(() => fetch(_fileUrl('file-content', tab.filepath)));
            const data = await resp.json();
            if (!current()) return;
            if (data.error) { _setBody(`<div class="preview-pane-error">${escapeHtml(data.error)}</div>`); return; }
            tab.content = data.content || '';
            tab.savedContent = tab.content;
        } catch {
            if (current()) _setBody('<div class="preview-pane-error">Failed to load file</div>');
            return;
        }
    }

    if (tab.mode === 'edit' || tab.mode === 'diff') {
        // CodeMirror loads on first use (#3083). A failed load shows Retry; the
        // tab's unsaved text stays on tab.content.
        if (!window.CoralCM) {
            _setBody('');
            const notice = showVendorLoading(body, 'the editor');
            try {
                await loadCodeMirror();
            } catch (e) {
                console.error('[coral]', e);
                notice.remove();
                if (current()) showVendorLoadError(body, 'The editor', () => { if (_activeId === tab.id) _render(tab); });
                return;
            }
            notice.remove();
            if (!current()) return;
        }
    }
    if (tab.mode === 'edit') {
        _mountEditor(tab, body);
    } else if (tab.mode === 'diff') {
        await _mountDiff(tab, body, current);
    } else {
        _renderContentView(body, tab.content, tab.filepath, tab.line, true);
    }
}

/** The element an editor mounts into. CodeMirror writes its theme into <style> elements, which the hub's CSP
 *  (style-src 'self') blocks; inside a shadow root it adopts a constructable stylesheet instead, which the CSP does
 *  not govern. So in cloud mode the editor lives in a shadow root and the page rules that style it (the same ones
 *  agentic.css/output.css hold for local mode) come from a stylesheet loaded from /static: no inline style anywhere. */
function _cmHost(body) {
    body.replaceChildren();
    const host = document.createElement('div');
    host.className = 'inline-preview-cm preview-pane-cm';
    body.appendChild(host);
    if (!isCloud() || !host.attachShadow) return host;
    const root = host.attachShadow({ mode: 'open' });
    const link = document.createElement('link');
    link.rel = 'stylesheet';
    link.href = new URL('./css/cm_shadow.css', import.meta.url).href; // inherits a versioned static prefix
    const mount = document.createElement('div');
    mount.className = 'cm-shadow-mount';
    root.append(link, mount);
    return mount;
}

function _mountEditor(tab, body) {
    const cm = getCm();
    if (cm) {
        try {
            const host = _cmHost(body);
            const extensions = [
                cm.basicSetup, cm.oneDark, cm.search(), cm.EditorView.lineWrapping,
                cm.EditorView.theme({ '&': { height: '100%' }, '.cm-scroller': { overflow: 'auto' } }),
                cm.EditorView.updateListener.of(u => {
                    if (!u.docChanged) return;
                    const wasDirty = _isDirty(tab);
                    tab.content = u.state.doc.toString();
                    if (_isDirty(tab)) tab.pinned = true;
                    if (wasDirty !== _isDirty(tab) || !tab.pinned) _renderTabs();
                }),
            ];
            const langExt = getLangExtension(cm, getLangFromPath(tab.filepath));
            if (langExt) extensions.push(langExt);
            _cmView = new cm.EditorView({ state: cm.EditorState.create({ doc: tab.content, extensions }), parent: host });
            if (tab.line > 0) {
                const doc = _cmView.state.doc;
                const lineObj = doc.line(Math.min(Math.max(1, tab.line), doc.lines));
                _cmView.dispatch({ selection: { anchor: lineObj.from }, scrollIntoView: true });
            }
            return;
        } catch (e) {
            console.error('[coral] CodeMirror editor creation failed:', e);
            _destroyEditor();
        }
    }
    body.innerHTML = `<textarea class="inline-preview-fallback-editor">${escapeHtml(tab.content)}</textarea>`;
    _fallbackEditor = body.querySelector('textarea');
    _fallbackEditor.addEventListener('input', () => {
        const wasDirty = _isDirty(tab);
        tab.content = _fallbackEditor.value;
        if (_isDirty(tab)) tab.pinned = true;
        if (wasDirty !== _isDirty(tab)) _renderTabs();
    });
}

async function _mountDiff(tab, body, current) {
    if (tab.originalContent === null) {
        _setBody('<div class="preview-pane-loading">Loading…</div>');
        try {
            const resp = await retryOnce(() => fetch(_fileUrl('file-original', tab.filepath)));
            const data = await resp.json();
            if (!current()) return;
            tab.originalContent = data.error ? '' : (data.content || '');
        } catch {
            if (current()) _setBody('<div class="preview-pane-error">Failed to load diff</div>');
            return;
        }
    }
    tab.hasDiff = tab.originalContent !== tab.content;
    const cm = tab.hasDiff ? getCm() : null;
    if (cm) {
        try {
            const host = _cmHost(body);
            const extensions = [
                cm.basicSetup, cm.oneDark, cm.EditorView.lineWrapping,
                cm.EditorView.editable.of(false), cm.EditorState.readOnly.of(true),
                cm.EditorView.theme({ '&': { height: '100%' }, '.cm-scroller': { overflow: 'auto' } }),
                // diffConfig: the merge view's own scanLimit would otherwise return one chunk.
                // mergeControls off: the view is read-only, so its Accept and Reject buttons would do nothing when clicked.
                cm.unifiedMergeView({ original: cm.Text.of(tab.originalContent.split('\n')), diffConfig: DIFF_CONFIG, mergeControls: false }),
            ];
            const langExt = getLangExtension(cm, getLangFromPath(tab.filepath));
            if (langExt) extensions.push(langExt);
            _cmView = new cm.EditorView({ state: cm.EditorState.create({ doc: tab.content, extensions }), parent: host });
            return;
        } catch (e) {
            console.error('[coral] CodeMirror merge view creation failed:', e);
            _destroyEditor();
        }
    }
    _renderContentView(body, tab.content, tab.filepath, tab.line, false);
}

/** Rendered HTML (sandboxed), formatted Markdown, or highlighted source. */
function _renderContentView(body, content, filepath, line, interpretHtml) {
    if (interpretHtml && /\.html?$/i.test(filepath)) {
        _renderSandboxedPreview(body, content, filepath);
        return;
    }

    const lang = getLangFromPath(filepath);

    if (lang === 'markdown' && typeof marked !== 'undefined') {
        const html = marked.parse(content);
        // A workspace file can be agent-written: its links follow the file policy (#3542).
        const safe = sanitizeHtml(html, DOMPURIFY_CONFIG, { links: 'file' });
        body.innerHTML = `<div class="preview-pane-markdown">${safe}</div>`;
        keepFragmentsInside(body);
        if (line > 0) _scrollToLine(body, content, line);
        return;
    }

    body.innerHTML = `<pre class="preview-pane-code"><code class="language-${lang}">${escapeHtml(content)}</code></pre>`;
    // Plain first; coloured once highlight.js has loaded (plain if it cannot).
    const block = body.querySelector('pre code');
    if (block) loadHighlight().then(h => { if (block.isConnected) h.highlightElement(block); }, () => {});
    if (line > 0) _scrollToLine(body, content, line);
}

function _renderSandboxedPreview(body, content, filepath) {
    const frame = document.createElement('iframe');
    frame.className = 'preview-pane-iframe';
    frame.title = `Preview of ${_fileName(filepath)}`;
    frame.setAttribute('sandbox', '');
    frame.referrerPolicy = 'no-referrer';
    frame.srcdoc = `<!doctype html><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; base-uri 'none'; form-action 'none'">${content}`;
    body.replaceChildren(frame);
}

function _scrollToLine(container, content, line) {
    if (!line || line <= 1) return;
    requestAnimationFrame(() => {
        const total = content.split('\n').length;
        if (total <= 1) return;
        const pre = container.querySelector('pre.preview-pane-code');
        if (pre) {
            const approx = pre.scrollHeight / total;
            container.scrollTop = Math.max(0, (line - 1) * approx - container.clientHeight / 3);
        } else {
            const ratio = (line - 1) / total;
            container.scrollTop = Math.max(0, ratio * container.scrollHeight - container.clientHeight / 3);
        }
    });
}

/* ── Modes & saving ────────────────────────────────────────── */

function setMode(tab, mode) {
    if (tab.mode === mode) return;
    if (mode === 'diff' && !tab.canDiff) return;
    _captureEditor();
    tab.mode = mode;
    if (mode === 'edit') tab.pinned = true;
    _renderTabs();
    _render(tab);
}

async function saveTab(tab, btn) {
    _captureEditor();
    if (!agentName() || tab.content === null) return;
    const content = tab.content;
    if (btn) { btn.textContent = 'Saving...'; btn.disabled = true; }
    try {
        const resp = await fetch(_fileUrl('file-content', tab.filepath), {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ content }),
        });
        const data = await resp.json();
        if (data.error) {
            window.showAlertModal?.('Save Failed', `Error saving: ${data.error}`);
        } else {
            tab.savedContent = content;
            _renderTabs();
            showToast('Saved');
        }
    } catch (e) {
        window.showAlertModal?.('Save Failed', `Failed to save: ${e.message}`);
    } finally {
        const saveBtn = document.querySelector('#preview-toolbar [data-action="save"]');
        if (saveBtn) { saveBtn.textContent = 'Save'; saveBtn.disabled = false; }
    }
}

/**
 * Switch the pane to another agent's tabs. Each agent keeps its own tabs (and
 * unsaved edits); re-selecting the current agent leaves the pane untouched.
 */
export function setPreviewSession(key) {
    key = key || null;
    if (key === _sessionKey) return;
    _setMaximised(false); // another agent's page opens with its Chat
    _setPhoneFull(false);
    _captureEditor();
    _destroyEditor();
    _tabs.forEach(_disposeTab);
    if (_sessionKey !== null) _stash.set(_sessionKey, { tabs: _tabs, activeId: _activeId });
    const saved = key !== null ? _stash.get(key) : null;
    _tabs = saved ? saved.tabs : [];
    _activeId = saved && _tabs.some(t => t.id === saved.activeId) ? saved.activeId : (_tabs[0]?.id ?? null);
    _sessionKey = key;
    // Files may have changed while this agent was in the background.
    for (const t of _tabs) if (!_isDirty(t)) { t.content = null; t.originalContent = null; }
    _renderTabs();
    const active = _activeTab();
    _renderToolbar(active);
    const body = _body();
    if (body) body.innerHTML = '';
    _placePane();
    if (active) _render(active);
}

