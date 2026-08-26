/* Changed files panel — load and render per-agent file diffs */

import { state } from './state.js';
import {
    CoralLSPClient, fileURIToRelativePath, groupLocations,
    hoverMarkup, lspPositionToOffset,
} from './lsp_client.js';
import { NavigationHistory } from './navigation_history.js';
import { HoverInteractionController } from './hover_intent.js';
import { escapeHtml, showToast } from './utils.js';
import { fetchFileList, fuzzyFilter, fetchDirEntries, getDirBrowseResults } from './file_mention.js';

let _currentFiles = [];
let _searchTimeout = null;
let _renderTimer = null;

/* ── Starred files (persisted in localStorage per session) ── */

function _starKey() {
    const sid = state.currentSession && state.currentSession.session_id;
    return sid ? `coral-starred-files-${sid}` : null;
}

function _getStarredFiles() {
    const key = _starKey();
    if (!key) return [];
    try { return JSON.parse(localStorage.getItem(key) || '[]'); } catch { return []; }
}

function _setStarredFiles(files) {
    const key = _starKey();
    if (key) localStorage.setItem(key, JSON.stringify(files));
}

export function toggleStarFile(filepath) {
    const starred = _getStarredFiles();
    const idx = starred.indexOf(filepath);
    if (idx >= 0) {
        starred.splice(idx, 1);
    } else {
        starred.push(filepath);
    }
    _setStarredFiles(starred);
    renderStarredFiles();
    renderChangedFiles();
    // Re-render search results if visible
    const searchResults = document.getElementById('files-search-results');
    if (searchResults && searchResults.style.display !== 'none') {
        const items = searchResults.querySelectorAll('.file-star-btn');
        const starredSet = new Set(starred);
        items.forEach(btn => {
            const fp = btn.dataset.filepath;
            btn.classList.toggle('starred', starredSet.has(fp));
            btn.textContent = starredSet.has(fp) ? '★' : '☆';
        });
    }
}

