/* Bottom preview pane — tabbed file/artifact viewer below the terminal/chat area */

import { state } from './state.js';
import { escapeHtml, escapeAttr, showToast, isImagePath, isMediaPath, renderImagePanes, renderMediaPanes, DOMPURIFY_CONFIG } from './utils.js';
import { getCm, getLangExtension, getLangFromPath, DIFF_CONFIG } from './cm_util.js';
import { makeDraggable } from './draggable.js';
import { toggleStarFile, isFileStarred } from './changed_files.js';
import { renderArtifact, resolveArtifactSource } from './artifact_preview.js';

const ICON_MAP = {
    javascript: '{ }', typescript: '{ }', jsx: '{ }', tsx: '{ }',
    json: '{ }', css: '⬡', html: '◇', markdown: '¶',
    python: '⌘', go: '⌘', rust: '⌘', java: '⌘', c: '⌘', cpp: '⌘',
    sql: '⊞', yaml: '≡', toml: '≡', xml: '◇', svg: '◇',
    shell: '$', bash: '$', zsh: '$', dockerfile: '▣',
};

function _fileIcon(tab) {
    if (tab.kind === 'artifact') return '❖';
    return ICON_MAP[getLangFromPath(tab.filepath)] || '◻';
}

function _fileName(filepath) {
    const i = filepath.lastIndexOf('/');
    return i === -1 ? filepath : filepath.substring(i + 1);
}

function _tabTitle(tab) {
    if (tab.kind === 'artifact') return tab.title || tab.options.filename || 'Artifact';
    return _fileName(tab.filepath);
}

function _agentName() {
    return state.currentSession && state.currentSession.name;
}

function _apiQs(filepath) {
    const qs = new URLSearchParams({ filepath });
    const sid = state.currentSession && state.currentSession.session_id;
    if (sid) qs.set('session_id', sid);
    return qs;
}

function _fileUrl(endpoint, filepath, extra) {
    const qs = _apiQs(filepath);
    if (extra) for (const [k, v] of Object.entries(extra)) qs.set(k, v);
    return `/api/sessions/live/${encodeURIComponent(_agentName())}/${endpoint}?${qs}`;
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

const DOCK_ICONS = {
    // Shown while docked at the bottom: switch to a full-height column.
    bottom: '<svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="1.5" y="2.5" width="13" height="11" rx="1.5"/><path d="M6 2.5v11"/></svg>',
    // Shown while docked as a column: switch back to the bottom.
    column: '<svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="1.5" y="2.5" width="13" height="11" rx="1.5"/><path d="M1.5 9h13"/></svg>',
};

const _isMobile = () => window.innerWidth <= 767;
const _effectiveDock = () => (_isMobile() ? 'bottom' : _dock);
let _cmView = null;        // editor or merge view for the active tab
let _fallbackEditor = null;

export function initPreviewPane() {
    const handle = document.getElementById('preview-pane-resize-handle');
    if (!handle) return;

    const saved = localStorage.getItem('coral-preview-pane-height');
    if (saved) _paneHeight = parseInt(saved, 10);

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
                localStorage.setItem('coral-preview-pane-height', _paneHeight);
            }
        },
    });

    const savedWidth = localStorage.getItem('coral-preview-column-width');
    if (savedWidth) _paneWidth = parseInt(savedWidth, 10);
    if (localStorage.getItem('coral-preview-pane-dock') === 'column') _dock = 'column';

    const colHandle = document.getElementById('preview-column-resize-handle');
    const column = document.getElementById('preview-column');
    const liveBody = document.querySelector('.live-body');
    if (colHandle && column && liveBody) {
        makeDraggable(colHandle, {
            cursor: 'col-resize',
            onMove: (e) => {
                const side = document.getElementById('agentic-state');
                const max = liveBody.getBoundingClientRect().width - 240 - (side ? side.offsetWidth : 0);
                return Math.min(Math.max(column.getBoundingClientRect().right - e.clientX, 240), Math.max(240, max));
            },
            onFlush: (v) => { column.style.width = v + 'px'; },
            onEnd: (v) => {
                if (v != null) {
                    _paneWidth = Math.round(v);
                    localStorage.setItem('coral-preview-column-width', _paneWidth);
                }
            },
        });
    }

    document.getElementById('preview-dock-btn')?.addEventListener('click', () => {
        _dock = _dock === 'column' ? 'bottom' : 'column';
        localStorage.setItem('coral-preview-pane-dock', _dock);
        _placePane();
    });
    window.addEventListener('resize', () => { if (_tabs.length) _placePane(); });
    _placePane();

    // Toolbar buttons are re-rendered per tab, so listen on the container.
    document.getElementById('preview-toolbar')?.addEventListener('click', _onToolbarClick);
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
        if (_paneWidth) column.style.width = _paneWidth + 'px';
    } else {
        if (pane.parentElement !== side) side.appendChild(pane);
        pane.classList.remove('docked-column');
        if (_paneHeight) pane.style.height = _paneHeight + 'px';
    }
    column.style.display = dock === 'column' && visible ? '' : 'none';
    colHandle.style.display = dock === 'column' && visible ? '' : 'none';
    bottomHandle.style.display = dock === 'bottom' && visible ? '' : 'none';
    pane.style.display = visible ? '' : 'none';

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
    _placePane();
}

function _activeTab() {
    return _tabs.find(t => t.id === _activeId) || null;
}

function _isDirty(tab) {
    return tab.kind === 'file' && tab.content !== null && tab.savedContent !== null && tab.content !== tab.savedContent;
}

/* ── Tab bar ───────────────────────────────────────────────── */

