/* Image sources for the Cloud Hub.
 *
 * Through the hub an <img src="/api/remote/..."> cannot be used for every file: the URL may not carry a cache-buster
 * the machine's route table does not know, and the hub's response filter turns SVG (which can carry script) into a
 * download. So in cloud mode the bytes are fetched (same-origin, no-store) and handed to the <img> as a blob: URL, and
 * SVG is fetched as TEXT and shown as a data: URL. An SVG loaded by an <img> never runs script, loads sub-resources
 * or reaches the page; neither innerHTML, inline <svg>, <object> nor <embed> is ever used for it. */

const MAX_IMAGE_BYTES = 10 * 1024 * 1024;
const MAX_SVG_BYTES = 2 * 1024 * 1024;

const MIME_BY_EXT = {
    png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg', gif: 'image/gif', webp: 'image/webp',
    avif: 'image/avif', bmp: 'image/bmp', ico: 'image/x-icon',
};

/** True for a name or media type that denotes SVG. */
export function isSvg(nameOrType) {
    return /\.svgz?$/i.test(nameOrType || '') || /^image\/svg\+xml\b/i.test(nameOrType || '');
}

/** An <img>-safe data: URL for SVG text. */
export function svgDataUrl(text) {
    return 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(text);
}

/** The file name a preview URL points at: its `filepath` query parameter, else its last path segment. */
export function urlFileName(url) {
    try {
        const u = new URL(url, window.location.href);
        return u.searchParams.get('filepath') || decodeURIComponent(u.pathname.split('/').pop() || '');
    } catch { return String(url || ''); }
}

async function readLimited(resp, max) {
    const declared = Number(resp.headers.get('Content-Length'));
    if (Number.isFinite(declared) && declared > max) throw new Error('too large');
    const buf = await resp.arrayBuffer();
    if (buf.byteLength > max) throw new Error('too large');
    return buf;
}

/** Fetch an image (or SVG) through the hub and return a URL an <img> can show.
 *  `name` picks SVG handling; it defaults to the file name in `url`. */
export async function fetchImageSrc(url, { name, mediaType, signal } = {}) {
    const resp = await fetch(url, { cache: 'no-store', signal });
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    if (isSvg(name || urlFileName(url)) || isSvg(mediaType)) {
        const text = new TextDecoder().decode(await readLimited(resp, MAX_SVG_BYTES));
        return { src: svgDataUrl(text), revoke: false };
    }
    const ext = (/\.([a-z0-9]+)$/i.exec(name || urlFileName(url)) || [])[1];
    const type = MIME_BY_EXT[(ext || '').toLowerCase()] || ((resp.headers.get('Content-Type') || '').startsWith('image/') ? resp.headers.get('Content-Type').split(';')[0] : 'image/png');
    const blob = new Blob([await readLimited(resp, MAX_IMAGE_BYTES)], { type });
    return { src: URL.createObjectURL(blob), revoke: true };
}