export function renderStarredFiles() {
    const container = document.getElementById('starred-files-list');
    if (!container) return;
    const starred = _getStarredFiles();
    if (starred.length === 0) {
        container.style.display = 'none';
        container.innerHTML = '';
        return;
    }
    container.style.display = '';
    container.innerHTML = `<div class="starred-section-label">★ Starred</div>` +
        starred.map(filepath => {
            const { dir, name } = splitPath(filepath);
            const escapedPath = escapeHtml(filepath).replace(/'/g, "\\'");
            const starBtn = `<button class="file-star-btn starred" data-filepath="${escapeHtml(filepath)}" onclick="event.stopPropagation(); toggleStarFile('${escapedPath}')" title="Unstar">★</button>`;
            const copyBtn = `<button class="file-action-btn" onclick="event.stopPropagation(); copyFilePath('${escapedPath}')" title="Copy path"><span class="material-icons">content_copy</span></button>`;
            const previewBtn = `<button class="file-action-btn" onclick="event.stopPropagation(); openFilePreview('${escapedPath}')" title="Preview"><span class="material-icons">visibility</span></button>`;
            const editBtn = `<button class="file-action-btn" onclick="event.stopPropagation(); openFileEdit('${escapedPath}')" title="Edit"><span class="material-icons">edit</span></button>`;
            return `<div class="file-item file-starred" onclick="openFilePreview('${escapedPath}')">
                ${starBtn}
                <div class="file-path-wrap">
                    <span class="file-name">${escapeHtml(name)}</span>
                    ${dir ? `<span class="file-dir">${escapeHtml(dir)}</span>` : ''}
                </div>
                <div class="file-action-btns">${copyBtn}${previewBtn}${editBtn}</div>
            </div>`;
        }).join('');
}

/* ── File search ── */

let _searchResults = [];      // current dropdown results
let _searchSelectedIdx = 0;   // selected index in dropdown

// Toggle between directory browse and fuzzy search modes
export function toggleFileSearchMode() {
    if (!state.settings) state.settings = {};
    const current = state.settings.file_search_mode || 'directory';
    const next = current === 'directory' ? 'fuzzy' : 'directory';
    state.settings.file_search_mode = next;
    localStorage.setItem('coral-file-search-mode', next);

    // Update toggle button icon
    const btn = document.getElementById('file-search-mode-btn');
    if (btn) btn.textContent = next === 'directory' ? '\u{1F4C1}' : '\u{1F50D}';

    // Update placeholder
    const input = document.getElementById('files-search-input');
    if (input) input.placeholder = next === 'directory' ? 'Browse files...' : 'Search files...';
}

export async function searchRepoFiles(query) {
    if (!query || !state.currentSession || state.currentSession.type !== 'live') {
        _hideSearchDropdown();
        return;
    }

    const mode = (state.settings || {}).file_search_mode || 'directory';

    if (mode === 'directory') {
        const browse = await getDirBrowseResults(query);
        const matches = browse ? browse.results : [];
        clearTimeout(_renderTimer);
        _renderTimer = setTimeout(() => _renderSearchDropdown(matches, query), 30);
    } else {
        const files = await fetchFileList();
        const matches = fuzzyFilter(files, query);
        clearTimeout(_renderTimer);
        _renderTimer = setTimeout(() => _renderSearchDropdown(matches, query), 30);
    }
}

function _renderSearchDropdown(files, query) {
    const dropdown = document.getElementById('files-search-dropdown');
    if (!dropdown) return;

    const mode = (state.settings || {}).file_search_mode || 'directory';
    const hasExactMatch = files.some(f => f === query);
    const looksLikePath = query.includes('/') || query.includes('.');

    let html = '';

    // Breadcrumb in directory mode
    if (mode === 'directory' && query.includes('/')) {
        const dirPath = query.slice(0, query.lastIndexOf('/'));
        if (dirPath) {
            html += `<div class="file-mention-breadcrumb">${escapeHtml(dirPath)}/</div>`;
        }
    }

    // Build results list
    _searchResults = [];
    files.slice(0, 50).forEach((filepath, i) => {
        const isDir = filepath.endsWith('/');
        const cls = i === _searchSelectedIdx ? 'file-mention-item selected' : 'file-mention-item';

        if (mode === 'directory' && isDir) {
            const dirName = filepath.replace(/\/$/, '').split('/').pop();
            _searchResults.push({ path: filepath, type: 'dir' });
            html += `<div class="${cls}" data-index="${i}"><span class="file-mention-dir-icon">&#128193;</span>${escapeHtml(dirName)}/</div>`;
        } else {
            _searchResults.push({ path: filepath, type: 'file' });
            html += `<div class="${cls}" data-index="${i}">${escapeHtml(filepath)}</div>`;
        }
    });

    // +create option at the end
    if (query && looksLikePath && !hasExactMatch) {
        const createIdx = _searchResults.length;
        _searchResults.push({ path: query, type: 'create' });
        const cls = createIdx === _searchSelectedIdx ? 'file-mention-item selected' : 'file-mention-item';
        html += `<div class="${cls}" data-index="${createIdx}" style="color:var(--accent)">+ Create ${escapeHtml(query)}</div>`;
    }

    if (_searchResults.length === 0) {
        _hideSearchDropdown();
        return;
    }

    _searchSelectedIdx = Math.min(_searchSelectedIdx, _searchResults.length - 1);
    dropdown.innerHTML = html;
    dropdown.style.display = 'block';

    // Click handlers
    dropdown.querySelectorAll('.file-mention-item').forEach(el => {
        el.addEventListener('mousedown', (e) => {
            e.preventDefault();
            _selectSearchItem(parseInt(el.dataset.index));
        });
    });
}

function _selectSearchItem(index) {
    if (index < 0 || index >= _searchResults.length) return;
    const item = _searchResults[index];

    if (item.type === 'dir') {
        // Navigate into directory
        const input = document.getElementById('files-search-input');
        if (input) {
            input.value = item.path;
            input.focus();
            input.dispatchEvent(new Event('input', { bubbles: true }));
        }
    } else if (item.type === 'create') {
        _hideSearchDropdown();
        window._createFile(item.path);
    } else {
        _hideSearchDropdown();
        openFilePreview(item.path);
    }
}

function _hideSearchDropdown() {
    const dropdown = document.getElementById('files-search-dropdown');
    if (dropdown) dropdown.style.display = 'none';
    _searchResults = [];
    _searchSelectedIdx = 0;
    // Also hide the old inline results container
    const el = document.getElementById('files-search-results');
    if (el) el.style.display = 'none';
}

export function initFileSearch() {
    const input = document.getElementById('files-search-input');
    if (!input || input.dataset.searchBound) return;
    input.dataset.searchBound = '1';

    // Restore persisted search mode
    const savedMode = localStorage.getItem('coral-file-search-mode');
    if (savedMode && state.settings) {
        state.settings.file_search_mode = savedMode;
        const btn = document.getElementById('file-search-mode-btn');
        if (btn) btn.textContent = savedMode === 'directory' ? '\u{1F4C1}' : '\u{1F50D}';
        input.placeholder = savedMode === 'directory' ? 'Browse files...' : 'Search files...';
    }

    function onSearchInput() {
        clearTimeout(_searchTimeout);
        const q = input.value.trim();
        if (!q) {
            _hideSearchDropdown();
            return;
        }
        _searchTimeout = setTimeout(() => searchRepoFiles(q), 200);
    }

    input.addEventListener('input', onSearchInput);
    input.addEventListener('keyup', onSearchInput);
    input.addEventListener('keydown', (e) => {
        const dropdown = document.getElementById('files-search-dropdown');
        const isVisible = dropdown && dropdown.style.display !== 'none';

        if (e.key === 'Escape') {
            if (isVisible) {
                e.preventDefault();
                _hideSearchDropdown();
            } else {
                input.value = '';
            }
            return;
        }

        if (!isVisible || _searchResults.length === 0) return;

        if (e.key === 'ArrowDown') {
            e.preventDefault();
            _searchSelectedIdx = Math.min(_searchSelectedIdx + 1, _searchResults.length - 1);
            _updateSearchSelection();
        } else if (e.key === 'ArrowUp') {
            e.preventDefault();
            _searchSelectedIdx = Math.max(_searchSelectedIdx - 1, 0);
            _updateSearchSelection();
        } else if (e.key === 'Tab' || e.key === 'Enter') {
            e.preventDefault();
            _selectSearchItem(_searchSelectedIdx);
        }
    });

    input.addEventListener('focus', async () => {
        if (!input.value.trim()) {
            // Show file listing on focus — respects user's search mode preference
            if (!state.currentSession || state.currentSession.type !== 'live') return;
            const mode = (state.settings || {}).file_search_mode || 'directory';
            if (mode === 'directory') {
                const browse = await getDirBrowseResults('');
                if (browse && browse.results.length > 0) {
                    _renderSearchDropdown(browse.results.slice(0, 50), '');
                }
            } else {
                const files = await fetchFileList();
                if (files && files.length > 0) {
                    _renderSearchDropdown(files.slice(0, 50), '');
                }
            }
        }
    });

    input.addEventListener('blur', () => {
        setTimeout(_hideSearchDropdown, 200);
    });
}

// Top bar search — prominent file search that renders results in its own dropdown.
export function initTopBarSearch() {
    const input = document.getElementById('topbar-file-search');
    if (!input || input.dataset.searchBound) return;
    input.dataset.searchBound = '1';
    // Clear any browser autofill that snuck in
    input.value = '';

    let debounce;
    function doSearch() {
        clearTimeout(debounce);
        const q = input.value.trim();
        if (!q) {
            _hideTopBarDropdown();
            return;
        }
        debounce = setTimeout(async () => {
            if (!state.currentSession || state.currentSession.type !== 'live') {
                _renderTopBarResults(null, 'Select an agent to search files');
                return;
            }
            const mode = (state.settings || {}).file_search_mode || 'directory';
            if (mode === 'directory') {
                const browse = await getDirBrowseResults(q);
                if (!browse || browse.results.length === 0) {
                    _renderTopBarResults(null, 'No files found');
                    return;
                }
                _renderTopBarResults(browse.results.slice(0, 50));
            } else {
                const files = await fetchFileList();
                if (!files || files.length === 0) {
                    _renderTopBarResults(null, 'No files found');
                    return;
                }
                const matches = fuzzyFilter(files, q);
                _renderTopBarResults(matches);
            }
        }, 200);
    }

    input.addEventListener('input', doSearch);
    input.addEventListener('keyup', doSearch);
    input.addEventListener('focus', () => {
        if (!input.value.trim()) _showWorkingDir();
    });
    input.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') {
            input.value = '';
            _hideTopBarDropdown();
        }
    });
    input.addEventListener('blur', () => setTimeout(_hideTopBarDropdown, 200));
}

async function _showWorkingDir() {
    const dir = state.currentSession?.working_directory;
    if (!dir) {
        _renderTopBarResults(null, 'Select an agent to search files');
        return;
    }
    const mode = (state.settings || {}).file_search_mode || 'directory';
    if (mode === 'directory') {
        const browse = await getDirBrowseResults('');
        if (browse && browse.results.length > 0) {
            _renderTopBarResults(browse.results.slice(0, 20), null, dir);
        } else {
            _renderTopBarResults(null, 'Type to search files...', dir);
        }
    } else {
        const files = await fetchFileList();
        if (files && files.length > 0) {
            _renderTopBarResults(files.slice(0, 20), null, dir);
        } else {
            _renderTopBarResults(null, 'Type to search files...', dir);
        }
    }
}

