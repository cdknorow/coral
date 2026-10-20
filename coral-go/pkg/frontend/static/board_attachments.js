/* Files that came with a cross-team message (#5503, the UI for #5501/#5506). A message that arrived from another Coral lists, in its envelope, the files stored on this computer for it
 * (external.attachments: id, name, media_type, size). They show under the text as an image thumbnail or a file chip, and open in the artifact viewer like any board artifact. A
 * coral://artifacts link in the text of such a message whose file is not in that list was not sent with it: it is shown as muted text saying "not on this machine", not as a link that
 * opens nothing (board_artifacts.js does that swap, from the data attributes this module puts on the message). Messages that are not from another Coral are not touched. */
import { escapeHtml, escapeAttr } from './html_escape.js';
import { artifactRefHex } from './markdown.js';

const MAX_SHOWN = 50;

/** The envelope of a message that arrived from another Coral (not the sender's own "to <board>" mirror), else null. */
function inboundEnvelope(m) {
    const ext = m && m.external;
    // From another Coral only: a message between two boards of THIS Coral carries the same inbound envelope with server_id "local" (internal/board/cross_team.go), and its files are in this
    // Coral's own store, so its links keep working and it has no attachments list.
    return ext && typeof ext === 'object' && ext.direction !== 'outbound' && ext.server_id && ext.server_id !== 'local' ? ext : null;
}

/** The message's files as {hex, name, type, size}: only entries with a real 64-hex id, at most 50. The server's own words (name, media type) are text and are escaped where shown. */
export function messageAttachments(m) {
    const ext = inboundEnvelope(m);
    const list = ext && Array.isArray(ext.attachments) ? ext.attachments : [];
    const out = [];
    for (const a of list) {
        const hex = a && typeof a === 'object' ? artifactRefHex(`coral://artifacts/${a.id}`) : '';
        if (!hex) continue;
        out.push({ hex, name: String(a.name || 'file').slice(0, 200), type: String(a.media_type || '').split(';')[0].trim().toLowerCase(), size: Number(a.size) });
        if (out.length >= MAX_SHOWN) break;
    }
    return out;
}

/** Attributes for the message's wrapper: it is from another Coral, and these are its files (the hexes), so a link to any other artifact can be marked "not on this machine". */
export function messageAttachmentAttrs(m) {
    if (!inboundEnvelope(m)) return '';
    return ` data-inbound="1" data-attached="${escapeAttr(messageAttachments(m).map(a => a.hex).join(' '))}"`;
}

function sizeLabel(size) {
    if (!Number.isFinite(size) || size < 0) return '';
    if (size < 1024) return `${size} B`;
    if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
    return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

/** The chips under the message text, or ''. Each is a link to the artifact (board_artifacts.js points it at this server and opens the viewer on a plain click). */
export function messageAttachmentsHtml(m) {
    const files = messageAttachments(m);
    if (!files.length) return '';
    const chips = files.map(f => {
        const size = sizeLabel(f.size);
        const label = `${f.name}${size ? ', ' + size : ''}`;
        const body = f.type.startsWith('image/')
            ? `<img class="mb-attachment-thumb" alt="" loading="lazy" data-attachment-img="${escapeAttr(f.hex)}"><span class="mb-attachment-name">${escapeHtml(f.name)}</span>`
            : `<span class="material-icons mb-attachment-icon" aria-hidden="true">description</span><span class="mb-attachment-name">${escapeHtml(f.name)}</span>${size ? `<span class="mb-attachment-size">${escapeHtml(size)}</span>` : ''}`;
        return `<a class="mb-attachment${f.type.startsWith('image/') ? ' is-image' : ''}" href="#" data-attachment="${escapeAttr(f.hex)}" data-filename="${escapeAttr(f.name)}" aria-label="${escapeAttr('Open ' + label)}" title="${escapeAttr(label)}">${body}</a>`;
    }).join('');
    return `<div class="mb-attachments" role="group" aria-label="Files sent with this message">${chips}</div>`;
}
