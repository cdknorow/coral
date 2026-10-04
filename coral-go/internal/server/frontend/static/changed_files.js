/* Changed files panel — load and render per-agent file diffs */

import { state } from './state.js';
import { escapeHtml, escapeAttr, showToast, isImagePath, isMediaPath, renderImagePanes, renderMediaPanes, DOMPURIFY_CONFIG } from './utils.js';
import { fetchFileList, fuzzyFilter, fetchDirEntries, getDirBrowseResults } from './file_mention.js';
import { toggleFileDiff, toggleAllFileDiffs, restoreExpandedDiffs, invalidateDiffs, destroyInlineDiffs, diffExpandIcons } from './diff_view.js';
import { getCm, getLangExtension, getLangFromPath, DIFF_CONFIG } from './cm_util.js';
import { initFilesSourcePicker, syncFilesSourceTeam } from './team_artifacts.js';
import { renderJSONReport } from './artifact_report.js';

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

const fileSearchModeIcons = {
    directory: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 7.5h6l2 2h10v9.5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><path d="M3 9.5v-3a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v1"/></svg>',
    fuzzy: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="10.5" cy="10.5" r="6.5"/><path d="m15.5 15.5 5 5"/></svg>',
};

function setFileSearchModeIcon(btn, mode) {
    if (!btn) return;
    btn.innerHTML = fileSearchModeIcons[mode === 'directory' ? 'directory' : 'fuzzy'];
}

// Toggle between directory browse and fuzzy search modes
export function toggleFileSearchMode() {
    if (!state.settings) state.settings = {};
    const current = state.settings.file_search_mode || 'directory';
    const next = current === 'directory' ? 'fuzzy' : 'directory';
    state.settings.file_search_mode = next;
    localStorage.setItem('coral-file-search-mode', next);

    // Update toggle button icon
    const btn = document.getElementById('file-search-mode-btn');
    setFileSearchModeIcon(btn, next);

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
    initFilesSourcePicker(openTeamArtifactPreview, openFilePreview);
    const input = document.getElementById('files-search-input');
    if (!input || input.dataset.searchBound) return;
    input.dataset.searchBound = '1';

    // Restore persisted search mode
    const savedMode = localStorage.getItem('coral-file-search-mode');
    const mode = savedMode || (state.settings || {}).file_search_mode || 'directory';
    if (state.settings) state.settings.file_search_mode = mode;
    setFileSearchModeIcon(document.getElementById('file-search-mode-btn'), mode);
    input.placeholder = mode === 'directory' ? 'Browse files...' : 'Search files...';

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

/** Create an editable CodeMirror editor. Returns false if CM is unavailable. */
function _createCmEditor(container, content, langName, line) {
    const cm = getCm();
    if (!cm) return false;

    try {
        const extensions = [
            cm.basicSetup,
            cm.oneDark,
            cm.search(),
            cm.EditorView.lineWrapping,
            cm.EditorView.theme({ '&': { height: '100%' }, '.cm-scroller': { overflow: 'auto' } }),
        ];

        const langExt = getLangExtension(cm, langName);
        if (langExt) extensions.push(langExt);

        _cmView = new cm.EditorView({
            state: cm.EditorState.create({ doc: content, extensions }),
            parent: container,
        });
        if (line && line > 0) {
            const doc = _cmView.state.doc;
            const targetLine = Math.min(Math.max(1, line), doc.lines);
            const lineObj = doc.line(targetLine);
            _cmView.dispatch({
                selection: { anchor: lineObj.from },
                scrollIntoView: true,
            });
        }
        return true;
    } catch (e) {
        console.error('[coral] CodeMirror editor creation failed:', e);
        return false;
    }
}

function _destroyCmEditor() {
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
    const cm = getCm();
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
                // Without this the merge view's own scanLimit of 500 returns
                // the whole file as a single chunk. See DIFF_CONFIG.
                diffConfig: DIFF_CONFIG,
            }),
        ];

        const langExt = getLangExtension(cm, langName);
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
let _artifactAbortController = null;
let _artifactManifestStack = [];
const MAX_ARTIFACT_PREVIEW_BYTES = 2 * 1024 * 1024;
const MAX_ARTIFACT_MANIFEST_ENTRIES = 200;
const CORAL_ARTIFACT_URI_RE = /^coral:\/\/artifacts\/([a-f0-9]{64})$/i;