function _renderTopBarResults(files, message, workingDir) {
    const dropdown = document.getElementById('topbar-search-dropdown');
    if (!dropdown) return;

    // Position fixed dropdown below the search input
    const input = document.getElementById('topbar-file-search');
    if (input) {
        const rect = input.getBoundingClientRect();
        dropdown.style.top = (rect.bottom + 4) + 'px';
        dropdown.style.left = rect.left + 'px';
        dropdown.style.width = Math.max(rect.width, 300) + 'px';
    }

    // Working directory header
    const dir = workingDir || state.currentSession?.working_directory;
    const dirHeader = dir
        ? `<div class="topbar-search-dir" style="padding:6px 12px;font-size:11px;color:var(--text-secondary);border-bottom:1px solid var(--border);font-family:var(--font-mono)"><span class="material-icons" style="font-size:13px;vertical-align:-2px;margin-right:4px">folder</span>${escapeHtml(dir)}</div>`
        : '';

    if (message) {
        dropdown.innerHTML = dirHeader + `<div class="file-mention-item" style="color:var(--text-secondary);cursor:default">${escapeHtml(message)}</div>`;
        dropdown.style.display = 'block';
        return;
    }
    if (!files || files.length === 0) {
        dropdown.innerHTML = dirHeader + '<div class="file-mention-item" style="color:var(--text-secondary);cursor:default">No matches</div>';
        dropdown.style.display = 'block';
        return;
    }
    dropdown.innerHTML = dirHeader + files.slice(0, 20).map(fp => {
        const escaped = escapeHtml(fp).replace(/'/g, "\\'");
        return `<div class="file-mention-item" onmousedown="event.preventDefault(); openFilePreview('${escaped}')">${escapeHtml(fp)}</div>`;
    }).join('');
    dropdown.style.display = 'block';
}

function _hideTopBarDropdown() {
    const dropdown = document.getElementById('topbar-search-dropdown');
    if (dropdown) dropdown.style.display = 'none';
}

export function showTopBarSearch() {
    const el = document.getElementById('top-bar-search');
    if (el) el.style.display = 'flex';
}

export function hideTopBarSearch() {
    const el = document.getElementById('top-bar-search');
    if (el) el.style.display = 'none';
}

function _updateSearchSelection() {
    const dropdown = document.getElementById('files-search-dropdown');
    if (!dropdown) return;
    dropdown.querySelectorAll('.file-mention-item').forEach((el, i) => {
        el.classList.toggle('selected', i === _searchSelectedIdx);
    });
    const selected = dropdown.querySelector('.file-mention-item.selected');
    if (selected) selected.scrollIntoView({ block: 'nearest' });
}

export async function loadChangedFiles(agentName, sessionId) {
    if (!agentName) return;
    try {
        const params = new URLSearchParams();
        const sid = sessionId || (state.currentSession && state.currentSession.session_id);
        if (sid) params.set("session_id", sid);
        const qs = params.toString() ? `?${params}` : "";
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(agentName)}/files${qs}`);
        if (!resp.ok) throw new Error(`files fetch failed: ${resp.status}`);
        const data = await resp.json();
        _currentFiles = data.files || [];
    } catch (e) {
        _currentFiles = [];
    }
    renderStarredFiles();
    renderChangedFiles();
}

export function updateChangedFileCount(count) {
    const el = document.getElementById('files-bar-count');
    if (el) {
        el.textContent = count > 0 ? String(count) : '';
    }
}

export function copyFilePath(filepath) {
    navigator.clipboard.writeText(filepath).then(() => {
        showToast('Path copied');
    });
}

function getStatusLabel(status) {
    const map = {
        'M': 'Modified',
        'A': 'Added',
        'D': 'Deleted',
        'R': 'Renamed',
        'C': 'Copied',
        '??': 'Untracked',
        'AM': 'Added',
        'MM': 'Modified',
        'agent_only': 'Agent edit',
    };
    return map[status] || status;
}

function getStatusClass(status) {
    if (status === 'A' || status === 'AM' || status === '??') return 'file-added';
    if (status === 'D') return 'file-deleted';
    if (status === 'R') return 'file-renamed';
    if (status === 'agent_only') return 'file-added';
    return 'file-modified';
}

function splitPath(filepath) {
    const lastSlash = filepath.lastIndexOf('/');
    if (lastSlash === -1) return { dir: '', name: filepath };
    return { dir: filepath.substring(0, lastSlash + 1), name: filepath.substring(lastSlash + 1) };
}

/* ── Helper: build API query string for the current session ── */

function _apiQs(filepath) {
    const qs = new URLSearchParams({ filepath });
    const sid = state.currentSession && state.currentSession.session_id;
    if (sid) qs.set('session_id', sid);
    return qs;
}

function _agentName() {
    return state.currentSession && state.currentSession.name;
}

/* ── CodeMirror 6 (lazy-loaded) ─────────────────────────────── */

let _cmView = null;       // active EditorView instance (edit mode)
let _cmMergeView = null;  // active merge view instance (diff mode)
let _lspClient = null;
let _hoverTimer = null;
let _hoverAnchor = null;
let _hoverRequestGeneration = 0;
const _hoverInteraction = new HoverInteractionController({
    onDismiss: () => _dismissLSPHoverCard(),
    onReplace: (x, y) => {
        if (!_cmView) return;
        const offset = _cmView.posAtCoords({ x, y });
        if (offset != null) _showLSPHover(offset, x, y);
    },
});
const _navigationHistory = new NavigationHistory(50);

/** Get the CodeMirror module (loaded via IIFE script tag as window.CoralCM). */
function _getCm() {
    if (!window.CoralCM) {
        console.error('[coral] CodeMirror not available — codemirror-bundle.js may not have loaded');
        return null;
    }
    return window.CoralCM;
}

function _getLangExtension(cm, langName) {
    const loaders = {
        javascript: () => cm.javascript(),
        typescript: () => cm.javascript({ typescript: true }),
        jsx:        () => cm.javascript({ jsx: true }),
        tsx:        () => cm.javascript({ jsx: true, typescript: true }),
        python: () => cm.python(), html: () => cm.html(), css: () => cm.css(),
        json: () => cm.json(), markdown: () => cm.markdown(), sql: () => cm.sql(),
        rust: () => cm.rust(), cpp: () => cm.cpp(), c: () => cm.cpp(),
        java: () => cm.java(), go: () => cm.go(), xml: () => cm.xml(), yaml: () => cm.yaml(),
    };
    const loader = loaders[langName];
    if (!loader) return null;
    try { return loader(); } catch { return null; }
}

/** Create an editable CodeMirror editor. Returns false if CM is unavailable. */
function _createCmEditor(container, content, langName) {
    const cm = _getCm();
    if (!cm) return false;

    try {
        const extensions = [
            cm.basicSetup,
            cm.oneDark,
            cm.search(),
            cm.EditorView.lineWrapping,
            cm.EditorView.theme({ '&': { height: '100%' }, '.cm-scroller': { overflow: 'auto' } }),
        ];

        const langExt = _getLangExtension(cm, langName);
        if (langExt) extensions.push(langExt);
        if (cm.EditorView.updateListener) {
            extensions.push(cm.EditorView.updateListener.of(update => {
                if (update.docChanged) {
                    _hideLSPHover();
                    _lspClient?.documentChanged();
                }
            }));
        }
        if (cm.EditorView.domEventHandlers) {
            extensions.push(cm.EditorView.domEventHandlers({
                mousemove: (event, view) => {
                    const card = document.getElementById('lsp-hover-card');
                    if (card) return false;
                    clearTimeout(_hoverTimer);
                    _hoverTimer = setTimeout(() => {
                        _hoverTimer = null;
                        const offset = view.posAtCoords({ x: event.clientX, y: event.clientY });
                        if (offset != null) _showLSPHover(offset, event.clientX, event.clientY);
                    }, 350);
                    return false;
                },
                mouseleave: event => {
                    if (document.getElementById('lsp-hover-card')) return false;
                    _hideLSPHover();
                    return false;
                },
                mousedown: (event, view) => {
                    if (!(event.metaKey || event.ctrlKey)) return false;
                    const offset = view.posAtCoords({ x: event.clientX, y: event.clientY });
                    if (offset != null) { event.preventDefault(); _goToDefinition(offset); return true; }
                    return false;
                },
                keydown: event => {
                    if (event.key === 'Escape') {
                        _hideLSPHover();
                        _closeLSPPanel();
                    }
                    return false;
                },
            }));
        }

        _cmView = new cm.EditorView({
            state: cm.EditorState.create({ doc: content, extensions }),
            parent: container,
        });
        _connectLSP();
        return true;
    } catch (e) {
        console.error('[coral] CodeMirror editor creation failed:', e);
        return false;
    }
}

function _destroyCmEditor() {
    clearTimeout(_hoverTimer);
    _hideLSPHover();
    _lspClient?.close();
    _lspClient = null;
    if (_cmView) {
        _cmView.destroy();
        _cmView = null;
    }
    if (_cmMergeView) {
        _cmMergeView.destroy();
        _cmMergeView = null;
    }
}

/** Create a read-only CodeMirror merge view. Returns false if CM is unavailable. */
function _createCmMergeView(container, originalContent, currentContent, langName) {
    const cm = _getCm();
    if (!cm) return false;

    try {
        const extensions = [
            cm.basicSetup,
            cm.oneDark,
            cm.EditorView.lineWrapping,
            cm.EditorView.editable.of(false),
            cm.EditorState.readOnly.of(true),
            cm.EditorView.theme({ '&': { height: '100%' }, '.cm-scroller': { overflow: 'auto' } }),
            cm.unifiedMergeView({
                original: cm.Text.of(originalContent.split('\n')),
            }),
        ];

        const langExt = _getLangExtension(cm, langName);
        if (langExt) extensions.push(langExt);

        _cmMergeView = new cm.EditorView({
            state: cm.EditorState.create({ doc: currentContent, extensions }),
            parent: container,
        });
        return true;
    } catch (e) {
        console.error('[coral] CodeMirror merge view creation failed:', e);
        return false;
    }
}

function _getCmContent() {
    return _cmView ? _cmView.state.doc.toString() : null;
}

/* ── Inline Preview Pane ───────────────────────────────────── */

let _previewState = null; // { filepath, mode, content, hasDiff, diffText, gen }
let _previewGen = 0;      // generation counter to guard against stale async writes

/** Show inline diff for a file (clicking a file row). */
export function openFileDiff(filepath) {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    _openInlinePane(filepath, 'diff');
}

/** Show inline preview for a file (clicking the preview icon). */
export function openFilePreview(filepath) {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    _openInlinePane(filepath, 'preview');
}

/** Open file directly in edit mode (clicking the edit icon). */
export function openFileEdit(filepath) {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    _openInlinePane(filepath, 'edit');
}

export function openWorkspaceFile(filepath, options = {}) {
    const mode = options.mode || 'edit';
    if (_previewState) {
        _navigationHistory.record({
            filepath: _previewState.filepath,
            mode: _previewState.mode,
            selection: _cmView ? {
                from: _cmView.state.selection.main.from,
                to: _cmView.state.selection.main.to,
            } : null,
            scrollTop: _cmView?.scrollDOM?.scrollTop || 0,
        }, options);
    }
    return _openInlinePane(filepath, mode, options);
}

export function navigateWorkspaceBack() {
    const previous = _navigationHistory.pop();
    if (previous) return _openInlinePane(previous.filepath, previous.mode, { ...previous, recordHistory: false });
}

export function findLSPReferences() {
    if (!_cmView || !_lspClient?.capabilities.references) return;
    _showReferences(_cmView.state.selection.main.head);
}

/** Create a new file via the API and open it in the editor. */
// Exposed on window for onclick in rendered search results.
window._createFile = async function(filePath) {
    if (!filePath) return;
    const s = state.currentSession;
    if (!s || s.type !== 'live') return;

    try {
        const qs = new URLSearchParams({ filepath: filePath, session_id: s.session_id || '' });
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(s.name)}/file-content?${qs}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ content: '' }),
        });
        const data = await resp.json();
        if (data.error) {
            showToast(data.error, true);
            return;
        }
        // Clear search, open the new file in editor, refresh file list
        const searchInput = document.getElementById('files-search-input');
        if (searchInput) searchInput.value = '';
        const searchResults = document.getElementById('files-search-results');
        if (searchResults) searchResults.style.display = 'none';
        openFileEdit(filePath);
        refreshChangedFiles();
    } catch (e) {
        showToast('Failed to create file', true);
    }
};

async function _openInlinePane(filepath, initialView, options = {}) {
    // On mobile, use a full-screen overlay instead of the sidebar pane
    const isMobile = window.innerWidth <= 767;
    let panel;

    if (isMobile) {
        // Remove any existing overlay
        document.querySelectorAll('.mobile-file-preview-overlay').forEach(el => el.remove());
        const overlay = document.createElement('div');
        overlay.className = 'mobile-file-preview-overlay';
        document.body.appendChild(overlay);
        panel = overlay;
    } else {
        panel = document.getElementById('agentic-panel-files');
        if (!panel) return;
        // Switch to files tab if not active
        if (window.switchAgenticTab) window.switchAgenticTab('files', 'top');
    }

    const { name } = splitPath(filepath);
    const gen = ++_previewGen;

    _previewState = {
        filepath, mode: 'preview', content: '', originalContent: null,
        hasDiff: false, gen, selection: options.selection || null,
        restoreScrollTop: options.scrollTop || 0, workingDirectory: '',
    };

    // Render the pane shell
    panel.innerHTML = `
        <div class="inline-preview-header">
            <button class="inline-preview-back" onclick="window._closeInlinePreview()" title="Back to file list">
                <span class="material-icons">arrow_back</span>
            </button>
            <span class="inline-preview-filepath" title="${escapeHtml(filepath)}">${escapeHtml(name)}</span>
            <div class="inline-preview-actions">
                <button class="inline-preview-mode-btn lsp-action" id="lsp-back-btn" onclick="navigateWorkspaceBack()" title="Navigate back" ${_navigationHistory.size ? '' : 'disabled'}><span class="material-icons">history</span></button>
                <button class="inline-preview-mode-btn lsp-action" id="lsp-definition-btn" onclick="goToLSPDefinition()" title="Go to definition" disabled><span class="material-icons">open_in_new</span></button>
                <button class="inline-preview-mode-btn lsp-action" id="lsp-references-btn" onclick="findLSPReferences()" title="Find references" disabled><span class="material-icons">account_tree</span></button>
                <span class="lsp-status" id="lsp-status" title="Language intelligence">unsupported</span>
                <button class="inline-preview-mode-btn file-star-btn ${_getStarredFiles().includes(filepath) ? 'starred' : ''}" id="preview-star-btn" onclick="window._togglePreviewStar()" title="Star">${_getStarredFiles().includes(filepath) ? '★' : '☆'}</button>
                <button class="inline-preview-mode-btn" id="mode-btn-diff" onclick="window._switchMode('diff')" title="Diff"><span class="material-icons">difference</span></button>
                <button class="inline-preview-mode-btn" id="mode-btn-preview" onclick="window._switchMode('preview')" title="Preview"><span class="material-icons">visibility</span></button>
                <button class="inline-preview-mode-btn" id="mode-btn-edit" onclick="window._switchMode('edit')" title="Edit"><span class="material-icons">edit</span></button>
                <button class="inline-preview-save" id="preview-save-btn" onclick="window._savePreviewFile()" style="display:none">Save</button>
            </div>
        </div>
        <div class="inline-preview-body" id="inline-preview-body">
            <div class="inline-preview-loading">Loading...</div>
        </div>
        <div class="inline-preview-cm" id="inline-preview-cm" style="display:none"></div>
    `;

    // Set active mode button and load content
    _updateModeButtons(initialView);
    if (initialView === 'edit') {
        _previewState.mode = 'edit';
        await _prefetchContent(filepath, gen);
        if (!_isStale(gen)) {
            const body = document.getElementById('inline-preview-body');
            const cmContainer = document.getElementById('inline-preview-cm');
            if (body && cmContainer) {
                body.style.display = 'none';
                cmContainer.style.display = 'block';
                const saveBtn = document.getElementById('preview-save-btn');
                if (saveBtn) saveBtn.style.display = '';
                const langName = _getLangFromPath(filepath);
                await _createCmEditor(cmContainer, _previewState.content, langName);
                _restoreEditorLocation();
            }
        }
    } else if (initialView === 'diff') {
        _previewState.mode = 'diff';
        await _loadDiffView(filepath, gen);
    } else {
        _previewState.mode = 'preview';
        await _loadContentView(filepath, gen);
    }
}

/** Check if this async operation is still for the current pane. */
function _isStale(gen) {
    return !_previewState || _previewState.gen !== gen;
}

async function _loadDiffView(filepath, gen) {
    const body = document.getElementById('inline-preview-body');
    if (!body) return;

    const agentName = _agentName();
    if (!agentName) return;

    try {
        // Fetch original and current content in parallel
        const [origResp, curResp] = await Promise.all([
            fetch(`/api/sessions/live/${encodeURIComponent(agentName)}/file-original?${_apiQs(filepath)}`),
            fetch(`/api/sessions/live/${encodeURIComponent(agentName)}/file-content?${_apiQs(filepath)}`),
        ]);

        const origData = await origResp.json();
        const curData = await curResp.json();

        if (_isStale(gen)) return;

        if (curData.error) {
            body.innerHTML = `<div class="inline-preview-error">${escapeHtml(curData.error)}</div>`;
            return;
        }

        const currentContent = curData.content || '';
        const originalContent = origData.error ? '' : (origData.content || '');
        _previewState.content = currentContent;
        _previewState.originalContent = originalContent;
        _previewState.workingDirectory = curData.working_directory || '';

        // If original and current are the same (no changes), show content view
        if (originalContent === currentContent) {
            _renderContentView(body, currentContent, filepath);
            return;
        }

        _previewState.hasDiff = true;

        // Try to show merge view; fall back to plain content if CM unavailable
        const cmContainer = document.getElementById('inline-preview-cm');
        if (cmContainer) {
            const langName = _getLangFromPath(filepath);
            const ok = await _createCmMergeView(cmContainer, originalContent, currentContent, langName);
            if (ok) {
                body.style.display = 'none';
                cmContainer.style.display = 'block';
            } else {
                // Fallback: show current content with syntax highlighting
                _renderContentView(body, currentContent, filepath);
            }
        }
    } catch (e) {
        console.error('[coral] _loadDiffView failed for', filepath, e);
        if (_isStale(gen)) return;
        body.innerHTML = '<div class="inline-preview-error">Failed to load diff</div>';
    }
}

/** Render file content with syntax highlighting into the given container. */
function _renderContentView(container, content, filepath) {
    const lang = _getLangFromPath(filepath);
    // Render markdown files as formatted HTML
    if (lang === 'markdown' && typeof marked !== 'undefined') {
        const html = marked.parse(content);
        container.innerHTML = `<div class="notes-rendered" style="padding:12px 14px;overflow-y:auto">${typeof DOMPurify !== 'undefined' ? DOMPurify.sanitize(html) : html}</div>`;
        return;
    }
    const escaped = escapeHtml(content);
    container.innerHTML = `<pre class="inline-preview-code"><code class="language-${lang}">${escaped}</code></pre>`;
    // Apply highlight.js if available
    if (window.hljs) {
        const block = container.querySelector('pre code');
        if (block) window.hljs.highlightElement(block);
    }
}

function _getLangFromPath(fp) {
    const ext = (fp.match(/\.(\w+)$/) || [])[1] || '';
    const map = {
        js: 'javascript', ts: 'typescript', tsx: 'typescript', jsx: 'javascript',
        py: 'python', rb: 'ruby', rs: 'rust', go: 'go', java: 'java',
        sh: 'bash', zsh: 'bash', bash: 'bash', yml: 'yaml', yaml: 'yaml',
        json: 'json', toml: 'toml', css: 'css', scss: 'scss',
        html: 'html', xml: 'xml', sql: 'sql', c: 'c', cpp: 'cpp',
        h: 'c', hpp: 'cpp', cs: 'csharp', swift: 'swift', kt: 'kotlin',
        md: 'markdown',
    };
    return map[ext.toLowerCase()] || ext.toLowerCase() || 'plaintext';
}

async function _loadContentView(filepath, gen) {
    const body = document.getElementById('inline-preview-body');
    if (!body) return;

    const agentName = _agentName();
    if (!agentName) return;

    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(agentName)}/file-content?${_apiQs(filepath)}`);
        const data = await resp.json();

        if (_isStale(gen)) return;

        if (data.error) {
            body.innerHTML = `<div class="inline-preview-error">${escapeHtml(data.error)}</div>`;
            return;
        }

        const content = data.content || '';
        _previewState.content = content;
        _previewState.workingDirectory = data.working_directory || '';

        _renderContentView(body, content, filepath);
    } catch (e) {
        if (_isStale(gen)) return;
        body.innerHTML = '<div class="inline-preview-error">Failed to load file</div>';
    }
}

