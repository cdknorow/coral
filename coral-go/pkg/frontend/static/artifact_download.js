/* Artifact downloads for the Cloud Hub.
 *
 * A plain <a href download> to /api/remote/... is a top-level navigation, which the hub refuses on purpose (it cannot
 * tell a link from an attack). So on the hub the bytes are fetched (same-origin GET) and saved from memory:
 *   - the Blob type is ALWAYS application/octet-stream, never the type the artifact declares, so nothing about the
 *     saved object can be interpreted as a page or a script;
 *   - the blob: URL is only ever the target of a programmatic <a download> click (never window.open, never location),
 *     then revoked;
 *   - the file name is a sanitised basename;
 *   - at most MAX_DOWNLOAD_BYTES are held in memory.
 * Off the hub the links keep working as plain links. */
import { isCloud } from './server_base.js';

export const MAX_DOWNLOAD_BYTES = 25 * 1024 * 1024;

/** A safe file name: no path parts, no control or bidi-override characters, no leading dots, at most 100 characters. */
export function sanitizeDownloadName(name) {
    let n = String(name == null ? '' : name);
    n = n.slice(n.lastIndexOf('/') + 1);
    n = n.slice(n.lastIndexOf('\\') + 1);
    // eslint-disable-next-line no-control-regex
    n = n.replace(/[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u2028-\u202e\u2066-\u2069\ufeff]/g, '').replace(/^[.\s]+/, '').trim();
    if (n.length > 100) n = n.slice(0, 100);
    return n || 'artifact';
}

/** Fetch `url` (same-origin GET) and save it as `name`. Throws a user-readable Error on failure. */
export async function downloadArtifact(url, name) {
    const target = new URL(url, window.location.href);
    if (target.origin !== window.location.origin) throw new Error('Download refused: not a same-origin link');
    const resp = await fetch(target.href, { method: 'GET', cache: 'no-store', credentials: 'same-origin' });
    if (!resp.ok) throw new Error(`Download failed (${resp.status})`);
    const declared = Number(resp.headers.get('Content-Length'));
    if (Number.isFinite(declared) && declared > MAX_DOWNLOAD_BYTES) throw new Error('Artifact is too large to download here (limit 25 MiB)');
    const buf = await resp.arrayBuffer();
    if (buf.byteLength > MAX_DOWNLOAD_BYTES) throw new Error('Artifact is too large to download here (limit 25 MiB)');
    const blobUrl = URL.createObjectURL(new Blob([buf], { type: 'application/octet-stream' }));
    try {
        const a = document.createElement('a');
        a.href = blobUrl;
        a.download = sanitizeDownloadName(name);
        a.rel = 'noopener';
        a.style.display = 'none';
        document.body.appendChild(a);
        a.click();
        a.remove();
    } finally {
        setTimeout(() => URL.revokeObjectURL(blobUrl), 0);
    }
}

async function _run(url, name) {
    try {
        await downloadArtifact(url, name);
    } catch (e) {
        const { showToast } = await import('./utils.js');
        showToast(e && e.message ? e.message : 'Download failed', true);
    }
}

/** On the hub, turn one anchor into a fetch-and-save button; elsewhere leave it a plain download link. */
export function bindDownload(anchor, url, name) {
    if (!anchor || !isCloud()) return anchor;
    anchor.removeAttribute('href');
    anchor.removeAttribute('download');
    anchor.removeAttribute('target');
    anchor.setAttribute('role', 'button');
    anchor.tabIndex = 0;
    const go = e => { e.preventDefault(); _run(url, name); };
    anchor.addEventListener('click', go);
    anchor.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') go(e); });
    return anchor;
}

/** Bind every `a[data-artifact-download]` under `root` (markup built from strings carries the url and name in data
 *  attributes so no handler text is needed). */
export function wireDownloads(root) {
    if (!root || !isCloud()) return;
    for (const a of root.querySelectorAll('a[data-artifact-download]')) {
        bindDownload(a, a.getAttribute('data-artifact-download'), a.getAttribute('data-artifact-name') || a.textContent);
    }
}
