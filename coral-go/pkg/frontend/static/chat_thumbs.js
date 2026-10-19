/* A thumbnail on a chat image chip (#5028). A chip names the image a user turn carried (Codex's "[Image #1]" with the file it saved); when that file is an upload on
 * the agent's server, GET /api/upload/{name} serves it (a base name only, PNG, JPEG, GIF or WebP by content, nosniff) and the chip shows it small and fixed in size,
 * lazily: the request goes out when the chip is near the screen. A file that is not an upload (a 404) leaves the chip as the name alone. A click opens the image in the
 * Preview pane. */
import { state } from './state.js';
import { escapeAttr } from './utils.js';
import { registerActions } from './actions.js';
import { serverUrl, sessionServer } from './server_base.js';
import { fetchImageSrc } from './image_source.js';
import { openUploadTab } from './preview_pane.js';

const SIZE = 48; // px, fixed: the chat does not reflow when the image arrives

/** The thumbnail's markup for an attachment's file name: hidden until the image has loaded. */
export function thumbHtml(file) {
    if (!file) return '';
    return `<button type="button" class="chat-attachment-thumb" data-on-click="chat-thumb-open" data-upload="${escapeAttr(file)}" aria-label="Open ${escapeAttr(file)}" hidden>` +
        `<img alt="${escapeAttr(file)}" width="${SIZE}" height="${SIZE}" decoding="async"></button>`;
}

const serverOf = () => (state.currentSession ? sessionServer(state.currentSession) : '');
const uploadUrl = (file, server) => serverUrl('/api/upload/' + encodeURIComponent(file), server);

async function load(btn) {
    if (btn.dataset.loaded) return;
    btn.dataset.loaded = '1';
    try {
        const { src, revoke } = await fetchImageSrc(uploadUrl(btn.dataset.upload, serverOf()), { name: btn.dataset.upload });
        const img = btn.querySelector('img');
        img.addEventListener('load', () => { btn.hidden = false; if (revoke) URL.revokeObjectURL(src); }, { once: true });
        img.addEventListener('error', () => { if (revoke) URL.revokeObjectURL(src); }, { once: true });
        img.src = src;
    } catch { /* not an upload, or not an image Coral serves: the chip keeps its name */ }
}

// The chips' row is what is watched: a thumbnail is hidden until its image has loaded, and a hidden element is never "intersecting".
const loadRow = row => { for (const btn of row.querySelectorAll('.chat-attachment-thumb')) load(btn); };
const seen = typeof IntersectionObserver === 'function' ? new IntersectionObserver(entries => {
    for (const e of entries) if (e.isIntersecting) { seen.unobserve(e.target); loadRow(e.target); }
}, { rootMargin: '200px' }) : null;

/** Called with a bubble once it is built: each of its thumbnails loads when it comes near the screen. */
export function wireThumbs(bubble) {
    for (const row of bubble.querySelectorAll('.chat-attachments')) { if (seen) seen.observe(row); else loadRow(row); }
}

registerActions({
    'chat-thumb-open': el => { const b = el.closest('[data-upload]'); if (b && b.dataset.upload) openUploadTab(b.dataset.upload, serverOf()); },
});
