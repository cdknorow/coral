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

export function resolveBoardArtifacts(root, server) {
    if (!root) return;
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
