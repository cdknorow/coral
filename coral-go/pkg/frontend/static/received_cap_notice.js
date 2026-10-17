/* The one-time notice that files received from other teams and Corals are at 80% of their limit (#4441, Security's #4438 SHOULD 2). The server says when it is
 * due (GET /api/system/artifact-store, received.notice) and remembers the answer: once told, it is told again only for a different limit. This computer's own
 * dashboard only: through Coral Hub there is no store to read and the Operator is not there. */
import { showToast } from './utils.js';
import { isCloud } from './server_base.js';

let asking = false;

export async function checkReceivedCap() {
    if (isCloud() || asking || document.body.classList.contains('popout-mode')) return;
    asking = true;
    try {
        const r = await fetch('/api/system/artifact-store');
        if (!r.ok) return;
        const rec = (await r.json()).received;
        if (!rec || !rec.notice) return;
        // Remember first, so a second tab or a reload cannot show it again; then say it.
        const ack = await fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ received_cap_notice_ack: String(rec.max_bytes) }) });
        if (!ack.ok) return;
        const gib = n => `${(n / 1024 ** 3).toFixed(1)} GiB`;
        showToast(`Files received from other teams take ${gib(rec.bytes)}, 80% of the ${gib(rec.max_bytes)} limit. Past it new received files are refused. Raise the limit in Settings > Storage.`, false, 15000);
    } catch { /* nothing to say */ } finally { asking = false; }
}