async function _prefetchContent(filepath, gen) {
    const agentName = _agentName();
    if (!agentName) return;

    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(agentName)}/file-content?${_apiQs(filepath)}`);
        const data = await resp.json();
        if (!_isStale(gen) && !data.error) {
            _previewState.content = data.content || '';
            _previewState.workingDirectory = data.working_directory || '';
        }
    } catch (e) { /* best effort */ }
}

function _updateModeButtons(activeMode) {
    ['diff', 'preview', 'edit'].forEach(m => {
        const btn = document.getElementById(`mode-btn-${m}`);
        if (btn) btn.classList.toggle('active', m === activeMode);
    });
    const saveBtn = document.getElementById('preview-save-btn');
    if (saveBtn) saveBtn.style.display = activeMode === 'edit' ? '' : 'none';
}

/** Toggle star on the currently previewed file. */
window._togglePreviewStar = function() {
    if (!_previewState) return;
    toggleStarFile(_previewState.filepath);
    const btn = document.getElementById('preview-star-btn');
    if (btn) {
        const isStarred = _getStarredFiles().includes(_previewState.filepath);
        btn.textContent = isStarred ? '★' : '☆';
        btn.classList.toggle('starred', isStarred);
    }
};

/** Switch between diff, preview, and edit modes. */
window._switchMode = async function(targetMode) {
    if (!_previewState || _previewState.mode === targetMode) return;

    const body = document.getElementById('inline-preview-body');
    const cmContainer = document.getElementById('inline-preview-cm');
    if (!body || !cmContainer) return;

    // Save content from current editor before destroying
    const cmContent = _getCmContent();
    const fallbackEl = document.getElementById('fallback-editor');
    if (cmContent != null) _previewState.content = cmContent;
    else if (fallbackEl) _previewState.content = fallbackEl.value;
    _destroyCmEditor();

    _previewState.mode = targetMode;
    _updateModeButtons(targetMode);
    const langName = _getLangFromPath(_previewState.filepath);

    if (targetMode === 'edit') {
        body.style.display = 'none';
        cmContainer.style.display = 'block';
        const ok = await _createCmEditor(cmContainer, _previewState.content, langName);
        if (ok) _restoreEditorLocation();
        if (!ok) {
            // Fallback: plain textarea
            cmContainer.style.display = 'none';
            body.style.display = '';
            body.innerHTML = `<textarea class="inline-preview-fallback-editor" id="fallback-editor">${escapeHtml(_previewState.content)}</textarea>`;
        }
    } else if (targetMode === 'diff') {
        if (_previewState.hasDiff && _previewState.originalContent != null) {
            body.style.display = 'none';
            cmContainer.style.display = 'block';
            const ok = await _createCmMergeView(cmContainer, _previewState.originalContent, _previewState.content, langName);
            if (!ok) {
                // Fallback: show plain content
                cmContainer.style.display = 'none';
                body.style.display = '';
                _renderContentView(body, _previewState.content, _previewState.filepath);
            }
        } else {
            cmContainer.style.display = 'none';
            body.style.display = '';
            _renderContentView(body, _previewState.content, _previewState.filepath);
        }
    } else {
        // preview — show syntax-highlighted content
        cmContainer.style.display = 'none';
        body.style.display = '';
        _renderContentView(body, _previewState.content, _previewState.filepath);
    }
};

// Keep backward compat for the edit initial view
window._togglePreviewEdit = async function() {
    if (!_previewState) return;
    await window._switchMode(_previewState.mode === 'edit' ? 'preview' : 'edit');
};

/** Save the file from the editor. */
window._savePreviewFile = async function() {
    if (!_previewState) return;

    const agentName = _agentName();
    if (!agentName) return;

    const saveBtn = document.getElementById('preview-save-btn');
    const fallbackEditor = document.getElementById('fallback-editor');
    const content = _getCmContent() ?? (fallbackEditor ? fallbackEditor.value : _previewState.content);

    if (saveBtn) { saveBtn.textContent = 'Saving...'; saveBtn.disabled = true; }

    try {
        const resp = await fetch(
            `/api/sessions/live/${encodeURIComponent(agentName)}/file-content?${_apiQs(_previewState.filepath)}`,
            {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ content }),
            }
        );
        const data = await resp.json();
        if (data.error) {
            window.showAlertModal?.('Save Failed', `Error saving: ${data.error}`);
        } else {
            _previewState.content = content;
            _lspClient?.didSave();
            if (saveBtn) { saveBtn.textContent = 'Saved!'; setTimeout(() => { saveBtn.textContent = 'Save'; }, 1500); }
        }
    } catch (e) {
        window.showAlertModal?.('Save Failed', `Failed to save: ${e.message}`);
    } finally {
        if (saveBtn) saveBtn.disabled = false;
    }
};

function _setLSPStatus({ status, message = '', capabilities = {} }) {
    const badge = document.getElementById('lsp-status');
    if (badge) {
        badge.className = `lsp-status lsp-status-${status}`;
        badge.textContent = status;
        badge.title = message || `Language intelligence: ${status}`;
        badge.onclick = (status === 'failed' || status === 'unavailable') ? () => _connectLSP(true) : null;
    }
    const definition = document.getElementById('lsp-definition-btn');
    const references = document.getElementById('lsp-references-btn');
    if (definition) definition.disabled = status !== 'ready' || !capabilities.definition;
    if (references) references.disabled = status !== 'ready' || !capabilities.references;
}

async function _connectLSP(retry = false) {
    if (!_cmView || !_previewState?.workingDirectory) return;
    if (_lspClient && !retry) return;
    _lspClient?.close();
    const agentName = _agentName();
    if (!agentName) return;
    const filepath = _previewState.filepath;
    const query = _apiQs(filepath).toString();
    const client = new CoralLSPClient({
        baseURL: `/api/sessions/live/${encodeURIComponent(agentName)}`,
        query,
        filepath,
        worktree: _previewState.workingDirectory,
        language: _getLangFromPath(filepath),
        getText: () => _getCmContent() ?? _previewState?.content ?? '',
        onStatus: status => { if (_lspClient === client) _setLSPStatus(status); },
    });
    _lspClient = client;
    try {
        await client.connect();
    } catch (error) {
        if (_lspClient === client) _setLSPStatus({ status: 'failed', message: error.message });
    }
}

function _restoreEditorLocation() {
    if (!_cmView || !_previewState) return;
    const selection = _previewState.selection;
    if (selection) {
        const text = _cmView.state.doc.toString();
        const from = selection.from && typeof selection.from === 'object'
            ? lspPositionToOffset(text, selection.from) : selection.from;
        const toValue = selection.to ?? selection.from;
        const to = toValue && typeof toValue === 'object'
            ? lspPositionToOffset(text, toValue) : toValue;
        _cmView.dispatch({
            selection: { anchor: Math.max(0, from || 0), head: Math.max(0, to || from || 0) },
            scrollIntoView: true,
        });
    }
    if (_previewState.restoreScrollTop) _cmView.scrollDOM.scrollTop = _previewState.restoreScrollTop;
}

function _hideLSPHover() {
    clearTimeout(_hoverTimer);
    _hoverTimer = null;
    _hoverInteraction.dismissNow();
}

// Grace-period dismissal deliberately leaves a pending replacement hover alone.
// Explicit teardown calls _hideLSPHover first so Escape/editor cleanup cannot
// resurrect the card.
function _dismissLSPHoverCard() {
    _hoverRequestGeneration++;
    _hoverAnchor = null;
    _lspClient?.cancelHover();
    document.getElementById('lsp-hover-card')?.remove();
}

async function _showLSPHover(offset, clientX, clientY) {
    if (!_lspClient?.capabilities.hover) return;
    const generation = ++_hoverRequestGeneration;
    const client = _lspClient;
    _hoverAnchor = { x: clientX, y: clientY };
    const pendingToken = _hoverInteraction.beginPending(
        { x: clientX, y: clientY },
        () => _hideLSPHover(),
    );
    try {
        const result = await client.hover(offset);
        if (generation !== _hoverRequestGeneration || client !== _lspClient) {
            _hoverInteraction.finishPending(pendingToken);
            return;
        }
        const pointer = _hoverInteraction.finishPending(pendingToken);
        const markdown = hoverMarkup(result?.contents);
        if (!markdown || !result) return;
        document.getElementById('lsp-hover-card')?.remove();
        const card = document.createElement('div');
        card.id = 'lsp-hover-card';
        card.className = 'lsp-hover-card';
        card.setAttribute('role', 'region');
        card.setAttribute('aria-label', 'Symbol information');
        const rawHTML = typeof marked !== 'undefined' ? marked.parse(markdown) : escapeHtml(markdown);
        card.innerHTML = typeof DOMPurify !== 'undefined' ? DOMPurify.sanitize(rawHTML) : escapeHtml(markdown);
        document.body.appendChild(card);
        const margin = 8;
        const rect = card.getBoundingClientRect();
        card.style.left = `${Math.max(margin, Math.min(clientX + 12, innerWidth - rect.width - margin))}px`;
        card.style.top = `${Math.max(margin, Math.min(clientY + 18, innerHeight - rect.height - margin))}px`;
        _hoverInteraction.activate({
            anchor: _hoverAnchor,
            card,
            editor: _cmView.dom,
            pointer,
        });
    } catch (error) {
        _hoverInteraction.finishPending(pendingToken);
        if (error.code === 'outside_workspace') showToast(error.message, true);
    }
}

function _locations(result) {
    if (!result) return [];
    return Array.isArray(result) ? result : [result];
}

async function _goToDefinition(offset) {
    if (!_lspClient?.capabilities.definition) return;
    try {
        const locations = _locations(await _lspClient.definition(offset));
        if (!locations.length) { showToast('No definition found'); return; }
        if (locations.length === 1) { _openLSPLocation(locations[0]); return; }
        _showLocationPicker('Definitions', locations);
    } catch (error) { showToast(error.message, true); }
}

export function goToLSPDefinition() {
    if (_cmView) _goToDefinition(_cmView.state.selection.main.head);
}

function _openLSPLocation(location) {
    try {
        const filepath = fileURIToRelativePath(location.uri || location.targetUri, _lspClient?.workspace || _previewState.workingDirectory);
        const range = location.range || location.targetSelectionRange || location.targetRange;
        openWorkspaceFile(filepath, {
            mode: 'edit',
            selection: { from: range.start, to: range.end },
            recordHistory: true,
        });
    } catch (error) { showToast(error.message, true); }
}

function _closeLSPPanel() { document.getElementById('lsp-results-panel')?.remove(); }

function _showLocationPicker(title, locations) {
    _closeLSPPanel();
    const panel = document.createElement('div');
    panel.id = 'lsp-results-panel';
    panel.className = 'lsp-results-panel';
    panel.innerHTML = `<div class="lsp-results-header"><strong>${escapeHtml(title)}</strong><button type="button" aria-label="Close">×</button></div><div class="lsp-results-list"></div>`;
    panel.querySelector('button').onclick = _closeLSPPanel;
    const list = panel.querySelector('.lsp-results-list');
    for (const location of locations) {
        try {
            const filepath = fileURIToRelativePath(location.uri || location.targetUri, _lspClient?.workspace || _previewState.workingDirectory);
            const range = location.range || location.targetSelectionRange || location.targetRange;
            const button = document.createElement('button');
            button.className = 'lsp-result-item';
            button.textContent = `${filepath}:${range.start.line + 1}:${range.start.character + 1}`;
            button.onclick = () => { _closeLSPPanel(); _openLSPLocation(location); };
            list.appendChild(button);
        } catch { /* backend also rejects these; omit defensively */ }
    }
    document.getElementById('inline-preview-body')?.parentElement?.appendChild(panel);
}

async function _showReferences(offset) {
    try {
        const locations = _locations(await _lspClient.references(offset));
        if (!locations.length) { _showLocationPicker('No references found', []); return; }
        const groups = groupLocations(locations, _lspClient?.workspace || _previewState.workingDirectory);
        _closeLSPPanel();
        const panel = document.createElement('div');
        panel.id = 'lsp-results-panel';
        panel.className = 'lsp-results-panel';
        panel.innerHTML = '<div class="lsp-results-header"><strong>References</strong><button type="button" aria-label="Close">×</button></div><div class="lsp-results-list"></div>';
        panel.querySelector('button').onclick = _closeLSPPanel;
        const list = panel.querySelector('.lsp-results-list');
        const previews = await _fetchReferencePreviews([...groups.keys()].slice(0, 20));
        for (const [filepath, items] of groups) {
            const group = document.createElement('section');
            group.className = 'lsp-result-group';
            const heading = document.createElement('h4');
            heading.textContent = filepath;
            group.appendChild(heading);
            for (const item of items.slice(0, 100)) {
                const button = document.createElement('button');
                button.className = 'lsp-result-item';
                const lineText = previews.get(filepath)?.split('\n')[item.range.start.line]?.trim() || '';
                button.textContent = `${item.line}:${item.column}  ${lineText.slice(0, 160)}`;
                const location = locations.find(value =>
                    (value.uri || value.targetUri) &&
                    (value.range || value.targetSelectionRange || value.targetRange) === item.range);
                button.onclick = () => { _closeLSPPanel(); _openLSPLocation(location); };
                group.appendChild(button);
            }
            list.appendChild(group);
        }
        document.getElementById('inline-preview-body')?.parentElement?.appendChild(panel);
    } catch (error) { showToast(error.message, true); }
}

async function _fetchReferencePreviews(filepaths) {
    const previews = new Map();
    await Promise.all(filepaths.map(async filepath => {
        try {
            const response = await fetch(`/api/sessions/live/${encodeURIComponent(_agentName())}/file-content?${_apiQs(filepath)}`);
            const data = await response.json();
            if (!data.error && typeof data.content === 'string') previews.set(filepath, data.content);
        } catch { /* preview is optional */ }
    }));
    return previews;
}

/** Close inline preview and restore the files list. */
window._closeInlinePreview = function() {
    _destroyCmEditor();
    _previewState = null;

    // Remove mobile overlay if present
    document.querySelectorAll('.mobile-file-preview-overlay').forEach(el => el.remove());

    const panel = document.getElementById('agentic-panel-files');
    if (panel) {
        panel.innerHTML = `
            <div class="changed-files-header" id="changed-files-header">
                <div class="files-search-row" style="position:relative">
                    <input type="search" id="files-search-input" class="files-search-input" placeholder="Search or create files..." autocomplete="off">
                    <button class="refresh-files-btn" onclick="refreshChangedFiles()" title="Refresh git diff">&#x21bb;</button>
                    <div id="files-search-dropdown" class="file-mention-dropdown" style="display:none;bottom:auto;top:100%;margin-top:4px;margin-bottom:0;max-height:400px"></div>
                </div>
                <span class="changed-files-title" id="changed-files-title">Loading...</span>
            </div>
            <div id="files-search-results" class="changed-files-list" style="display:none"></div>
            <div id="starred-files-list" class="changed-files-list" style="display:none"></div>
            <div class="changed-files-list" id="changed-files-list">
                <div class="file-empty">Loading...</div>
            </div>
        `;
        initFileSearch();
    }
    if (state.currentSession) {
        loadChangedFiles(state.currentSession.name, state.currentSession.session_id);
    }
};

/* ── Refresh & Render ──────────────────────────────────────── */

const _diffModes = ['branch_point', 'previous_commit', 'main_head'];
const _diffModeLabels = { branch_point: 'vs merge-base', previous_commit: 'vs HEAD~1', main_head: 'vs main' };

export async function setGitDiffMode(mode) {
    if (!_diffModes.includes(mode)) return;

    try {
        await fetch('/api/settings', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ git_diff_mode: mode }),
        });
        state.settings = { ...state.settings, git_diff_mode: mode };
    } catch (e) {
        console.error('Failed to save git diff mode:', e);
        return;
    }

    refreshChangedFiles();
}

export async function toggleGitDiffMode() {
    const current = _getGitDiffMode();
    const idx = _diffModes.indexOf(current);
    const next = _diffModes[(idx + 1) % _diffModes.length];
    setGitDiffMode(next);
}

function _getGitDiffMode() {
    return (state.settings || {}).git_diff_mode || 'branch_point';
}

export async function refreshChangedFiles() {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    const btn = document.querySelector('.refresh-files-btn');
    if (btn) btn.classList.add('refreshing');

    const agentName = state.currentSession.name;
    const sessionId = state.currentSession.session_id;

    try {
        const body = {};
        if (sessionId) body.session_id = sessionId;
        const resp = await fetch(
            `/api/sessions/live/${encodeURIComponent(agentName)}/files/refresh`,
            {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(body),
            }
        );

        if (resp.ok) {
            const data = await resp.json();
            _currentFiles = data.files || [];
            renderStarredFiles();
            renderChangedFiles();
        } else {
            await loadChangedFiles(agentName, sessionId);
        }
    } catch (e) {
        await loadChangedFiles(agentName, sessionId);
    }

    if (btn) setTimeout(() => btn.classList.remove('refreshing'), 300);
}

export function renderChangedFiles() {
    const list = document.getElementById('changed-files-list');
    const titleEl = document.getElementById('changed-files-title');
    const countEl = document.getElementById('files-bar-count');
    if (!list) return;

    const files = _currentFiles.slice().sort((a, b) => a.filepath.localeCompare(b.filepath));

    if (titleEl) {
        const diffMode = _getGitDiffMode();
        const options = _diffModes.map(m =>
            `<option value="${m}"${m === diffMode ? ' selected' : ''}>${escapeHtml(_diffModeLabels[m])}</option>`
        ).join('');
        titleEl.innerHTML = `${files.length} file${files.length !== 1 ? 's' : ''} changed <select class="diff-mode-select" onchange="setGitDiffMode(this.value)" title="Diff comparison mode">${options}</select>`;
    }
    if (countEl) {
        countEl.textContent = files.length > 0 ? String(files.length) : '';
    }

    if (files.length === 0) {
        const emptyOptions = _diffModes.map(m =>
            `<option value="${m}"${m === _getGitDiffMode() ? ' selected' : ''}>${escapeHtml(_diffModeLabels[m])}</option>`
        ).join('');
        list.innerHTML = `<div class="file-empty">No changed files<br><select class="diff-mode-select" onchange="setGitDiffMode(this.value)" style="margin-top:8px" title="Diff comparison mode">${emptyOptions}</select></div>`;
        return;
    }

    const starred = new Set(_getStarredFiles());
    list.innerHTML = files.map((f) => {
        const { dir, name } = splitPath(f.filepath);
        const statusCls = getStatusClass(f.status);
        const statusLabel = getStatusLabel(f.status);
        const adds = f.additions > 0 ? `<span class="file-adds">+${f.additions}</span>` : '';
        const dels = f.deletions > 0 ? `<span class="file-dels">-${f.deletions}</span>` : '';
        const stats = (adds || dels) ? `<span class="file-stats">${adds}${dels}</span>` : '';
        const statusIcon = f.status === 'agent_only' ? '\u270E' : f.status === '??' ? '?' : f.status === 'A' || f.status === 'AM' ? '+' : f.status === 'D' ? '-' : '~';
        const escapedPath = escapeHtml(f.filepath).replace(/'/g, "\\'");
        const isStarred = starred.has(f.filepath);
        const isAgentOnly = f.status === 'agent_only';
        const agentOnlyCls = isAgentOnly ? ' file-agent-only' : '';
        const agentsHtml = f.agents && f.agents.length > 0
            ? `<span class="file-agents">${escapeHtml(f.agents.map(a => a.name).join(', '))}</span>`
            : '';
        const starBtn = `<button class="file-star-btn ${isStarred ? 'starred' : ''}" data-filepath="${escapeHtml(f.filepath)}" onclick="event.stopPropagation(); toggleStarFile('${escapedPath}')" title="${isStarred ? 'Unstar' : 'Star'}">${isStarred ? '★' : '☆'}</button>`;
        const copyBtn = `<button class="file-action-btn" onclick="event.stopPropagation(); copyFilePath('${escapedPath}')" title="Copy path"><span class="material-icons">content_copy</span></button>`;
        const diffBtn = `<button class="file-action-btn" onclick="event.stopPropagation(); openFileDiff('${escapedPath}')" title="Diff"><span class="material-icons">difference</span></button>`;
        const previewBtn = `<button class="file-action-btn" onclick="event.stopPropagation(); openFilePreview('${escapedPath}')" title="Preview"><span class="material-icons">visibility</span></button>`;
        const editBtn = `<button class="file-action-btn" onclick="event.stopPropagation(); openFileEdit('${escapedPath}')" title="Edit"><span class="material-icons">edit</span></button>`;

        return `<div class="file-item ${statusCls}${agentOnlyCls}" title="${escapeHtml(f.filepath)} (${statusLabel})"
                     onclick="openFileDiff('${escapedPath}')">
            ${starBtn}
            <span class="file-status-icon">${statusIcon}</span>
            <div class="file-path-wrap">
                <span class="file-name">${escapeHtml(name)}</span>
                ${dir ? `<span class="file-dir">${escapeHtml(dir)}</span>` : ''}
                ${agentsHtml}
            </div>
            ${stats}
            <div class="file-action-btns">${copyBtn}${diffBtn}${previewBtn}${editBtn}</div>
        </div>`;
    }).join('');
}
