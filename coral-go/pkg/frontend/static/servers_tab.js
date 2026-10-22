/* Servers tab (#3070): the top-level place to see and manage the machines this
 * dashboard reaches. On the Cloud Hub it lists the enrolled machines
 * (cloud_servers.js); on a self-hosted hub, the registered remotes (servers.js).
 * The tab shows when there is something to manage: always on the Cloud Hub,
 * and on a local Coral once a remote server is configured. Deep links go to a
 * row: /servers/<id>, the server chips in the agent list, and the "Open
 * Servers" action of a refused send. */

import { importOnce } from './import_once.js';
import { state } from './state.js';
import { isCloud, isHub, normServer, LOCAL_SERVER } from './server_base.js';

/** Whether the Servers tab has anything to show. */
 function serversTabAvailable() {
    if (isCloud()) return true;
    if (!isHub()) return false;
    return (state.servers || []).some(s => s && s.id && normServer(s.id) !== LOCAL_SERVER);
}

/** Show or hide the top-nav tab. Safe to call often (after each list update). */
export function updateServersTabVisibility() {
    const tab = document.getElementById('nav-tab-servers');
    if (!tab) return;
    const show = serversTabAvailable();
    if (tab.hidden === !show) return;
    tab.hidden = !show;
    // Leaving a tab that no longer applies (the last remote was removed).
    if (!show && tab.classList.contains('active') && window.switchNavTab) window.switchNavTab('agents');
}

let _active = false;

/** Called by switchNavTab('servers'): render the right list into the tab. */
export async function showServersTabContent(focusId) {
    const body = document.getElementById('servers-tab-body');
    if (!body) return;
    _active = true;
    if (isCloud()) {
        importOnce('./hub_nav.js').then(n => n.loadHubNav()).catch(() => {});
        const m = await importOnce('./cloud_servers.js', 'the server list');
        await m.renderCloudServersInto(body);
    } else {
        const m = await importOnce('./servers.js');
        await m.renderServersInto(body);
    }
    if (!_active) return;
    focusRow(focusId);
}

/** Called when another tab is selected: stop the list refresh. */
export async function leaveServersTab() {
    if (!_active) return;
    _active = false;
    if (isCloud()) (await importOnce('./cloud_servers.js')).hideCloudServers();
    else (await importOnce('./servers.js')).hideServersModal();
}

async function focusRow(focusId) {
    const heading = document.getElementById('servers-tab-title');
    if (!focusId) { if (heading) heading.focus(); return; }
    const id = 'server-row-' + String(focusId).replace(/[^A-Za-z0-9_-]/g, '_');
    if (isCloud()) (await importOnce('./cloud_servers.js')).expandServer(focusId); // a deep link opens the machine's details
    const row = document.getElementById(id) || document.getElementById('server-row-' + focusId);
    if (!row) { if (heading) heading.focus(); return; }
    row.scrollIntoView({ block: 'center' });
    row.classList.add('server-row-target');
    setTimeout(() => row.classList.remove('server-row-target'), 2500);
    row.focus();
}

/** Open the Servers tab, at one server's row when `focusId` is given. Nothing
 *  happens where the tab does not apply (a local Coral with no remotes). */
export async function openServersTab(focusId, { fromHistory = false } = {}) {
    if (!serversTabAvailable()) return false;
    if (window.switchNavTab) window.switchNavTab('servers', { focusId, fromHistory });
    return true;
}

// Server chips in the agent list (and its phone clone) open the tab at their row.
document.addEventListener('click', (e) => {
    const chip = e.target.closest && e.target.closest('[data-server-open]');
    if (!chip) return;
    e.preventDefault();
    openServersTab(chip.dataset.serverOpen);
});
