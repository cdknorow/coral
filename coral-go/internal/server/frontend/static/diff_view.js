/* Inline diffs for the Files tab — each expanded row mounts a read-only
 * CodeMirror unified merge view, collapsed to the changed hunks, so inline
 * diffs get the same syntax highlighting as the file preview pane. */

import { state } from './state.js';
import { escapeHtml, isImagePath, renderImagePanes } from './utils.js';
import { getCm, getLangExtension, getLangFromPath, DIFF_CONFIG } from './cm_util.js';

let _expanded = new Set();   // filepaths the user has opened
const _views = new Map();    // filepath → mounted EditorView
let _queue = [];             // row bodies waiting for their editor
let _active = 0;             // mounts currently in flight

// How many diffs load at once, so "expand all" stays responsive.
const MAX_CONCURRENT_MOUNTS = 4;
// How long to keep the hunk pinned while an expanded region lays out.
const SETTLE_MS = 800;
const SETTLE_STEP_MS = 50;
// Building an editor over a very large file is slow and not worth it inline.
const MAX_INLINE_CHARS = 400000;
// Unchanged runs longer than this collapse, keeping a few lines of context.
const COLLAPSE_MARGIN = 3;
const COLLAPSE_MIN_SIZE = 6;


/* ── Expansion state ───────────────────────────────────────── */

/** Tear down every mounted editor — before a re-render, or on teardown. */
export function destroyInlineDiffs() {
    _views.forEach(view => {
        try { view.destroy(); } catch { /* already detached */ }
    });
    _views.clear();
    _queue = [];
}

/** Drop mounted editors but keep rows open — after a refresh or a diff-mode
 *  change the same files are still worth reading, just with fresh content. */
export function invalidateDiffs() {
    destroyInlineDiffs();
}

/** Full reset, for when the selected agent changes. */
export function resetDiffCache() {
    destroyInlineDiffs();
    _expanded = new Set();
}

function _rowFor(filepath, rowEl) {
    const row = rowEl && rowEl.isConnected
        ? rowEl
        : document.querySelector(`#changed-files-list .file-item[data-filepath="${CSS.escape(filepath)}"]`);
    if (!row) return null;
    const body = row.nextElementSibling;
    if (!body || !body.classList.contains('file-diff-body')) return null;
    return { row, body };
}

/** Toggle one file row's inline diff. */
export function toggleFileDiff(filepath, rowEl) {
    const found = _rowFor(filepath, rowEl);
    if (!found) return;

    if (_expanded.has(filepath)) {
        _expanded.delete(filepath);
        found.row.classList.remove('expanded');
        found.body.style.display = 'none';
        _unmount(filepath, found.body);
    } else {
        _expanded.add(filepath);
        found.row.classList.add('expanded');
        found.body.style.display = '';
        _scheduleMount(found.body);
    }
    _syncExpandAllBtn();
}

/** Expand every changed file, or collapse them all if any are open. */
export function toggleAllFileDiffs() {
    const rows = document.querySelectorAll('#changed-files-list .file-item');
    const collapsing = _expanded.size > 0;

    rows.forEach(row => {
        const filepath = row.dataset.filepath;
        const body = row.nextElementSibling;
        if (!filepath || !body || !body.classList.contains('file-diff-body')) return;

        if (collapsing) {
            row.classList.remove('expanded');
            body.style.display = 'none';
            _unmount(filepath, body);
        } else {
            _expanded.add(filepath);
            row.classList.add('expanded');
            body.style.display = '';
            _scheduleMount(body);
        }
    });

    if (collapsing) _expanded.clear();
    _syncExpandAllBtn();
}

/** Re-open rows that were expanded before the file list re-rendered. */
export function restoreExpandedDiffs() {
    _syncExpandAllBtn();
    if (_expanded.size === 0) return;

    document.querySelectorAll('#changed-files-list .file-item').forEach(row => {
        const filepath = row.dataset.filepath;
        if (!filepath || !_expanded.has(filepath)) return;
        const body = row.nextElementSibling;
        if (!body || !body.classList.contains('file-diff-body')) return;
        row.classList.add('expanded');
        body.style.display = '';
        _scheduleMount(body);
    });
}