function _artifactSizeLabel(bytes) {
    if (!Number.isFinite(bytes) || bytes < 0) return 'unknown size';
    if (bytes < 1024) return `${bytes} bytes`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function _renderArtifactFallback(container, { filename, type, size, url, reason }) {
    if (!container) return;
    const safeName = escapeHtml(filename || 'artifact');
    const safeType = escapeHtml(type || 'application/octet-stream');
    const safeSize = escapeHtml(_artifactSizeLabel(size));
    const safeReason = escapeHtml(reason);
    container.innerHTML = `<div class="inline-preview-artifact-fallback">
        <strong>${safeName}</strong>
        <span>${safeType} · ${safeSize}</span>
        <p>${safeReason}</p>
        <a href="${escapeAttr(url)}" target="_blank" rel="noopener noreferrer" download>Download artifact</a>
    </div>`;
}

async function _readArtifactText(response, maxBytes, signal) {
    if (!response.body || !response.body.getReader) {
        const text = await response.text();
        if (new TextEncoder().encode(text).byteLength > maxBytes) throw new Error('Artifact is too large to preview');
        return text;
    }
    const reader = response.body.getReader();
    const chunks = [];
    let total = 0;
    try {
        for (;;) {
            if (signal?.aborted) throw new DOMException('Preview cancelled', 'AbortError');
            const { value, done } = await reader.read();
            if (done) break;
            total += value.byteLength;
            if (total > maxBytes) {
                await reader.cancel();
                throw new Error('Artifact is too large to preview');
            }
            chunks.push(value);
        }
    } finally {
        reader.releaseLock();
    }
    const bytes = new Uint8Array(total);
    let offset = 0;
    for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
    return new TextDecoder().decode(bytes);
}

function _parseArtifactManifest(content) {
    let value;
    try { value = JSON.parse(content); } catch { return null; }
    if (!Array.isArray(value) || value.length === 0 || value.length > MAX_ARTIFACT_MANIFEST_ENTRIES) return null;
    const entries = [];
    for (const item of value) {
        if (!item || typeof item !== 'object' || Array.isArray(item)) return null;
        const name = typeof item.name === 'string' ? item.name.trim() : '';
        const uri = typeof item.uri === 'string' ? item.uri.trim() : '';
        const match = CORAL_ARTIFACT_URI_RE.exec(uri);
        const mediaType = typeof item.media_type === 'string' ? item.media_type.trim() : '';
        if (!name || name.length > 160 || !match || (mediaType && mediaType.length > 120)) return null;
        entries.push({ name, uri: `coral://artifacts/${match[1].toLowerCase()}`, media_type: mediaType });
    }
    return { entries, raw: content };
}

function _artifactEntryType(entry) {
    if (entry.media_type) return entry.media_type;
    const name = entry.name.toLowerCase();
    if (/\.md(?:own)?$/.test(name)) return 'Markdown';
    if (/\.json$/.test(name)) return 'JSON';
    if (/\.(?:png|jpe?g|gif|webp|avif|bmp|svg)$/.test(name)) return 'Image';
    if (/\.(?:txt|log|csv|ya?ml|toml|ini|conf|sh|js|ts|go|py|rs|css|html?)$/.test(name)) return 'Text';
    return 'Artifact';
}

function _renderArtifactManifest(body, manifest) {
    if (!body) return;
    const rows = manifest.entries.map((entry, index) => {
        const digest = CORAL_ARTIFACT_URI_RE.exec(entry.uri)[1];
        return `<li class="artifact-manifest-entry">
        <div class="artifact-manifest-main"><button type="button" class="artifact-manifest-open" data-manifest-index="${index}">
            <span class="artifact-manifest-name">${escapeHtml(entry.name)}</span>
            <span class="artifact-manifest-type">${escapeHtml(_artifactEntryType(entry))}</span>
        </button><a class="artifact-manifest-download" href="/api/artifacts/${digest}" download="${escapeAttr(entry.name)}" aria-label="Download ${escapeAttr(entry.name)}">Download</a></div>
        <span class="artifact-manifest-uri">${escapeHtml(entry.uri)}</span>
    </li>`;
    }).join('');
    body.innerHTML = `<div class="artifact-manifest-toolbar"><strong>${manifest.entries.length} file${manifest.entries.length === 1 ? '' : 's'}</strong><div class="artifact-manifest-actions"><button type="button" class="inline-preview-mode-btn artifact-manifest-raw">View raw JSON</button><button type="button" class="inline-preview-mode-btn artifact-manifest-download-raw">Download JSON</button></div></div><ul class="artifact-manifest-list">${rows}</ul>`;
    body.querySelectorAll('.artifact-manifest-open').forEach(button => button.addEventListener('click', () => {
        const entry = manifest.entries[Number(button.dataset.manifestIndex)];
        if (entry) _openArtifactPreview(entry.uri, { fromManifest: true });
    }));
    body.querySelector('.artifact-manifest-raw')?.addEventListener('click', () => _showArtifactManifestRaw());
    body.querySelector('.artifact-manifest-download-raw')?.addEventListener('click', () => _downloadArtifactManifest());
}

function _downloadArtifactManifest() {
    if (!_previewState?.manifest) return;
    const blob = new Blob([_previewState.manifest.raw], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = _previewState.filename || 'manifest.json';
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 0);
}

function _showArtifactManifestRaw() {
    const state = _previewState;
    const body = document.getElementById('inline-preview-body');
    if (!state?.manifest || !body) return;
    _renderContentView(body, state.manifest.raw, state.filename || 'manifest.json');
    const back = document.createElement('button');
    back.type = 'button'; back.className = 'artifact-manifest-back'; back.textContent = 'Back to file collection';
    back.addEventListener('click', () => _renderCurrentArtifactManifest());
    body.prepend(back);
}

function _renderCurrentArtifactManifest() {
    if (!_previewState?.manifest) return;
    const body = document.getElementById('inline-preview-body');
    _renderArtifactManifest(body, _previewState.manifest);
}

function _artifactBack() {
    _artifactAbortController?.abort();
    _artifactAbortController = null;
    if (_artifactManifestStack.length) {
        _previewState = _artifactManifestStack.pop();
        const title = document.querySelector('#agentic-panel-files .inline-preview-filepath, .mobile-file-preview-overlay .inline-preview-filepath');
        if (title) {
            title.textContent = _previewState.manifest ? 'Artifact manifest' : 'Artifact preview';
            title.title = _previewState.filepath || '';
        }
        const body = document.getElementById('inline-preview-body');
        if (body) _renderArtifactState(body);
        return;
    }
    window._closeInlinePreview();
}

function _renderArtifactState(body) {
    if (!_previewState) return;
    body.innerHTML = '';
    if (_previewState.manifest) _renderArtifactManifest(body, _previewState.manifest);
}

/** Show inline preview for a file (clicking the preview icon). */
export function openFilePreview(filepath, line) {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    if (/^coral:\/\/artifacts\/[a-f0-9]{64}$/i.test(filepath || '')) {
        _openArtifactPreview(filepath);
        return;
    }
    _openInlinePane(filepath, 'preview', line);
}

function openTeamArtifactPreview(item) {
    return _openArtifactPreview(item.uri || '', {
        contentURL: item.inline ? item.content_url : null,
        externalURL: item.external_url,
        mediaType: item.media_type,
        filename: item.name,
    });
}

// Called immediately after a session switch, before transcript/file requests.
export function syncFilesViewerSession() {
    syncFilesSourceTeam();
    if (_previewState) window._closeInlinePreview();
    initFilesSourcePicker(openTeamArtifactPreview, openFilePreview);
}

function _renderSandboxedArtifact(body, { url, content, name, gen }) {
    const wrapper = document.createElement('div');
    wrapper.className = 'artifact-linked-preview';
    const note = document.createElement('p');
    note.className = 'artifact-linked-notice';
    note.textContent = url
        ? 'Linked preview: scripts and interactive features are disabled. Some sites block embedding; if the preview stays blank, use Open link.'
        : 'HTML preview: scripts and external resources are disabled. Download the file for the original.';
    const frame = document.createElement('iframe');
    frame.title = `Preview of ${name || 'artifact'}`;
    // Empty sandbox grants no scripts, same-origin privilege, forms, popups,
    // downloads or top-level navigation. External fetching is browser-only.
    frame.setAttribute('sandbox', '');
    frame.referrerPolicy = 'no-referrer';
    wrapper.append(note);
    if (url) {
        const status = document.createElement('div');
        status.className = 'artifact-linked-status';
        status.setAttribute('role', 'status');
        status.textContent = 'Loading linked preview…';
        frame.addEventListener('load', () => { if (!_isStale(gen)) status.remove(); });
        frame.addEventListener('error', () => {
            if (!_isStale(gen)) status.textContent = 'The linked preview could not load. Use Open link to view it.';
        });
        wrapper.append(status);
        frame.src = url;
    } else {
        // Keep authored layout/CSS and embedded images, but do not let HTML
        // artifacts load resources using Coral's origin or credentials.
        frame.srcdoc = `<!doctype html><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; base-uri 'none'; form-action 'none'">${content}`;
    }
    wrapper.append(frame);
    body.replaceChildren(wrapper);
}

/** Preview a Coral-managed artifact in the same Files panel as repository files. */
async function _openArtifactPreview(uri, options = {}) {
    const match = CORAL_ARTIFACT_URI_RE.exec(uri);
    const teamPrefix = `/api/board/${encodeURIComponent(state.currentSession?.board_project || '')}/tasks/`;
    const inlineURL = options.contentURL?.startsWith(teamPrefix) && /\/artifact-content\?/.test(options.contentURL) ? options.contentURL : null;
    let externalURL = null;
    try {
        const parsed = new URL(options.externalURL);
        if (parsed.protocol === 'https:' || parsed.protocol === 'http:') externalURL = parsed.href;
    } catch { /* Only explicit HTTP(S) links can be embedded. */ }
    if (!match && !inlineURL && !externalURL) return;
    const url = externalURL || inlineURL || `/api/artifacts/${match[1]}`;
    const isMobile = window.innerWidth <= 767;
    let panel = isMobile ? document.createElement('div') : document.getElementById('agentic-panel-files');
    if (isMobile) { panel.className = 'mobile-file-preview-overlay'; document.body.appendChild(panel); }
    if (!panel) return;
    _artifactAbortController?.abort();
    if (options.fromManifest && _previewState) {
        _artifactManifestStack.push(_previewState);
        if (_artifactManifestStack.length > 8) _artifactManifestStack.shift();
    } else if (!options.fromManifest) {
        _artifactManifestStack = [];
    }
    _artifactAbortController = new AbortController();
    const signal = _artifactAbortController.signal;
    if (!isMobile && window.switchAgenticTab) window.switchAgenticTab('files', 'top');
    _previewState = { artifact: true, filepath: uri, mode: 'preview', gen: ++_previewGen };
    const gen = _previewState.gen;
    panel.innerHTML = `<div class="inline-preview-header">
        <button class="inline-preview-back" onclick="window._artifactBack()" title="Back" aria-label="Back to files"><span class="material-icons">arrow_back</span></button>
        <span class="inline-preview-filepath" title="${escapeHtml(options.filename || uri)}">${escapeHtml(options.filename || 'Artifact preview')}</span>
        <a class="team-artifacts-action" href="${escapeAttr(url)}" ${externalURL ? 'target="_blank" rel="noopener noreferrer"' : `download="${escapeAttr(options.filename || '')}"`}>${externalURL ? 'Open link' : 'Download'}</a>
    </div><div class="inline-preview-body" id="inline-preview-body"><div class="inline-preview-loading">Loading...</div></div>`;
    const body = panel.querySelector('#inline-preview-body');
    if (externalURL) {
        _renderSandboxedArtifact(body, { url: externalURL, name: options.filename, gen });
        _artifactAbortController = null;
        return;
    }
    try {
        const resp = await fetch(url, { signal });
        if (!resp.ok) throw new Error(`Artifact unavailable (${resp.status})`);
        const type = (resp.headers.get('Content-Type') || 'application/octet-stream').split(';')[0].toLowerCase();
        // Scoped inline content is deliberately served as text/plain. Its
        // declared artifact media type still selects HTML or Markdown rendering.
        const declaredType = (options.mediaType || '').split(';')[0].trim().toLowerCase();
        const isHTML = type === 'text/html' || type === 'application/xhtml+xml' || declaredType === 'text/html' || declaredType === 'application/xhtml+xml';
        const disposition = resp.headers.get('Content-Disposition') || '';
        const filename = options.filename || (disposition.match(/filename="([^"]+)"/i) || disposition.match(/filename=([^;]+)/i) || [])[1] || '';
        const isMarkdown = [type, declaredType].some(value => value === 'text/markdown' || value === 'text/x-markdown') || /\.(?:md|markdown|mdown)$/i.test(filename);
        const isJSON = [type, declaredType].some(value => value === 'application/json' || value.endsWith('+json')) || /\.json$/i.test(filename);
        const artifactPath = filename || uri;
        const sizeHeader = Number(resp.headers.get('Content-Length'));
        const size = Number.isFinite(sizeHeader) && sizeHeader >= 0 ? sizeHeader : -1;
        if (_isStale(gen)) return;
        if (type.startsWith('audio/') || type.startsWith('video/')) {
            const media = document.createElement(type.startsWith('video/') ? 'video' : 'audio');
            media.controls = true; media.autoplay = false; media.preload = 'metadata';
            media.src = resp.url; media.className = 'artifact-preview-media';
            body.replaceChildren(media);
        } else if (type.startsWith('image/')) {
            renderImagePanes(body, [{ url: resp.url, missing: 'Artifact unavailable' }]);
        } else if (!isHTML && !isMarkdown && !isJSON && !type.startsWith('text/') && !type.includes('json') && !type.includes('xml') && !/\.(?:md|markdown|txt|log|json|xml|csv|ya?ml|toml|ini|conf|sh|js|ts|go|py|rs|css|html?)$/i.test(filename)) {
            _renderArtifactFallback(body, { filename, type, size, url: resp.url, reason: 'This binary artifact is not rendered inline.' });
        } else if (size > MAX_ARTIFACT_PREVIEW_BYTES) {
            _renderArtifactFallback(body, { filename, type, size, url: resp.url, reason: `Preview is limited to ${_artifactSizeLabel(MAX_ARTIFACT_PREVIEW_BYTES)}.` });
        } else {
            // Render Markdown artifacts with the same formatted view used by
            // repository .md files.
            const content = await _readArtifactText(resp, MAX_ARTIFACT_PREVIEW_BYTES, signal);
            if (_isStale(gen)) return;
            if (isHTML) {
                _renderSandboxedArtifact(body, { content, name: filename, gen });
                return;
            }
            const manifest = isJSON ? _parseArtifactManifest(content) : null;
            if (manifest) {
                _previewState.manifest = manifest;
                _previewState.filename = filename || 'manifest.json';
                const title = panel.querySelector('.inline-preview-filepath');
                if (title) { title.textContent = 'Artifact manifest'; title.title = _previewState.filename; }
                _renderArtifactManifest(body, manifest);
            } else if (!isMarkdown && isJSON && renderJSONReport(body, content, filename)) {
                // Recognized reports keep their original bytes in the raw view.
            } else {
                // The shared renderer chooses a language from the extension;
                // artifact display names such as acceptance_report have none.
                _renderContentView(body, content, isMarkdown ? 'artifact.md' : artifactPath);
            }
        }
    } catch (error) {
        if (error?.name === 'AbortError' || _isStale(gen)) return;
        body.innerHTML = `<div class="inline-preview-error">${escapeHtml(error.message)}</div>`;
    } finally {
        if (_artifactAbortController?.signal === signal) _artifactAbortController = null;
    }
}

/** Open file directly in edit mode (clicking the edit icon). */
export function openFileEdit(filepath, line) {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    _openInlinePane(filepath, 'edit', line);
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

async function _openInlinePane(filepath, initialView, line) {
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
        const agenticState = document.getElementById('agentic-state');
        if (agenticState && agenticState.classList.contains('collapsed')) {
            agenticState.classList.remove('collapsed');
            const btn = document.getElementById('agentic-collapse-btn');
            if (btn) btn.classList.remove('collapsed');
        }
        // Switch to files tab if not active
        if (window.switchAgenticTab) window.switchAgenticTab('files', 'top');
    }

    const { name } = splitPath(filepath);
    const gen = ++_previewGen;

    _previewState = { filepath, line: line || 0, mode: initialView, content: '', originalContent: null, hasDiff: false, gen };

    // Render the pane shell
    panel.innerHTML = `
        <div class="inline-preview-header">
            <button class="inline-preview-back" onclick="window._closeInlinePreview()" title="Back to file list">
                <span class="material-icons">arrow_back</span>
            </button>
            <span class="inline-preview-filepath" title="${escapeHtml(filepath)}">${escapeHtml(name)}</span>
            <div class="inline-preview-actions">
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

    // Images are shown as pictures (preview, or before/after in diff mode)
    // and cannot be edited.
    if (isImagePath(filepath)) {
        _previewState.isImage = true;
        const editBtn = document.getElementById('mode-btn-edit');
        if (editBtn) editBtn.style.display = 'none';
        _updateModeButtons('preview');
        _renderImagePreview();
        return;
    }

    // Audio and video use native browser playback controls and cannot be edited.
    if (isMediaPath(filepath)) {
        _previewState.isMedia = true;
        const editBtn = document.getElementById('mode-btn-edit');
        if (editBtn) editBtn.style.display = 'none';
        _updateModeButtons('preview');
        _renderMediaPreview();
        return;
    }

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
                const langName = getLangFromPath(filepath);
                await _createCmEditor(cmContainer, _previewState.content, langName, line);
            }
        }
    } else {
        _previewState.mode = 'preview';
        await _loadContentView(filepath, gen, line);
    }
}

/** URL of a file's bytes for an <img>; `endpoint` is file-content (the
 *  working tree) or file-original (the diff base). */
function _rawImageUrl(endpoint, filepath) {
    const qs = _apiQs(filepath);
    qs.set('raw', '1');
    qs.set('t', Date.now()); // the image may have changed since the last look
    return `/api/sessions/live/${encodeURIComponent(_agentName())}/${endpoint}?${qs}`;
}

function _renderMediaPreview() {
    const body = document.getElementById('inline-preview-body');
    if (!body || !_previewState) return;
    renderMediaPanes(body, [{ url: _rawImageUrl('file-content', _previewState.filepath), missing: 'File not found' }]);
}

function _renderMediaDiff() {
    const body = document.getElementById('inline-preview-body');
    if (!body || !_previewState) return;
    const fp = _previewState.filepath;
    renderMediaPanes(body, [
        { label: 'Before', url: _rawImageUrl('file-original', fp), missing: 'New file' },
        { label: 'After', url: _rawImageUrl('file-content', fp), missing: 'Deleted' },
    ]);
}

function _renderImagePreview() {
    const body = document.getElementById('inline-preview-body');
    if (!body || !_previewState) return;
    renderImagePanes(body, [{ url: _rawImageUrl('file-content', _previewState.filepath), missing: 'File not found' }]);
}

function _renderImageDiff() {
    const body = document.getElementById('inline-preview-body');
    if (!body || !_previewState) return;
    const fp = _previewState.filepath;
    renderImagePanes(body, [
        { label: 'Before', url: _rawImageUrl('file-original', fp), missing: 'New file' },
        { label: 'After', url: _rawImageUrl('file-content', fp), missing: 'Deleted' },
    ]);
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

        // If original and current are the same (no changes), show content view
        if (originalContent === currentContent) {
            _renderContentView(body, currentContent, filepath);
            return;
        }

        _previewState.hasDiff = true;

        // Try to show merge view; fall back to plain content if CM unavailable
        const cmContainer = document.getElementById('inline-preview-cm');
        if (cmContainer) {
            const langName = getLangFromPath(filepath);
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
function _renderContentView(container, content, filepath, line) {
    const lang = getLangFromPath(filepath);
    // Render markdown files as formatted HTML
    if (lang === 'markdown' && typeof marked !== 'undefined') {
        const html = marked.parse(content);
        container.innerHTML = `<div class="notes-rendered" style="padding:12px 14px;overflow-y:auto">${typeof DOMPurify !== 'undefined' ? DOMPurify.sanitize(html, DOMPURIFY_CONFIG) : html}</div>`;
        if (line && line > 0) _scrollToLine(container, content, line);
        return;
    }
    const escaped = escapeHtml(content);
    container.innerHTML = `<pre class="inline-preview-code"><code class="language-${lang}">${escaped}</code></pre>`;
    // Apply highlight.js if available
    if (window.hljs) {
        const block = container.querySelector('pre code');
        if (block) window.hljs.highlightElement(block);
    }
    if (line && line > 0) {
        _scrollToLine(container, content, line);
    }
}

function _scrollToLine(container, content, line) {
    if (!line || line <= 1) return;
    const doScroll = () => {
        const lines = content.split('\n');
        const totalLines = lines.length;
        if (totalLines <= 1) return;
        const pre = container.querySelector('pre.inline-preview-code');
        if (pre) {
            const approxLineHeight = pre.scrollHeight / totalLines;
            const targetScroll = Math.max(0, (line - 1) * approxLineHeight - (container.clientHeight / 3));
            container.scrollTop = targetScroll;
        } else {
            const ratio = (line - 1) / totalLines;
            container.scrollTop = Math.max(0, ratio * container.scrollHeight - (container.clientHeight / 3));
        }
    };
    requestAnimationFrame(() => {
        doScroll();
        setTimeout(doScroll, 50);
    });
}

async function _loadContentView(filepath, gen, line) {
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

        _renderContentView(body, content, filepath, line);
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
        if (!_isStale(gen) && !data.error) _previewState.content = data.content || '';
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

    if (_previewState.isImage || _previewState.isMedia) {
        if (targetMode === 'edit') return;
        _previewState.mode = targetMode;
        _updateModeButtons(targetMode);
        if (_previewState.isMedia) {
            if (targetMode === 'diff') _renderMediaDiff();
            else _renderMediaPreview();
        } else if (targetMode === 'diff') _renderImageDiff();
        else _renderImagePreview();
        return;
    }

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
    const langName = getLangFromPath(_previewState.filepath);

    if (targetMode === 'edit') {
        body.style.display = 'none';
        cmContainer.style.display = 'block';
        const ok = await _createCmEditor(cmContainer, _previewState.content, langName, _previewState.line);
        if (!ok) {
            // Fallback: plain textarea
            cmContainer.style.display = 'none';
            body.style.display = '';
            body.innerHTML = `<textarea class="inline-preview-fallback-editor" id="fallback-editor">${escapeHtml(_previewState.content)}</textarea>`;
        }
    } else if (targetMode === 'diff') {
        if (_previewState.originalContent == null) {
            // First switch into diff mode: fetch the base content, which
            // renders the merge view (or the plain file when nothing changed).
            cmContainer.style.display = 'none';
            body.style.display = '';
            body.innerHTML = '<div class="inline-preview-loading">Loading...</div>';
            await _loadDiffView(_previewState.filepath, _previewState.gen);
        } else if (_previewState.hasDiff) {
            body.style.display = 'none';
            cmContainer.style.display = 'block';
            const ok = await _createCmMergeView(cmContainer, _previewState.originalContent, _previewState.content, langName);
            if (!ok) {
                // Fallback: show plain content
                cmContainer.style.display = 'none';
                body.style.display = '';
                _renderContentView(body, _previewState.content, _previewState.filepath, _previewState.line);
            }
        } else {
            cmContainer.style.display = 'none';
            body.style.display = '';
            _renderContentView(body, _previewState.content, _previewState.filepath, _previewState.line);
        }
    } else {
        // preview — show syntax-highlighted content
        cmContainer.style.display = 'none';
        body.style.display = '';
        _renderContentView(body, _previewState.content, _previewState.filepath, _previewState.line);
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
            if (saveBtn) { saveBtn.textContent = 'Saved!'; setTimeout(() => { saveBtn.textContent = 'Save'; }, 1500); }
        }
    } catch (e) {
        window.showAlertModal?.('Save Failed', `Failed to save: ${e.message}`);
    } finally {
        if (saveBtn) saveBtn.disabled = false;
    }
};

/** Close inline preview and restore the files list. */
window._closeInlinePreview = function() {
    _artifactAbortController?.abort();
    _artifactAbortController = null;
    _destroyCmEditor();
    _previewState = null;
    _artifactManifestStack = [];

    // Remove mobile overlay if present
    document.querySelectorAll('.mobile-file-preview-overlay').forEach(el => el.remove());

    const panel = document.getElementById('agentic-panel-files');
    if (panel) {
        panel.innerHTML = `
            <div class="changed-files-header" id="changed-files-header">
                <div class="files-search-row" style="position:relative">
                    <input type="search" id="files-search-input" class="files-search-input" placeholder="Search or create files..." autocomplete="off">
                    <button class="refresh-files-btn" id="file-search-mode-btn" onclick="toggleFileSearchMode()" title="Toggle browse/search mode" aria-label="Toggle file search mode">${fileSearchModeIcons.directory}</button>
                    <button class="refresh-files-btn" id="diff-expand-all-btn" onclick="toggleAllFileDiffs()" title="Expand all diffs" aria-label="Expand all diffs">${diffExpandIcons.expand}</button>
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

window._artifactBack = _artifactBack;

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

    invalidateDiffs();
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
    invalidateDiffs();

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
        const totals = files.reduce(
            (acc, f) => ({ adds: acc.adds + (f.additions || 0), dels: acc.dels + (f.deletions || 0) }),
            { adds: 0, dels: 0 }
        );
        const stats = files.length > 0
            ? `<span class="file-adds">+${totals.adds}</span><span class="file-dels">-${totals.dels}</span>`
            : '';
        titleEl.innerHTML = `<span class="changed-files-count">${files.length} file${files.length !== 1 ? 's' : ''} changed</span>
            ${stats}
            <select class="diff-mode-select" onchange="setGitDiffMode(this.value)" title="Diff comparison mode">${options}</select>`;
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

    // The rows about to be replaced may host mounted editors.
    destroyInlineDiffs();

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
        const previewBtn = `<button class="file-action-btn" onclick="event.stopPropagation(); openFilePreview('${escapedPath}')" title="Preview"><span class="material-icons">visibility</span></button>`;
        const editBtn = `<button class="file-action-btn" onclick="event.stopPropagation(); openFileEdit('${escapedPath}')" title="Edit"><span class="material-icons">edit</span></button>`;

        // The row expands in place to show its diff; the remaining buttons
        // copy the path, preview the file, and open it in the editor.
        return `<div class="file-item ${statusCls}${agentOnlyCls}" title="${escapeHtml(f.filepath)} (${statusLabel})"
                     data-filepath="${escapeHtml(f.filepath)}"
                     onclick="toggleFileDiff('${escapedPath}', this)">
            ${starBtn}
            <span class="file-diff-caret material-icons">chevron_right</span>
            <span class="file-status-icon">${statusIcon}</span>
            <div class="file-path-wrap">
                <span class="file-name">${escapeHtml(name)}</span>
                ${dir ? `<span class="file-dir">${escapeHtml(dir)}</span>` : ''}
                ${agentsHtml}
            </div>
            ${stats}
            <div class="file-action-btns">${copyBtn}${previewBtn}${editBtn}</div>
        </div>
        <div class="file-diff-body" style="display:none"></div>`;
    }).join('');

    restoreExpandedDiffs();
}
