/* Changed files panel — load and render per-agent file diffs */

import { state } from './state.js';
import { escapeHtml, showToast } from './utils.js';
import { fetchFileList, fuzzyFilter, getDirBrowseResults } from './file_mention.js';
import { initFilesSourcePicker, syncFilesSourceTeam } from './team_artifacts.js';
import { fileIconHtml } from './file_explorer.js';
import { openPreviewTab, openArtifactTab, setPreviewSession } from './preview_pane.js';
import { serverFetch, serverForSession, sessionServer, currentServer } from './server_base.js';

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

export function isFileStarred(filepath) {
    return _getStarredFiles().includes(filepath);
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

export async function searchRepoFiles(query) {
    if (!query || !state.currentSession || state.currentSession.type !== 'live') {
        _hideSearchDropdown();
        return;
    }

    const files = await fetchFileList();
    const matches = fuzzyFilter(files, query);
    clearTimeout(_renderTimer);
    _renderTimer = setTimeout(() => _renderSearchDropdown(matches, query), 30);
}

function _renderSearchDropdown(files, query) {
    const dropdown = document.getElementById('files-search-dropdown');
    if (!dropdown) return;

    const hasExactMatch = files.some(f => f === query);
    const looksLikePath = query.includes('/') || query.includes('.');

    let html = '';

    // Build results list
    _searchResults = [];
    files.slice(0, 50).forEach((filepath, i) => {
        const cls = i === _searchSelectedIdx ? 'file-mention-item selected' : 'file-mention-item';
        _searchResults.push({ path: filepath, type: 'file' });
        html += `<div class="${cls}" data-index="${i}">${escapeHtml(filepath)}</div>`;
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

    if (item.type === 'create') {
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

    input.placeholder = 'Search files...';

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
            // Show the file listing on focus
            if (!state.currentSession || state.currentSession.type !== 'live') return;
            const files = await fetchFileList();
            if (files && files.length > 0) {
                _renderSearchDropdown(files.slice(0, 50), '');
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
        const resp = await serverFetch(serverForSession(agentName), `/api/sessions/live/${encodeURIComponent(agentName)}/files${qs}`);
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

/** Show preview for a file. Everything opens as a tab in the bottom preview pane;
 *  `opts.diff` marks files from the changed-files list so the Diff mode is offered. */
export function openFilePreview(filepath, line, opts) {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    if (/^coral:\/\/artifacts\/[a-f0-9]{64}$/i.test(filepath || '')) {
        openArtifactTab(filepath);
        return;
    }
    openPreviewTab(filepath, line, opts);
}

function openTeamArtifactPreview(item) {
    openArtifactTab(item.uri || '', {
        contentURL: item.inline ? item.content_url : null,
        externalURL: item.external_url,
        mediaType: item.media_type,
        filename: item.name,
    });
}

// Called immediately after a session switch, before transcript/file requests.
export function syncFilesViewerSession() {
    syncFilesSourceTeam();
    setPreviewSession(state.currentSession?.type === 'live' ? state.currentSession.session_id : null);
    initFilesSourcePicker(openTeamArtifactPreview, openFilePreview);
}

/** Open file directly in edit mode (clicking the edit icon). */
export function openFileEdit(filepath, line, opts) {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    openPreviewTab(filepath, line, { ...opts, mode: 'edit' });
}

/** Create a new file via the API and open it in the editor. */
// Exposed on window for onclick in rendered search results.
window._createFile = async function(filePath) {
    if (!filePath) return;
    const s = state.currentSession;
    if (!s || s.type !== 'live') return;

    try {
        const qs = new URLSearchParams({ filepath: filePath, session_id: s.session_id || '' });
        const resp = await serverFetch(sessionServer(s), `/api/sessions/live/${encodeURIComponent(s.name)}/file-content?${qs}`, {
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
        const resp = await serverFetch(
            currentServer(), `/api/sessions/live/${encodeURIComponent(agentName)}/files/refresh`,
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

    const starred = new Set(_getStarredFiles());
    list.innerHTML = files.map((f) => {
        const { dir, name } = splitPath(f.filepath);
        const statusCls = getStatusClass(f.status);
        const statusLabel = getStatusLabel(f.status);
        const adds = f.additions > 0 ? `<span class="file-adds">+${f.additions}</span>` : '';
        const dels = f.deletions > 0 ? `<span class="file-dels">-${f.deletions}</span>` : '';
        const stats = (adds || dels) ? `<span class="file-stats">${adds}${dels}</span>` : '';
        const escapedPath = escapeHtml(f.filepath).replace(/'/g, "\\'");
        const isStarred = starred.has(f.filepath);
        const isAgentOnly = f.status === 'agent_only';
        const agentOnlyCls = isAgentOnly ? ' file-agent-only' : '';
        const agentsHtml = f.agents && f.agents.length > 0
            ? `<span class="file-agents">${escapeHtml(f.agents.map(a => a.name).join(', '))}</span>`
            : '';
        const starBtn = `<button class="file-star-btn ${isStarred ? 'starred' : ''}" data-filepath="${escapeHtml(f.filepath)}" onclick="event.stopPropagation(); toggleStarFile('${escapedPath}')" title="${isStarred ? 'Unstar' : 'Star'}">${isStarred ? '★' : '☆'}</button>`;
        const copyBtn = `<button class="file-action-btn" onclick="event.stopPropagation(); copyFilePath('${escapedPath}')" title="Copy path"><span class="material-icons">content_copy</span></button>`;
        const previewBtn = `<button class="file-action-btn" onclick="event.stopPropagation(); openFilePreview('${escapedPath}', 0, {diff: true})" title="Preview"><span class="material-icons">visibility</span></button>`;
        const editBtn = `<button class="file-action-btn" onclick="event.stopPropagation(); openFileEdit('${escapedPath}', 0, {diff: true})" title="Edit"><span class="material-icons">edit</span></button>`;

        // Clicking the row opens its diff in the preview pane; the buttons
        // copy the path, preview the file, and open it in the editor.
        return `<div class="file-item ${statusCls}${agentOnlyCls}" title="${escapeHtml(f.filepath)} (${statusLabel})"
                     data-filepath="${escapeHtml(f.filepath)}"
                     onclick="openFilePreview('${escapedPath}', 0, {diff: true})">
            ${starBtn}
            <span class="file-type-icon">${fileIconHtml(name)}</span>
            <div class="file-path-wrap">
                <span class="file-name">${escapeHtml(name)}</span>
                ${dir ? `<span class="file-dir">${escapeHtml(dir)}</span>` : ''}
                ${agentsHtml}
            </div>
            ${stats}
            <div class="file-action-btns">${copyBtn}${previewBtn}${editBtn}</div>
        </div>`;
    }).join('');
}