/* ── Mounting ──────────────────────────────────────────────── */

/** Queue a row's editor. Expanding a 30-file changeset would otherwise fire
 *  60 requests and build 30 editors at once, so mounts run a few at a time. */
function _scheduleMount(body) {
    if (body.dataset.mounted === '1' || _queue.includes(body)) return;
    _bindCollapseAnchor();
    body.innerHTML = '<div class="diff-file-note">Loading diff...</div>';
    _queue.push(body);
    _pumpQueue();
}

function _pumpQueue() {
    while (_active < MAX_CONCURRENT_MOUNTS && _queue.length > 0) {
        const body = _queue.shift();
        if (!body.isConnected) continue;
        _active++;
        _mount(body).finally(() => {
            _active--;
            _pumpQueue();
        });
    }
}

/* ── Keeping the hunk in place when context is expanded ────── */

/** CodeMirror's "N unchanged lines" widget inserts those lines in place, which
 *  pushes everything below it down — including the hunk you were reading. We
 *  capture the click before CodeMirror handles it, then scroll by exactly the
 *  height that was inserted so the hunk stays where it was on screen. */
function _bindCollapseAnchor() {
    const list = document.getElementById('changed-files-list');
    if (!list || list.dataset.diffAnchorBound === '1') return;
    list.dataset.diffAnchorBound = '1';

    list.addEventListener('click', (e) => {
        const widget = e.target.closest('.cm-collapsedLines');
        if (!widget) return;
        const body = widget.closest('.file-diff-body');
        if (!body) return;

        const anchor = _nearestChange(widget);
        // Only expansions above the hunk shift it; ones below leave it alone.
        if (!anchor || !(widget.compareDocumentPosition(anchor) & Node.DOCUMENT_POSITION_FOLLOWING)) return;

        _holdScrollThroughExpand(list, body);
    }, true);  // capture, so this runs before CodeMirror expands the region
}

/** Re-apply the scroll offset as the row grows. CodeMirror lays out a large
 *  expansion over several frames, so a single measurement right after the
 *  click sees no change yet; this keeps up until the height settles, and
 *  stops the moment the reader scrolls on their own. */
function _holdScrollThroughExpand(list, body) {
    const heightBefore = body.offsetHeight;
    const scrollBefore = list.scrollTop;
    const deadline = Date.now() + SETTLE_MS;
    let applied = 0;
    let expected = scrollBefore;

    const tick = () => {
        // Anything other than our own adjustment means the reader took over.
        if (Math.abs(list.scrollTop - expected) > 2) return;
        const delta = body.offsetHeight - heightBefore;
        if (delta !== applied) {
            applied = delta;
            expected = scrollBefore + delta;
            list.scrollTop = expected;
        }
        if (Date.now() < deadline) setTimeout(tick, SETTLE_STEP_MS);
    };
    setTimeout(tick, 0);
}

/** The changed line this expander belongs to: the next one in the document,
 *  falling back to the previous one for a trailing expander. */
function _nearestChange(widget) {
    const body = widget.closest('.file-diff-body');
    if (!body) return null;
    const changes = [...body.querySelectorAll('.cm-changedLine, .cm-deletedChunk')];
    if (changes.length === 0) return null;
    const after = changes.find(el => widget.compareDocumentPosition(el) & Node.DOCUMENT_POSITION_FOLLOWING);
    return after || changes[changes.length - 1];
}

function _unmount(filepath, body) {
    const view = _views.get(filepath);
    if (view) {
        try { view.destroy(); } catch { /* already detached */ }
        _views.delete(filepath);
    }
    const queued = _queue.indexOf(body);
    if (queued >= 0) _queue.splice(queued, 1);
    body.innerHTML = '';
    delete body.dataset.mounted;
}

