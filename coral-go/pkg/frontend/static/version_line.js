/* The running Coral's version at the bottom of the top-bar gear menu ("Coral v1.4.88"), as a small muted line that is not a menu item. It is the
 * version the server reports in GET /api/system/status (the one Settings > About and the update check use), never written into the page; an
 * answer with no version, or none at all, leaves the line out. The menu is built on its first open (#4500), so this runs then. */
import { onBuilt } from './lazy_dom.js';
import { fetchSystemStatus } from './system_status.js';

/** "Coral v1.4.88" for a server version ("1.4.88" or "v1.4.88"); a build name that is not a number ("dev") is shown as it is; '' for none. */
function versionText(v) {
    if (typeof v !== 'string' || !v.trim()) return '';
    const t = v.trim().replace(/^v(?=\d)/i, '');
    return /^\d/.test(t) ? `Coral v${t}` : `Coral ${t}`;
}

async function fill(menu) {
    const row = menu.querySelector('#coral-version-row');
    const line = menu.querySelector('#coral-version');
    if (!row || !line) return;
    try {
        const resp = await fetchSystemStatus();
        if (!resp.ok) return;
        const text = versionText((await resp.json()).version);
        if (!text) return;
        line.textContent = text;
        row.hidden = false;
    } catch { /* no status: no line */ }
}

onBuilt('top-bar-settings-menu', menu => { fill(menu); });