function _renderTabs() {
    const bar = document.getElementById('preview-tabs');
    if (!bar) return;

    let html = '';
    for (const tab of _tabs) {
        const active = tab.id === _activeId ? ' active' : '';
        const title = tab.kind === 'artifact' ? (tab.options.filename || tab.filepath) : tab.filepath;
        const preview = tab.pinned ? '' : ' preview';
        html += `<div class="preview-tab${active}${preview}" data-tab-id="${tab.id}" title="${tab.pinned ? '' : 'Preview — double-click to keep open'}">
            <span class="preview-tab-icon">${_fileIcon(tab)}</span>
            <span class="preview-tab-name" title="${escapeAttr(title)}">${escapeHtml(_tabTitle(tab))}${_isDirty(tab) ? ' •' : ''}</span>
            <button class="preview-tab-close" data-tab-id="${tab.id}" title="Close">&times;</button>
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
    return `<button class="inline-preview-mode-btn${tab.mode === mode ? ' active' : ''}" data-action="mode" data-mode="${mode}" title="${title}"><span class="material-icons">${icon}</span></button>`;
}

function _renderToolbar(tab) {
    const bar = document.getElementById('preview-toolbar');
    if (!bar) return;
    if (!tab) { bar.innerHTML = ''; return; }

    if (tab.kind === 'artifact') {
        const src = resolveArtifactSource(tab.filepath, tab.options);
        const link = src
            ? `<a class="team-artifacts-action" href="${escapeAttr(src.url)}" ${src.externalURL ? 'target="_blank" rel="noopener noreferrer"' : `download="${escapeAttr(tab.options.filename || '')}"`}>${src.externalURL ? 'Open link' : 'Download'}</a>`
            : '';
        bar.innerHTML = `<span class="preview-toolbar-path" title="${escapeAttr(tab.options.filename || tab.filepath)}">${escapeHtml(tab.options.filename || 'Artifact preview')}</span>
            <div class="inline-preview-actions">${link}</div>`;
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
    } else if (btn.dataset.action === 'save') saveTab(tab, btn);
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

export function closeTab(tabId) {
    const idx = _tabs.findIndex(t => t.id === tabId);
    if (idx === -1) return;
    const tab = _tabs[idx];
    if (_isDirty(tab) && !window.confirm(`Discard unsaved changes to ${_fileName(tab.filepath)}?`)) return;
    if (tab.id === _activeId) { _destroyEditor(); }
    _disposeTab(tab);
    _tabs.splice(idx, 1);

    if (_tabs.length === 0) {
        _activeId = null;
        _renderTabs();
        _renderToolbar(null);
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

async function _render(tab) {
    const body = _body();
    if (!body) return;
    _destroyEditor();
    const gen = ++tab.renderGen;
    const current = () => _activeId === tab.id && tab.renderGen === gen;
    _renderToolbar(tab);

    if (tab.kind === 'artifact') {
        tab.abort?.abort();
        tab.abort = new AbortController();
        await renderArtifact(body, tab.filepath, tab.options, {
            signal: tab.abort.signal,
            isStale: () => !current(),
            renderContent: (el, content, path) => _renderContentView(el, content, path, 0, false),
            openEntry: (entry) => openArtifactTab(entry.uri, { filename: entry.name, mediaType: entry.media_type }),
            setMeta: ({ title }) => { tab.title = title; _renderTabs(); },
        });
        return;
    }

    const binaryKind = isImagePath(tab.filepath) ? 'image' : isMediaPath(tab.filepath) ? 'media' : null;
    if (binaryKind) {
        const render = binaryKind === 'image' ? renderImagePanes : renderMediaPanes;
        const t = Date.now(); // the file may have changed since the last look
        if (tab.mode === 'diff') {
            render(body, [
                { label: 'Before', url: _fileUrl('file-original', tab.filepath, { raw: 1, t }), missing: 'New file' },
                { label: 'After', url: _fileUrl('file-content', tab.filepath, { raw: 1, t }), missing: 'Deleted' },
            ]);
        } else {
            render(body, [{ url: _fileUrl('file-content', tab.filepath, { raw: 1, t }), missing: binaryKind === 'image' ? 'Image not found' : 'Media not found' }]);
        }
        return;
    }

    if (tab.content === null) {
        _setBody('<div class="preview-pane-loading">Loading…</div>');
        if (!_agentName()) return;
        try {
            const resp = await fetch(_fileUrl('file-content', tab.filepath));
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

    if (tab.mode === 'edit') {
        _mountEditor(tab, body);
    } else if (tab.mode === 'diff') {
        await _mountDiff(tab, body, current);
    } else {
        _renderContentView(body, tab.content, tab.filepath, tab.line, true);
    }
}

function _cmHost(body) {
    body.replaceChildren();
    const host = document.createElement('div');
    host.className = 'inline-preview-cm preview-pane-cm';
    body.appendChild(host);
    return host;
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
            const resp = await fetch(_fileUrl('file-original', tab.filepath));
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
                cm.unifiedMergeView({ original: cm.Text.of(tab.originalContent.split('\n')), diffConfig: DIFF_CONFIG }),
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
        const safe = typeof DOMPurify !== 'undefined' ? DOMPurify.sanitize(html, DOMPURIFY_CONFIG) : html;
        body.innerHTML = `<div class="preview-pane-markdown">${safe}</div>`;
        if (line > 0) _scrollToLine(body, content, line);
        return;
    }

    body.innerHTML = `<pre class="preview-pane-code"><code class="language-${lang}">${escapeHtml(content)}</code></pre>`;
    if (window.hljs) {
        const block = body.querySelector('pre code');
        if (block) window.hljs.highlightElement(block);
    }
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
    const agentName = _agentName();
    if (!agentName || tab.content === null) return;
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

export function resetPreviewPane() {
    closeAllTabs();
    const body = _body();
    if (body) body.innerHTML = '';
}
