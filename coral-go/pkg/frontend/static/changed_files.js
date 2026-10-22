/* Changed files panel — load and render per-agent file diffs */

import { state } from './state.js';
import { escapeHtml, escapeAttr, showToast, refusedTooLarge } from './utils.js';
import { registerActions, textOf } from './actions.js';
import { fetchFileList, fuzzyFilter } from './file_mention.js';
import { initFilesSourcePicker, syncFilesSourceTeam } from './team_artifacts.js';
import { fileIconHtml } from './file_explorer.js';
import { openPreviewTab, openArtifactTab, setPreviewSession } from './preview_pane.js';
import { storeKey, readStars, writeStars } from './local_store.js';
import { serverFetch, serverForSession, sessionServer, currentServer, isCloud, dropBody } from './server_base.js';

let _currentFiles = [];
let _searchTimeout = null;
let _renderTimer = null;

/* ── Starred files (persisted in localStorage per session) ── */

// The signed-in account's (local_store.js, #3681), stored with when they were last changed, so an
// untouched list ages out.
function _starKey() {
    const sid = state.currentSession && state.currentSession.session_id;
    return sid ? storeKey(`coral-starred-files-${sid}`) : null;
}

function _getStarredFiles() {
    const key = _starKey();
    return key ? readStars(key).files : [];
}

