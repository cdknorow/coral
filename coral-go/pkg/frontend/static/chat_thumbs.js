/* A thumbnail on a chat image chip (#5028, #5032). A chip names the image a user turn carried ("[Image #1]"). Two things can serve it:
 *  - ref: the image's reference in the session's own transcript (Claude's image block, Codex's data URL): GET /api/sessions/live/{name}/attachment/{ref};
 *  - file: the name of an upload on the agent's server: GET /api/upload/{name}.
 * Both give a PNG, JPEG, GIF or WebP (nosniff), shown small and fixed in size, lazily: the request goes out when the chips are near the screen. A reference
 * wins over a file name. Anything that fails (not an upload, not a image Coral serves, through the Hub where neither route is served) leaves the chip as its name.
 * A click opens the image in the Preview pane. */
import { state } from './state.js';
import { escapeAttr } from './utils.js';
import { registerActions } from './actions.js';
import { serverUrl, sessionServer, isCloud } from './server_base.js';
import { fetchImageSrc } from './image_source.js';
import { openChatImageTab } from './preview_pane.js';
import { removeSharedCopy } from './shared_images.js';
import { showConfirmModal } from './confirm_dialog.js';
import { showToast } from './utils.js';

const SIZE = 48; // px, fixed: the chat does not reflow when the image arrives

/** The thumbnail's markup for an attachment ({ref, file}): hidden until the image has loaded. */
export function thumbHtml(att) {
    const artifact = att && /^[0-9a-f]{64}$/.test(att.artifact || '') ? att.artifact : '';
    const ref = !artifact && att && /^[0-9a-f]{32}$/.test(att.ref || '') ? att.ref : '';
    const file = att && att.file ? String(att.file) : '';
    if (!artifact && !ref && !file) return '';
    const label0 = (att && att.name) || file || 'image';
    // An image pasted in the dashboard is named by its place ("Pasted image 2"): read as a sentence, "Open pasted image 2", not "Open Pasted image 2".
    const label = /^Pasted image\b/.test(label0) ? label0.charAt(0).toLowerCase() + label0.slice(1) : label0;
    // An artifact (an image pasted in the dashboard, #5284) is served by the artifact route, locally and through Coral Hub; its owner can remove the shared copy (on this computer only).
    const unshare = artifact && !isCloud() ? `<button type="button" class="chat-attachment-unshare" data-on-click="chat-thumb-unshare" data-artifact="${escapeAttr(artifact)}" aria-label="Remove the shared copy of ${escapeAttr(label)}" title="Remove the shared copy (the image goes from Coral Hub and from this chat)" hidden>&times;</button>` : '';
    return `<button type="button" class="chat-attachment-thumb" data-on-click="chat-thumb-open" data-artifact="${escapeAttr(artifact)}" data-ref="${escapeAttr(ref)}" data-upload="${escapeAttr(artifact || ref ? '' : file)}" ` +
        `aria-label="Open ${escapeAttr(label)}" hidden><img alt="${escapeAttr(file || label)}" width="${SIZE}" height="${SIZE}" decoding="async"></button>${unshare}`;
}

const serverOf = () => (state.currentSession ? sessionServer(state.currentSession) : '');

/** The server-relative route that serves a thumbnail's image: its attachment in the open session's transcript, or an upload by name. */
function routeOf(btn) {
    if (btn.dataset.artifact) return `/api/artifacts/${btn.dataset.artifact}?v=2`;
    if (btn.dataset.ref) {
        const s = state.currentSession;
        if (!s) return '';
        const q = new URLSearchParams();
        if (s.session_id) q.set('session_id', s.session_id);
        if (s.agent_type) q.set('agent_type', s.agent_type);
        // Through the hub the machine resolves the directory from session_id, and the route table refuses an absolute path (as the chat request does).
        if (s.working_directory && !(isCloud() && serverOf() !== 'local')) q.set('working_directory', s.working_directory);
        return `/api/sessions/live/${encodeURIComponent(s.name)}/attachment/${btn.dataset.ref}?${q}`;
    }
    return btn.dataset.upload ? '/api/upload/' + encodeURIComponent(btn.dataset.upload) : '';
}

async function load(btn) {
    if (btn.dataset.loaded) return;
    btn.dataset.loaded = '1';
    const path = routeOf(btn);
    if (!path) return;
    try {
        const { src, revoke } = await fetchImageSrc(serverUrl(path, serverOf()), { name: btn.dataset.ref || btn.dataset.artifact ? undefined : btn.dataset.upload });
        const img = btn.querySelector('img');
        img.addEventListener('load', () => { btn.hidden = false; const x = btn.nextElementSibling; if (x && x.classList.contains('chat-attachment-unshare')) x.hidden = false; if (revoke) URL.revokeObjectURL(src); }, { once: true });
        img.addEventListener('error', () => { if (revoke) URL.revokeObjectURL(src); }, { once: true });
        img.src = src;
    } catch { /* not served, or not an image Coral serves: the chip keeps its name */ }
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
    'chat-thumb-unshare': el => {
        const x = el.closest('.chat-attachment-unshare'), s = state.currentSession;
        if (!x || !s || !s.session_id) return;
        showConfirmModal('Remove the shared copy?', 'The image is removed from Coral Hub and from the chat here. The agent keeps its own file.', async () => {
            if (await removeSharedCopy(serverOf(), s.session_id, x.dataset.artifact).catch(() => false)) {
                const thumb = x.previousElementSibling;
                if (thumb) thumb.hidden = true;
                x.hidden = true; // the chip with the file's name stays
            } else showToast('Could not remove the shared copy.', true);
        });
    },
    'chat-thumb-open': el => {
        const btn = el.closest('.chat-attachment-thumb');
        const path = btn && routeOf(btn);
        if (path) openChatImageTab(btn.querySelector('img').alt || 'image', path, serverOf());
    },
});
