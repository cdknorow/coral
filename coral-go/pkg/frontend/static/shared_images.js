/* Images pasted or attached in the dashboard are artifacts too (#5284). What Send types into the agent for them (#5481): ONLY each image's coral://artifacts/<hex> link, then one short hint the agent can
 * act on (fetch the link with coral-agent artifact download, then open the path it prints); never the local uploads path. The chat shows the images as artifact thumbnails, not as that text.
 *  - sharedImagesPrefix / attachmentsLead: what Send puts before the person's own text for the pending attachments;
 *  - splitSharedImages: the leading images of a user turn and the text after them. Two shapes: today's "<link> <link> <hint>" and the one before #5481, "<path> coral://artifacts/<hex>" pairs (an upload
 *    path, whitespace, a 64-hex link, repeated), which older turns in a session's history still have. A user's own text that starts with a link (no hint after it), or a link with no upload path before it, is never
 *    taken for one.
 *  - the notice before the first paste after each Coral Hub connect (the images are visible to Hub viewers and cannot be hidden), with the way to turn sharing off;
 *  - the call that removes one image's shared copy. */
import { serverFetch } from './server_base.js';
import { browserSession } from './safe_storage.js';

/** The one hint typed after the image links: the same words every time, so the chat can tell them from a person's text. */
export const IMAGE_HINT = '(Image link(s) above: run coral-agent artifact download <link> and open the path it prints before reading the image.)';

const LINK = String.raw`coral:\/\/artifacts\/[0-9a-f]{64}(?![0-9a-f])`;
const NEW_LEADING = new RegExp(String.raw`^\s*((?:${LINK}\s+)+)${IMAGE_HINT.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}(?:\s+|$)`);
const PAIR = String.raw`(?:(?:\/|[A-Za-z]:[\\\/])[^\n]*?[\\\/]uploads[\\\/]\S+)\s+coral:\/\/artifacts\/[0-9a-f]{64}(?![0-9a-f])`;
const LEADING = new RegExp(String.raw`^\s*(?:${PAIR}(?:\s+|$))+`);
const ONE = new RegExp(String.raw`((?:\/|[A-Za-z]:[\\\/])[^\n]*?[\\\/]uploads[\\\/]\S+)\s+coral:\/\/artifacts\/([0-9a-f]{64})(?![0-9a-f])`, 'g');

/** The leading images of a user turn and the text after them: { images: [{ path, name, hex }], rest }. None: { images: [], rest: text }. */
export function splitSharedImages(text) {
    const s = String(text == null ? '' : text);
    const n = NEW_LEADING.exec(s);
    if (n) {
        const hexes = [...n[1].matchAll(/coral:\/\/artifacts\/([0-9a-f]{64})/g)].map(m => m[1]);
        return { images: hexes.map((hex, i) => ({ path: '', name: hexes.length > 1 ? `Pasted image ${i + 1}` : 'Pasted image', hex })), rest: s.slice(n[0].length).trim() };
    }
    const m = LEADING.exec(s);
    if (!m) return { images: [], rest: s };
    const images = [];
    for (const p of m[0].matchAll(ONE)) images.push({ path: p[1], name: p[1].split(/[\\/]/).pop(), hex: p[2] });
    return { images, rest: s.slice(m[0].length).trim() };
}

/** What Send types for one pending attachment: its artifact link; the local path only when it has no artifact (nothing else to name it by). */
export function attachmentText(att) {
    return att.artifact && att.artifact.uri ? att.artifact.uri : att.path;
}

/** The parts Send types before the person's text, for the pending attachments: each image's text, then the one hint when any of them is a link. */
export function attachmentsLead(atts) {
    const parts = atts.map(attachmentText);
    if (atts.some(a => a.artifact && a.artifact.uri)) parts.push(IMAGE_HINT);
    return parts;
}

const NOTICE_KEY = 'coral-share-notice-connect';

/** Coral Hub's current connection, as { connected, since }: the notice is once for each. */
async function hubConnection() {
    try {
        const resp = await fetch('/api/cloud/status');
        if (!resp.ok) return { connected: false, since: '' };
        const st = await resp.json();
        return { connected: st.state === 'connected', since: String(st.connected_at || ''), share: st.share_pasted_images !== false };
    } catch { return { connected: false, since: '' }; }
}

export async function setShareSwitch(on) {
    const resp = await fetch('/api/cloud/share-pasted-images', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ share: !!on }) });
    return resp.ok;
}

/** Before an image is pasted: when Coral Hub is connected and the person has not been told for this connect, show the notice (in `host`) and wait for the answer. Resolves when it is answered; never rejects. */
export async function ensureShareNotice(host) {
    const hub = await hubConnection();
    if (!hub.connected || !hub.share) return;
    try { if (browserSession.get(NOTICE_KEY) === hub.since) return; } catch { /* storage blocked: ask each time */ }
    await new Promise((resolve) => {
        const box = document.createElement('div');
        box.className = 'share-notice'; box.setAttribute('role', 'alertdialog'); box.setAttribute('aria-labelledby', 'share-notice-title'); box.setAttribute('aria-describedby', 'share-notice-body');
        const title = document.createElement('b'); title.id = 'share-notice-title'; title.textContent = 'Coral Hub is connected. ';
        const body = document.createElement('span'); body.id = 'share-notice-body';
        body.textContent = 'Images you paste are visible to anyone who can view this Coral through Coral Hub, and cannot be hidden.';
        const row = document.createElement('div'); row.className = 'share-notice-actions';
        const off = document.createElement('button'); off.type = 'button'; off.className = 'btn'; off.textContent = "Don't share pasted images";
        const ok = document.createElement('button'); ok.type = 'button'; ok.className = 'btn btn-primary'; ok.textContent = 'OK';
        const done = () => { try { browserSession.set(NOTICE_KEY, hub.since); } catch { /* storage blocked */ } box.remove(); resolve(); };
        off.addEventListener('click', async () => { off.disabled = true; await setShareSwitch(false); done(); });
        ok.addEventListener('click', done);
        row.append(off, ok); box.append(title, body, row);
        host.prepend(box);
        host.style.display = 'flex'; // display-ok: the attachments strip is shown and hidden by controls.js renderAttachments with style.display, so the notice shows the strip too
        ok.focus();
    });
}

/** Remove this agent's shared copy of an image: the Hub's view of it goes with it, the agent's file stays. */
export async function removeSharedCopy(server, sessionId, hex) {
    const resp = await serverFetch(server, `/api/shared-images/${encodeURIComponent(hex)}?session_id=${encodeURIComponent(sessionId)}`, { method: 'DELETE' });
    return resp.ok;
}
