/* Artifacts in board messages (#3515). An agent's post can link an artifact
 * (coral://artifacts/<64 hex>, exactly: Coral writes these when it attaches a file the
 * agent linked). The sanitizer already keeps only such images; here they and the links are
 * pointed at the board's own server, through Coral Hub too. A plain click on a link or an
 * image opens the artifact in the dashboard's viewer (#3540: Markdown is previewed, not
 * downloaded); a modified or middle click keeps the link's new tab (served with a sandbox
 * CSP; images inline, other types as a download). */
import { serverUrl, artifactPath } from './server_base.js';
import { artifactRefHex } from './utils.js';
import { importOnce } from './import_once.js';

/** Whether a click is a plain left click the viewer should take over. */
function plainClick(e) {
    return !e.defaultPrevented && e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey;
}

function openInViewer(e, hex, server, filename) {
    if (!plainClick(e)) return;
    e.preventDefault();
    importOnce('./artifact_viewer.js').then(m => m.openArtifactViewer(hex, { server, filename }), () => {
        // The viewer could not load: fall back to the artifact itself.
        window.open(serverUrl(artifactPath(hex), server), '_blank', 'noopener,noreferrer');
    });
}

/** A link or image in a message from another Coral whose file did not come with it (#5503): muted text and a note, not a link that opens nothing. */
function markNotHere(el, label) {
    const note = document.createElement('span');
    note.className = 'mb-not-here';
    note.title = 'This file was not sent with the message, so it is not stored on this computer.';
    note.append(document.createTextNode(label), Object.assign(document.createElement('span'), { className: 'mb-not-here-note', textContent: ' (not on this machine)' }));
    el.replaceWith(note);
}

export function resolveBoardArtifacts(root, server) {
    if (!root) return;
    for (const msg of root.querySelectorAll('.mb-message[data-inbound]')) {
        const here = new Set((msg.dataset.attached || '').split(' ').filter(Boolean));
        for (const img of msg.querySelectorAll('.mb-message-body img[data-artifact]')) {
            const hex = artifactRefHex(`coral://artifacts/${img.dataset.artifact}`);
            if (hex && !here.has(hex)) markNotHere(img, img.getAttribute('alt') || 'image');
        }
        for (const a of msg.querySelectorAll('.mb-message-body a[href]')) {
            const hex = artifactRefHex(a.getAttribute('href'));
            if (hex && !here.has(hex)) markNotHere(a, a.textContent.trim() || 'file');
        }
    }
    for (const chip of root.querySelectorAll('.mb-attachment[data-attachment]')) {
        const hex = artifactRefHex(`coral://artifacts/${chip.dataset.attachment}`);
        if (!hex) { chip.remove(); continue; }
        chip.href = serverUrl(artifactPath(hex), server);
        chip.target = '_blank';
        chip.rel = 'noopener noreferrer';
        const thumb = chip.querySelector('img[data-attachment-img]');
        if (thumb && !thumb.getAttribute('src')) {
            // a thumbnail that cannot be drawn (the file is gone, or not an image after all) leaves the chip a file chip, not an empty box
            thumb.addEventListener('error', () => {
                const icon = Object.assign(document.createElement('span'), { className: 'material-icons mb-attachment-icon', textContent: 'image' });
                icon.setAttribute('aria-hidden', 'true');
                thumb.replaceWith(icon);
                chip.classList.remove('is-image');
            }, { once: true });
            thumb.src = chip.href;
        }
        if (!chip.dataset.viewerBound) {
            chip.dataset.viewerBound = '1';
            chip.addEventListener('click', e => openInViewer(e, hex, server, chip.dataset.filename || ''));
        }
    }
    for (const img of root.querySelectorAll('.mb-message-body img[data-artifact]')) {
        const hex = artifactRefHex(`coral://artifacts/${img.dataset.artifact}`);
        if (!hex) { img.remove(); continue; }
        img.src = serverUrl(artifactPath(hex), server);
        img.classList.add('board-artifact-image');
        if (!img.dataset.viewerBound) {
            img.dataset.viewerBound = '1';
            img.addEventListener('click', e => openInViewer(e, hex, server, img.getAttribute('alt') || ''));
        }
    }
    for (const a of root.querySelectorAll('.mb-message-body a[href]')) {
        const hex = artifactRefHex(a.getAttribute('href'));
        if (!hex) continue;
        a.href = serverUrl(artifactPath(hex), server);
        a.target = '_blank';
        a.rel = 'noopener noreferrer';
        a.dataset.artifact = hex;
        a.classList.add('board-artifact-link');
        if (!a.dataset.viewerBound) {
            a.dataset.viewerBound = '1';
            a.addEventListener('click', e => openInViewer(e, hex, server, a.textContent.trim()));
        }
    }
}