async function _mount(body) {
    if (body.dataset.mounted === '1') return;
    body.dataset.mounted = '1';

    const row = body.previousElementSibling;
    const filepath = row && row.dataset.filepath;
    if (!filepath) return;

    const s = state.currentSession;
    if (!s || s.type !== 'live') return;

    const qs = new URLSearchParams({ filepath });
    if (s.session_id) qs.set('session_id', s.session_id);
    const base = `/api/sessions/live/${encodeURIComponent(s.name)}`;

    // Images: the base and working-tree versions side by side
    if (isImagePath(filepath)) {
        const raw = new URLSearchParams(qs);
        raw.set('raw', '1');
        raw.set('t', Date.now());
        renderImagePanes(body, [
            { label: 'Before', url: `${base}/file-original?${raw}`, missing: 'New file' },
            { label: 'After', url: `${base}/file-content?${raw}`, missing: 'Deleted' },
        ]);
        return;
    }

    let original = '';
    let current = '';
    try {
        // A new file has no original; a deleted one has no current content.
        const [origResp, curResp] = await Promise.all([
            fetch(`${base}/file-original?${qs}`),
            fetch(`${base}/file-content?${qs}`),
        ]);
        const origData = await origResp.json();
        const curData = await curResp.json();
        original = origData.error ? '' : (origData.content || '');
        current = curData.error ? '' : (curData.content || '');
    } catch (e) {
        console.error('[coral] failed to load diff for', filepath, e);
        if (body.isConnected) _note(body, 'Failed to load diff');
        return;
    }

    if (!body.isConnected || !_expanded.has(filepath)) return;

    if (original === '' && current === '') {
        _note(body, 'No content to compare');
        return;
    }
    if (_looksBinary(original) || _looksBinary(current)) {
        _note(body, 'Binary file not shown');
        return;
    }
    if (original.length + current.length > MAX_INLINE_CHARS) {
        _note(body, 'File too large to diff inline — open it in the preview to read it');
        return;
    }
    if (original === current) {
        _note(body, 'No textual changes');
        return;
    }

    body.innerHTML = '';
    if (!_createInlineMergeView(body, filepath, original, current)) {
        _note(body, 'Diff view unavailable');
    }
}

function _note(body, text) {
    body.innerHTML = `<div class="diff-file-note">${escapeHtml(text)}</div>`;
}

/** Git's own heuristic: a NUL byte in the leading chunk means binary. */
function _looksBinary(text) {
    return text.slice(0, 8000).includes('\u0000');
}

/** Mount a read-only unified merge view showing only the changed hunks. */
function _createInlineMergeView(container, filepath, original, current) {
    const cm = getCm();
    if (!cm) return false;

    try {
        const extensions = [
            cm.basicSetup,
            cm.oneDark,
            cm.EditorView.lineWrapping,
            cm.EditorView.editable.of(false),
            cm.EditorState.readOnly.of(true),
            // Size to content; the file list stays the scroll container.
            cm.EditorView.theme({
                '&': { height: 'auto' },
                '.cm-scroller': { overflow: 'hidden' },
            }),
            cm.unifiedMergeView({
                original: cm.Text.of(original.split('\n')),
                mergeControls: false,
                collapseUnchanged: { margin: COLLAPSE_MARGIN, minSize: COLLAPSE_MIN_SIZE },
                diffConfig: DIFF_CONFIG,
            }),
        ];

        const langExt = getLangExtension(cm, getLangFromPath(filepath));
        if (langExt) extensions.push(langExt);

        const view = new cm.EditorView({
            state: cm.EditorState.create({ doc: current, extensions }),
            parent: container,
        });
        _views.set(filepath, view);
        return true;
    } catch (e) {
        console.error('[coral] inline merge view failed for', filepath, e);
        return false;
    }
}

/* ── Header control ────────────────────────────────────────── */

/** Stroke icons matching the browse and refresh buttons beside it. */
export const diffExpandIcons = {
    expand: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 9l4-4 4 4"/><path d="M8 15l4 4 4-4"/></svg>',
    collapse: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 5l4 4 4-4"/><path d="M8 19l4-4 4 4"/></svg>',
};

function _syncExpandAllBtn() {
    const btn = document.getElementById('diff-expand-all-btn');
    if (!btn) return;
    const open = _expanded.size > 0;
    btn.classList.toggle('active', open);
    btn.title = open ? 'Collapse all diffs' : 'Expand all diffs';
    btn.setAttribute('aria-label', btn.title);
    btn.innerHTML = open ? diffExpandIcons.collapse : diffExpandIcons.expand;
}
