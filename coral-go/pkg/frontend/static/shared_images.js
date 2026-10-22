/* Images pasted or attached in the dashboard are artifacts too (#5284). What Send types into the agent for them (#5481): ONLY each image's coral://artifacts/<hex> link, then one hint the agent can
 * act on: the exact coral-agent artifact download command for each link (#5496), then open the path it prints; never the local uploads path. The chat shows the images as artifact thumbnails, not as that text.
 *  - sharedImagesPrefix / attachmentsLead: what Send puts before the person's own text for the pending attachments;
 *  - splitSharedImages: the leading images of a user turn and the text after them. Two shapes: today's "<link> <link> <hint>" and the one before #5481, "<path> coral://artifacts/<hex>" pairs (an upload
 *    path, whitespace, a 64-hex link, repeated), which older turns in a session's history still have. A user's own text that starts with a link (no hint after it), or a link with no upload path before it, is never
 *    taken for one.
 *  - the call that removes one image's shared copy. */
import { serverFetch } from './server_base.js';

/** The one hint typed after the image links (#5496): the exact command that fetches each of them, with the real link filled in, then what to do with its output. The same words every time, so the chat can tell
 *  them from a person's text; an agent that has never seen a coral:// link can act on it as it stands. */
export function imageHint(uris) {
    return `(To read the image(s) above, run first: ${uris.map(u => `coral-agent artifact download ${u}`).join(' ; ')} and open the path each prints.)`;
}

const LINK = String.raw`coral:\/\/artifacts\/[0-9a-f]{64}(?![0-9a-f])`;
const HINT = String.raw`\(To read the image\(s\) above, run first: ((?:coral-agent artifact download ${LINK}(?: ; )?)+) and open the path each prints\.\)`;
const NEW_LEADING = new RegExp(String.raw`^\s*((?:${LINK}\s+)+)${HINT}(?:\s+|$)`);
const HEXES = /coral:\/\/artifacts\/([0-9a-f]{64})/g;
const PAIR = String.raw`(?:(?:\/|[A-Za-z]:[\\\/])[^\n]*?[\\\/]uploads[\\\/]\S+)\s+coral:\/\/artifacts\/[0-9a-f]{64}(?![0-9a-f])`;
const LEADING = new RegExp(String.raw`^\s*(?:${PAIR}(?:\s+|$))+`);
const ONE = new RegExp(String.raw`((?:\/|[A-Za-z]:[\\\/])[^\n]*?[\\\/]uploads[\\\/]\S+)\s+coral:\/\/artifacts\/([0-9a-f]{64})(?![0-9a-f])`, 'g');

/** The leading images of a user turn and the text after them: { images: [{ path, name, hex }], rest }. None: { images: [], rest: text }. */
export function splitSharedImages(text) {
    const s = String(text == null ? '' : text);
    const n = NEW_LEADING.exec(s);
    const hexes = n ? [...n[1].matchAll(HEXES)].map(m => m[1]) : [];
    // The commands must name exactly the links above them, in order: any other text that merely looks like the hint is a person's own.
    if (n && JSON.stringify(hexes) === JSON.stringify([...n[2].matchAll(HEXES)].map(m => m[1]))) {
        return { images: hexes.map((hex, i) => ({ path: '', name: hexes.length > 1 ? `Pasted image ${i + 1}` : 'Pasted image', hex })), rest: s.slice(n[0].length).trim() };
    }
    const m = LEADING.exec(s);
    if (!m) return { images: [], rest: s };
    const images = [];
    for (const p of m[0].matchAll(ONE)) images.push({ path: p[1], name: p[1].split(/[\\/]/).pop(), hex: p[2] });
    return { images, rest: s.slice(m[0].length).trim() };
}

const TOAST_CHARS = 28;

/** What the "Sent" toast says: the person's own words, short. The image links and the hint after them (for the agent) are left out, only the first line is kept and it is cut at 28 characters (one line at 320 px); with no words (images only) it is
 *  just "Sent". A toast that carried the whole message covered a phone screen (#6137). */
export function sentToastText(command) {
    const rest = splitSharedImages(command).rest;
    const line = String(rest).split(/\r?\n/).map(l => l.trim()).find(Boolean) || '';
    if (!line) return 'Sent';
    return 'Sent: ' + (line.length > TOAST_CHARS ? line.slice(0, TOAST_CHARS - 1).trimEnd() + '\u2026' : line);
}

/** What Send types for one pending attachment: its artifact link; the local path only when it has no artifact (nothing else to name it by). */
export function attachmentText(att) {
    return att.artifact && att.artifact.uri ? att.artifact.uri : att.path;
}

/** The parts Send types before the person's text, for the pending attachments: each image's text, then the one hint when any of them is a link. */
export function attachmentsLead(atts) {
    const parts = atts.map(attachmentText);
    const uris = atts.filter(a => a.artifact && a.artifact.uri).map(a => a.artifact.uri);
    if (uris.length) parts.push(imageHint(uris));
    return parts;
}

/** Remove this agent's shared copy of an image: the Hub's view of it goes with it unless another agent was given the same bytes, the agent's file stays.
 *  null when it was not removed; otherwise { artifactRemoved }: true when the image itself went too, false when another agent's record keeps it, undefined when the answer did not say. */
export async function removeSharedCopy(server, sessionId, hex) {
    const resp = await serverFetch(server, `/api/shared-images/${encodeURIComponent(hex)}?session_id=${encodeURIComponent(sessionId)}`, { method: 'DELETE' });
    if (!resp.ok) return null;
    let body = null;
    try { body = await resp.json(); } catch { /* an answer with no body: removed, nothing more said */ }
    return { artifactRemoved: body && typeof body.artifact_removed === 'boolean' ? body.artifact_removed : undefined };
}

/** What the person is told once their shared copy is removed: whether the image is gone from Coral Hub too, or stays because another agent was given the same image. */
export function removedCopyText(result) {
    if (result && result.artifactRemoved === false) return 'Removed from this chat. The same image is also shared with another agent, so it stays there.';
    if (result && result.artifactRemoved === true) return 'Removed from this chat and from Coral Hub.';
    return 'Removed from this chat.';
}
