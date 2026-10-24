/* Coral artifact rendering for the preview pane (manifests, reports, media, sandboxed HTML) */

import { state } from './state.js';
import { escapeHtml, escapeAttr, renderImagePanes } from './utils.js';
import { renderJSONReport } from './artifact_report.js';
import { splitKey, serverUrl, contextServer, isCloud, artifactPath } from './server_base.js';
import { inlineArtifactPath } from './artifact_paths.js';
import { isSvg, svgDataUrl } from './image_source.js';
import { wireDownloads } from './artifact_download.js';
import { lockFrame, sandboxedDoc } from './sandbox_frame.js';
import { responseFilename as contentDispositionName } from './content_disposition.js'; // Content-Disposition describes the stored file; the artifact label may be extensionless or entirely different. Keep it independent of the UI title.

const MAX_ARTIFACT_PREVIEW_BYTES = 2 * 1024 * 1024;
const MAX_ARTIFACT_MANIFEST_ENTRIES = 200;
 const CORAL_ARTIFACT_URI_RE = /^coral:\/\/artifacts\/([a-f0-9]{64})$/i;

function _sizeLabel(bytes) {
    if (!Number.isFinite(bytes) || bytes < 0) return 'unknown size';
    if (bytes < 1024) return `${bytes} bytes`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function _renderFallback(container, { filename, type, size, url, reason }) {
    container.innerHTML = `<div class="inline-preview-artifact-fallback">
        <strong>${escapeHtml(filename || 'artifact')}</strong>
        <span>${escapeHtml(type || 'application/octet-stream')} · ${escapeHtml(_sizeLabel(size))}</span>
        <p>${escapeHtml(reason)}</p>
        <a href="${escapeAttr(url)}" target="_blank" rel="noopener noreferrer" download data-artifact-download="${escapeAttr(url)}" data-artifact-name="${escapeAttr(filename || '')}">Download artifact</a>
    </div>`;
    wireDownloads(container);
}

async function _readText(response, maxBytes, signal) {
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

function _parseManifest(content) {
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

function _entryType(entry) {
    if (entry.media_type) return entry.media_type;
    const name = entry.name.toLowerCase();
    if (/\.md(?:own)?$/.test(name)) return 'Markdown';
    if (/\.json$/.test(name)) return 'JSON';
    if (/\.(?:png|jpe?g|gif|webp|avif|bmp|svg)$/.test(name)) return 'Image';
    if (/\.(?:txt|log|csv|ya?ml|toml|ini|conf|sh|js|ts|go|py|rs|css|html?)$/.test(name)) return 'Text';
    return 'Artifact';
}

function _renderManifest(body, manifest, filename, ctx) {
    const rows = manifest.entries.map((entry, index) => {
        const digest = CORAL_ARTIFACT_URI_RE.exec(entry.uri)[1];
        return `<li class="artifact-manifest-entry">
        <div class="artifact-manifest-main"><button type="button" class="artifact-manifest-open" data-manifest-index="${Number(index)}">
            <span class="artifact-manifest-name">${escapeHtml(entry.name)}</span>
            <span class="artifact-manifest-type">${escapeHtml(_entryType(entry))}</span>
        </button><a class="artifact-manifest-download" href="${escapeAttr(serverUrl(artifactPath(digest), contextServer()))}" download="${escapeAttr(entry.name)}" data-artifact-download="${escapeAttr(serverUrl(artifactPath(digest), contextServer()))}" data-artifact-name="${escapeAttr(entry.name)}" aria-label="Download ${escapeAttr(entry.name)}">Download</a></div>
        <span class="artifact-manifest-uri">${escapeHtml(entry.uri)}</span>
    </li>`;
    }).join('');
    body.innerHTML = `<div class="artifact-manifest-toolbar"><strong>${manifest.entries.length} file${manifest.entries.length === 1 ? '' : 's'}</strong><div class="artifact-manifest-actions"><button type="button" class="inline-preview-mode-btn artifact-manifest-raw">View raw JSON</button><button type="button" class="inline-preview-mode-btn artifact-manifest-download-raw">Download JSON</button></div></div><ul class="artifact-manifest-list">${rows}</ul>`;
    wireDownloads(body);
    // Entries open as their own tabs so the collection stays available.
    body.querySelectorAll('.artifact-manifest-open').forEach(button => button.addEventListener('click', () => {
        const entry = manifest.entries[Number(button.dataset.manifestIndex)];
        if (entry) ctx.openEntry(entry);
    }));
    body.querySelector('.artifact-manifest-raw')?.addEventListener('click', () => {
        ctx.renderContent(body, manifest.raw, filename);
        const back = document.createElement('button');
        back.type = 'button'; back.className = 'artifact-manifest-back'; back.textContent = 'Back to file collection';
        back.addEventListener('click', () => _renderManifest(body, manifest, filename, ctx));
        body.prepend(back);
    });
    body.querySelector('.artifact-manifest-download-raw')?.addEventListener('click', () => {
        const blob = new Blob([manifest.raw], { type: 'application/json' });
        const url = URL.createObjectURL(blob);
        const link = document.createElement('a');
        link.href = url;
        link.download = filename || 'manifest.json';
        link.click();
        setTimeout(() => URL.revokeObjectURL(url), 0);
    });
}

function _renderSandboxed(body, { url, content, name, isStale }) {
    const wrapper = document.createElement('div');
    wrapper.className = 'artifact-linked-preview';
    const note = document.createElement('p');
    note.className = 'artifact-linked-notice';
    note.textContent = url
        ? 'Linked preview: scripts and interactive features are disabled. Some sites block embedding; if the preview stays blank, use Open link.'
        : 'HTML preview: scripts and external resources are disabled. Download the file for the original.';
    const frame = document.createElement('iframe');
    frame.title = `Preview of ${name || 'artifact'}`;
    lockFrame(frame); // empty sandbox + csp: no scripts, same-origin privilege, forms, popups, downloads or navigation
    wrapper.append(note);
    if (url) {
        const status = document.createElement('div');
        status.className = 'artifact-linked-status';
        status.setAttribute('role', 'status');
        status.textContent = 'Loading linked preview…';
        frame.addEventListener('load', () => { if (!isStale()) status.remove(); });
        frame.addEventListener('error', () => {
            if (!isStale()) status.textContent = 'The linked preview could not load. Use Open link to view it.';
        });
        wrapper.append(status);
        frame.src = url;
    } else {
        frame.srcdoc = sandboxedDoc(content);
    }
    wrapper.append(frame);
    body.replaceChildren(wrapper);
}

/** Resolve where an artifact's bytes come from; null when the source is unusable. */
export function resolveArtifactSource(uri, options = {}) {
    const match = CORAL_ARTIFACT_URI_RE.exec(uri);
    const board = state.selectedTeam ? splitKey(state.selectedTeam).name : (state.currentSession?.board_project || '');
    const inlineURL = inlineArtifactPath(options.contentURL, board); // exactly the shape this route has, for this board (a registered server's value must name no other route)
    let externalURL = null;
    try {
        const parsed = new URL(options.externalURL);
        if (parsed.protocol === 'https:' || parsed.protocol === 'http:') externalURL = parsed.href;
    } catch { /* Only explicit HTTP(S) links can be embedded. */ }
    if (!match && !inlineURL && !externalURL) return null;
    // A board post's artifact lives on the board's server (#3540), which the caller names.
    return { url: externalURL || inlineURL || artifactPath(match[1]), externalURL, server: options.server || contextServer() };
}

/**
 * Render an artifact into `body`.
 * ctx: { signal, isStale(), renderContent(body, content, path), openEntry({name, uri, media_type}),
 *        setMeta({ title, filename }) }
 */
export async function renderArtifact(body, uri, options, ctx) {
    const source = resolveArtifactSource(uri, options);
    if (!source) { body.innerHTML = '<div class="inline-preview-error">Artifact unavailable</div>'; return; }
    const { url, externalURL } = source;
    body.innerHTML = '<div class="inline-preview-loading">Loading...</div>';
    if (externalURL) {
        _renderSandboxed(body, { url: externalURL, name: options.filename, isStale: ctx.isStale });
        return;
    }
    try {
        // Managed/inline artifact paths live on the viewed server; external URLs are absolute.
        const requestURL = externalURL ? url : serverUrl(url, source.server);
        const resp = await fetch(requestURL, { signal: ctx.signal });
        if (!resp.ok) throw new Error(`Artifact unavailable (${resp.status})`);
        // The URL the bytes came from; a response built in the page (a test, a cache) has none of its own.
        const bytesURL = resp.url || requestURL;
        const type = (resp.headers.get('Content-Type') || 'application/octet-stream').split(';')[0].toLowerCase();
        // Scoped inline content is deliberately served as text/plain. Its
        // declared artifact media type still selects HTML or Markdown rendering.
        const declaredType = (options.mediaType || '').split(';')[0].trim().toLowerCase();
        const isHTML = type === 'text/html' || type === 'application/xhtml+xml' || declaredType === 'text/html' || declaredType === 'application/xhtml+xml'
            // The hub always answers octet-stream for HTML, so there the declared type (above) or the name decides.
            || (isCloud() && [options.filename || '', contentDispositionName(resp.headers.get('Content-Disposition') || '')].some(n => /\.x?html?$/i.test(n)));
        const responseFilename = contentDispositionName(resp.headers.get('Content-Disposition') || '');
        const filename = options.filename || responseFilename;
        const filenameHints = [options.filename || '', responseFilename];
        const isMarkdown = [type, declaredType].some(value => value === 'text/markdown' || value === 'text/x-markdown') || filenameHints.some(name => /\.(?:md|markdown|mdown)$/i.test(name));
        const isJSON = [type, declaredType].some(value => value === 'application/json' || value.endsWith('+json')) || filenameHints.some(name => /\.json$/i.test(name));
        const artifactPath = responseFilename || filename || uri;
        const sizeHeader = Number(resp.headers.get('Content-Length'));
        const size = Number.isFinite(sizeHeader) && sizeHeader >= 0 ? sizeHeader : -1;
        if (ctx.isStale()) return;
        // The hub's response filter rewrites SVG to an octet-stream download, so there SVG is recognised by its declared
        // type or name, read as text and shown through an <img> data: URL (never inline markup; see image_source.js).
        const isSvgArtifact = isCloud() && (isSvg(type) || isSvg(declaredType) || filenameHints.some(isSvg));
        if (isSvgArtifact) {
            if (size > MAX_ARTIFACT_PREVIEW_BYTES) {
                _renderFallback(body, { filename, type, size, url: bytesURL, reason: `Preview is limited to ${_sizeLabel(MAX_ARTIFACT_PREVIEW_BYTES)}.` });
            } else {
                const svgText = await _readText(resp, MAX_ARTIFACT_PREVIEW_BYTES, ctx.signal);
                if (ctx.isStale()) return;
                renderImagePanes(body, [{ url: svgDataUrl(svgText), missing: 'Artifact unavailable' }]);
            }
        } else if (type.startsWith('audio/') || type.startsWith('video/')) {
            const media = document.createElement(type.startsWith('video/') ? 'video' : 'audio');
            media.controls = true; media.autoplay = false; media.preload = 'metadata';
            media.src = bytesURL; media.className = 'artifact-preview-media';
            body.replaceChildren(media);
        } else if (type.startsWith('image/')) {
            renderImagePanes(body, [{ url: bytesURL, missing: 'Artifact unavailable' }]);
        } else if (!isHTML && !isMarkdown && !isJSON && !type.startsWith('text/') && !type.includes('json') && !type.includes('xml') && !filenameHints.some(name => /\.(?:md|markdown|txt|log|json|xml|csv|ya?ml|toml|ini|conf|sh|js|ts|go|py|rs|css|html?)$/i.test(name))) {
            _renderFallback(body, { filename, type, size, url: bytesURL, reason: 'This binary artifact is not rendered inline.' });
        } else if (size > MAX_ARTIFACT_PREVIEW_BYTES) {
            _renderFallback(body, { filename, type, size, url: bytesURL, reason: `Preview is limited to ${_sizeLabel(MAX_ARTIFACT_PREVIEW_BYTES)}.` });
        } else {
            const content = await _readText(resp, MAX_ARTIFACT_PREVIEW_BYTES, ctx.signal);
            if (ctx.isStale()) return;
            if (isHTML) {
                _renderSandboxed(body, { content, name: filename, isStale: ctx.isStale });
                return;
            }
            const manifest = isJSON ? _parseManifest(content) : null;
            if (manifest) {
                ctx.setMeta({ title: 'Artifact manifest', filename: filename || 'manifest.json' });
                _renderManifest(body, manifest, filename || 'manifest.json', ctx);
            } else if (!isMarkdown && isJSON && renderJSONReport(body, content, filename)) {
                // Recognized reports keep their original bytes in the raw view.
            } else {
                // The shared renderer chooses a language from the extension;
                // artifact display names such as acceptance_report have none.
                ctx.renderContent(body, content, isMarkdown ? 'artifact.md' : artifactPath);
            }
        }
    } catch (error) {
        if (error?.name === 'AbortError' || ctx.isStale()) return;
        body.innerHTML = `<div class="inline-preview-error">${escapeHtml(error.message)}</div>`;
    }
}