function _setStarredFiles(files) {
    const key = _starKey();
    if (key) writeStars(key, files);
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
            const starBtn = `<button class="file-star-btn starred" data-filepath="${escapeAttr(filepath)}" data-on-click="file-star" data-path="${escapeAttr(filepath)}" title="Unstar">★</button>`;
            const copyBtn = `<button class="file-action-btn" data-on-click="file-copy" data-path="${escapeAttr(filepath)}" title="Copy path" aria-label="Copy path"><span class="material-icons" aria-hidden="true">content_copy</span></button>`;
            const previewBtn = `<button class="file-action-btn" data-on-click="file-preview" data-path="${escapeAttr(filepath)}" title="Preview" aria-label="Preview"><span class="material-icons" aria-hidden="true">visibility</span></button>`;
            const editBtn = `<button class="file-action-btn" data-on-click="file-edit" data-path="${escapeAttr(filepath)}" title="Edit" aria-label="Edit"><span class="material-icons" aria-hidden="true">edit</span></button>`;
            return `<div class="file-item file-starred" data-on-click="file-open" data-path="${escapeAttr(filepath)}">
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
        html += `<div class="${cls}" data-index="${Number(i)}">${escapeHtml(filepath)}</div>`;
    });

    // +create option at the end
    if (query && looksLikePath && !hasExactMatch) {
        const createIdx = _searchResults.length;
        _searchResults.push({ path: query, type: 'create' });
        const cls = createIdx === _searchSelectedIdx ? 'file-mention-item selected' : 'file-mention-item';
        html += `<div class="${cls}" data-index="${Number(createIdx)}" style="color:var(--accent)">+ Create ${escapeHtml(query)}</div>`;
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
        _createFile(item.path);
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

function _updateSearchSelection() {
    const dropdown = document.getElementById('files-search-dropdown');
    if (!dropdown) return;
    dropdown.querySelectorAll('.file-mention-item').forEach((el, i) => {
        el.classList.toggle('selected', i === _searchSelectedIdx);
    });
    const selected = dropdown.querySelector('.file-mention-item.selected');
    if (selected) selected.scrollIntoView({ block: 'nearest' });
}

// The list was refused as too large to check through Coral Hub (#3718): it says so, not "No changed files".
let _filesRefused = false;
let _notGit = false; // the folder is not a git repository (#6072): there is nothing to compare, so the panel says so instead of "0 files changed vs HEAD~1"

export async function loadChangedFiles(agentName, sessionId) {
    if (!agentName) return;
    try {
        const params = new URLSearchParams();
        const sid = sessionId || (state.currentSession && state.currentSession.session_id);
        if (sid) params.set("session_id", sid);
        const qs = params.toString() ? `?${params}` : "";
        const resp = await serverFetch(serverForSession(agentName), `/api/sessions/live/${encodeURIComponent(agentName)}/files${qs}`);
        _filesRefused = !resp.ok && await refusedTooLarge(resp);
        if (!resp.ok) { dropBody(resp); throw new Error(`files fetch failed: ${resp.status}`); }
        const data = await resp.json();
        _currentFiles = data.files || [];
        _notGit = data.is_git === false;
        if (data.diff_mode) _cloudDiffMode = data.diff_mode;
    } catch (e) {
        _currentFiles = [];
        _notGit = false;
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
async function _createFile(filePath) {
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

// On the hub the comparison base is the machine's own setting: it is read from the files response and changed through the one
// narrow route the hub allows (PUT /api/settings/git-diff-mode, Interact tier), not through the hub's per-user settings.
let _cloudDiffMode = '';

export async function setGitDiffMode(mode) {
    if (!_diffModes.includes(mode)) return;
    if (isCloud()) {
        try {
            const resp = await serverFetch(currentServer(), '/api/settings/git-diff-mode', {
                method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ mode }),
            });
            if (!resp.ok) { renderChangedFiles(); return; } // refused (view-only server): the wrapper has explained it; show the real mode again
            _cloudDiffMode = mode;
        } catch (e) { renderChangedFiles(); return; }
        refreshChangedFiles();
        return;
    }

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

function _getGitDiffMode() {
    if (isCloud()) return _cloudDiffMode || 'branch_point';
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
            _notGit = data.is_git === false;
            if (data.diff_mode) _cloudDiffMode = data.diff_mode;
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

 function renderChangedFiles() {
    const list = document.getElementById('changed-files-list');
    const titleEl = document.getElementById('changed-files-title');
    const countEl = document.getElementById('files-bar-count');
    if (!list) return;

    if (_notGit) { // no comparison to choose and no count to give (#6072)
        if (titleEl) titleEl.textContent = 'Not a git folder';
        if (countEl) countEl.textContent = '';
        list.innerHTML = '<div class="file-empty">Changes show once the folder is a git repository.</div>';
        return;
    }

    const files = _currentFiles.slice().sort((a, b) => a.filepath.localeCompare(b.filepath));

    if (titleEl) {
        const diffMode = _getGitDiffMode();
        const options = _diffModes.map(m =>
            `<option value="${escapeAttr(m)}"${m === diffMode ? ' selected' : ''}>${escapeHtml(_diffModeLabels[m])}</option>`
        ).join('');
        const totals = files.reduce(
            (acc, f) => ({ adds: acc.adds + (f.additions || 0), dels: acc.dels + (f.deletions || 0) }),
            { adds: 0, dels: 0 }
        );
        const modeSelect = `<select class="diff-mode-select" data-on-change="diff-mode" title="Diff comparison mode">${options}</select>`;
        const stats = files.length > 0
            ? `<span class="file-adds">+${totals.adds}</span><span class="file-dels">-${totals.dels}</span>`
            : '';
        titleEl.innerHTML = `<span class="changed-files-count">${files.length} file${files.length !== 1 ? 's' : ''} changed</span>
            ${stats}
            ${modeSelect}`;
    }
    if (countEl) {
        countEl.textContent = files.length > 0 ? String(files.length) : '';
    }

    if (files.length === 0) {
        // The comparison choice is in the header above, with the count: not drawn a second time under "No changed files" (#5991).
        list.innerHTML = _filesRefused
            ? '<div class="file-empty">Too many changed files to show through Coral Hub; open the agent on the computer.</div>'
            : `<div class="file-empty">No changed files</div>`;
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
        const isStarred = starred.has(f.filepath);
        const isAgentOnly = f.status === 'agent_only';
        const agentOnlyCls = isAgentOnly ? ' file-agent-only' : '';
        const agentsHtml = f.agents && f.agents.length > 0
            ? `<span class="file-agents">${escapeHtml(f.agents.map(a => a.name).join(', '))}</span>`
            : '';
        const starBtn = `<button class="file-star-btn ${escapeAttr(isStarred ? 'starred' : '')}" data-filepath="${escapeAttr(f.filepath)}" data-on-click="file-star" data-path="${escapeAttr(f.filepath)}" title="${escapeAttr(isStarred ? 'Unstar' : 'Star')}">${isStarred ? '★' : '☆'}</button>`;
        const copyBtn = `<button class="file-action-btn" data-on-click="file-copy" data-path="${escapeAttr(f.filepath)}" title="Copy path" aria-label="Copy path"><span class="material-icons" aria-hidden="true">content_copy</span></button>`;
        const previewBtn = `<button class="file-action-btn" data-on-click="file-preview" data-path="${escapeAttr(f.filepath)}" data-diff="1" title="Preview" aria-label="Preview"><span class="material-icons" aria-hidden="true">visibility</span></button>`;
        const editBtn = `<button class="file-action-btn" data-on-click="file-edit" data-path="${escapeAttr(f.filepath)}" data-diff="1" title="Edit" aria-label="Edit"><span class="material-icons" aria-hidden="true">edit</span></button>`;

        // Clicking the row opens its diff in the preview pane; the buttons
        // copy the path, preview the file, and open it in the editor.
        return `<div class="file-item ${statusCls}${agentOnlyCls}" title="${escapeHtml(f.filepath)} (${escapeAttr(statusLabel)})"
                     data-filepath="${escapeAttr(f.filepath)}"
                     data-on-click="file-open" data-path="${escapeAttr(f.filepath)}" data-diff="1">
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

// The changed and starred files' markup (#3645): data-on-* names, never code. The path is the file's
// own (data-path, never re-escaped into a script); data-diff opens its diff.
const pathOf = el => textOf(el.dataset.path, 4096);
const diffOpts = el => (el.dataset.diff === '1' ? [0, { diff: true }] : []);
registerActions({
    'file-open': el => { const p = pathOf(el); if (p) openFilePreview(p, ...diffOpts(el)); },
    'file-star': (el, event) => { event.stopPropagation(); const p = pathOf(el); if (p) toggleStarFile(p); },
    'file-copy': (el, event) => { event.stopPropagation(); const p = pathOf(el); if (p) copyFilePath(p); },
    'file-preview': (el, event) => { event.stopPropagation(); const p = pathOf(el); if (p) openFilePreview(p, ...diffOpts(el)); },
    'file-edit': (el, event) => { event.stopPropagation(); const p = pathOf(el); if (p) openFileEdit(p, ...diffOpts(el)); },
    // On mousedown, as before: the @-mention list closes on the input's blur, which a click comes after.
    // Only a mode this list offers.
    'diff-mode': el => { if (_diffModes.includes(el.value)) setGitDiffMode(el.value); },
});
