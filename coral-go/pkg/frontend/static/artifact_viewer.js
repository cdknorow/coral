/* The artifact viewer (#3540): a Coral artifact (coral://artifacts/<64 hex>) linked from a board
 * post, a chat or a team's artifacts opens here, in the dashboard, instead of a new tab, where
 * a Markdown file was only a download. One dialog for every view; the artifact is rendered by
 * the preview pane's renderArtifact (images, media, sandboxed HTML, a download for binaries),
 * and Markdown by the strict renderer (raw HTML shown as text, then the sanitizer). The
 * Download button keeps the original bytes one click away, through Coral Hub too. */
import { escapeHtml, renderMarkdownStrict, highlightCodeIn, keepFragmentsInside } from './utils.js';
import { serverUrl, artifactPath, isCloud } from './server_base.js';
import { renderArtifact } from './artifact_preview.js';
import { downloadArtifact, sanitizeDownloadName } from './artifact_download.js';

const HEX_RE = /^[a-f0-9]{64}$/;
let _dialog = null;
let _abort = null;
let _gen = 0;
let _returnFocus = null;
let _server = undefined; // the server of the artifact on show, for the artifacts it links

/** Render an artifact's text: Markdown through the strict renderer, anything else as plain text. */
export function renderArtifactText(el, content, path) {
    if (/\.(?:md|markdown|mdown)$/i.test(path || '')) {
        el.innerHTML = `<div class="preview-pane-markdown artifact-viewer-markdown">${renderMarkdownStrict(content)}</div>`;
        highlightCodeIn(el);
    } else {
        el.innerHTML = `<pre class="preview-pane-code artifact-viewer-text"><code>${escapeHtml(content)}</code></pre>`;
    }
}

function _build() {
    const d = document.createElement('div');
    d.id = 'artifact-viewer';
    d.className = 'modal artifact-viewer';
    d.setAttribute('role', 'dialog');
    d.setAttribute('aria-modal', 'true');
    d.setAttribute('aria-labelledby', 'artifact-viewer-title');
    d.hidden = true;
    d.innerHTML = `<div class="modal-content artifact-viewer-content">
        <div class="artifact-viewer-header">
            <h2 id="artifact-viewer-title" class="artifact-viewer-title"></h2>
            <a class="btn artifact-viewer-download" rel="noopener noreferrer" download>Download</a>
            <button type="button" class="btn artifact-viewer-close" aria-label="Close">✕</button>
        </div>
        <div class="artifact-viewer-body"></div>
    </div>`;
    d.addEventListener('click', (e) => { if (e.target === d) closeArtifactViewer(); });
    d.querySelector('.artifact-viewer-close').addEventListener('click', closeArtifactViewer);
    // A Coral artifact linked inside the shown file opens here too (the strict renderer keeps
    // only web, mailto and coral://artifacts links, #3540).
    d.querySelector('.artifact-viewer-body').addEventListener('click', (e) => {
        const a = e.target.closest && e.target.closest('a[href^="coral://artifacts/"]');
        const m = a && /^coral:\/\/artifacts\/([a-f0-9]{64})$/i.exec(a.getAttribute('href'));
        if (!m) return;
        e.preventDefault();
        openArtifactViewer(m[1], { server: _server, filename: a.textContent.trim() });
    });
    d.addEventListener('keydown', (e) => {
        // Stop here: the page-wide Escape handler (modal_chrome) would also close a modal under the viewer.
        if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); closeArtifactViewer(); }
        else if (e.key === 'Tab') _trapTab(d, e);
    });
    keepFragmentsInside(d.querySelector('.artifact-viewer-body'));
    // Back or Forward changes the page under the viewer (#3532 routes): close it with the page.
    window.addEventListener('popstate', closeArtifactViewer);
    document.body.appendChild(d);
    return d;
}

function _trapTab(d, e) {
    const f = [...d.querySelectorAll('button, a[href], [tabindex]:not([tabindex="-1"])')].filter(el => !el.disabled && el.offsetParent !== null);
    if (!f.length) return;
    const first = f[0], last = f[f.length - 1];
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
}

/** Open the artifact `hex` (64 lowercase hex). opts: { server, filename, mediaType }. */
export function openArtifactViewer(hex, opts = {}) {
    hex = String(hex || '').toLowerCase();
    if (!HEX_RE.test(hex)) return false;
    const d = _dialog || (_dialog = _build());
    if (d.hidden) _returnFocus = document.activeElement;
    _abort?.abort();
    _abort = new AbortController();
    const gen = ++_gen;
    _server = opts.server;
    const url = serverUrl(artifactPath(hex), opts.server);
    const title = d.querySelector('.artifact-viewer-title');
    title.textContent = opts.filename || 'Artifact';
    const dl = d.querySelector('.artifact-viewer-download');
    dl.href = url;
    // An empty download attribute keeps the stored file's own name (Content-Disposition), not the link's label.
    dl.setAttribute('download', '');
    // Through Coral Hub a link can't navigate to /api/remote/...: the bytes are fetched and saved (artifact_download.js).
    dl.onclick = isCloud() ? (e) => { e.preventDefault(); downloadArtifact(url, sanitizeDownloadName(opts.filename || 'artifact')).catch(err => { title.textContent = err.message; }); } : null;
    const body = d.querySelector('.artifact-viewer-body');
    d.hidden = false;
    d.querySelector('.artifact-viewer-close').focus();
    renderArtifact(body, `coral://artifacts/${hex}`, { server: opts.server, filename: opts.filename, mediaType: opts.mediaType }, {
        signal: _abort.signal,
        isStale: () => gen !== _gen || d.hidden,
        renderContent: renderArtifactText,
        openEntry: (entry) => {
            const m = /^coral:\/\/artifacts\/([a-f0-9]{64})$/i.exec(entry.uri || '');
            if (m) openArtifactViewer(m[1], { server: opts.server, filename: entry.name, mediaType: entry.media_type });
        },
        setMeta: ({ title: t, filename }) => {
            if (gen !== _gen) return;
            if (t || filename) title.textContent = t || filename;
        },
    });
    return true;
}

export function closeArtifactViewer() {
    if (!_dialog || _dialog.hidden) return;
    _abort?.abort();
    _abort = null;
    _gen++;
    _dialog.hidden = true;
    _dialog.querySelector('.artifact-viewer-body').replaceChildren(); // stop media, drop frames
    const back = _returnFocus;
    _returnFocus = null;
    if (back && back.isConnected) back.focus();
}

