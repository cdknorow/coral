/* Artifacts in board messages (#3515). An agent's post can link an artifact
 * (coral://artifacts/<64 hex>, exactly: Coral writes these when it attaches a file the
 * agent linked). The sanitizer already keeps only such images; here they and the links are
 * pointed at the board's own server, through Coral Hub too, and a link opens the artifact
 * in a new tab (served with a sandbox CSP; images inline, other types as a download). */
import { serverUrl, artifactPath } from './server_base.js';
import { artifactRefHex } from './utils.js';

export function resolveBoardArtifacts(root, server) {
    if (!root) return;
    for (const img of root.querySelectorAll('.mb-message-body img[data-artifact]')) {
        const hex = artifactRefHex(`coral://artifacts/${img.dataset.artifact}`);
        if (hex) img.src = serverUrl(artifactPath(hex), server);
        else img.remove();
    }
    for (const a of root.querySelectorAll('.mb-message-body a[href]')) {
        const hex = artifactRefHex(a.getAttribute('href'));
        if (!hex) continue;
        a.href = serverUrl(artifactPath(hex), server);
        a.target = '_blank';
        a.rel = 'noopener noreferrer';
        a.dataset.artifact = hex;
        a.classList.add('board-artifact-link');
    }
}
