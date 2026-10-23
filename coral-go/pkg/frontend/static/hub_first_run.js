/* Coral Hub's first run (#4586): what the Agents page says depends on what the hub's owned-machine list says, once it has loaded (machineState, launch_server.js):
 *   none     no computer is connected: "Connect your computers" (install Coral on each, then Settings > Network and Hub > Connect to Coral Hub) with an Add a computer link (the hub's /machines/add), in place of the launch cards
 *   offline  computers, none usable: "Your computers are offline"
 *   ready    a machine is usable: with no agents, "No agents yet"
 * A list that has not loaded or failed is never shown as the first run, and a local Coral shows none of this. A folder Browse says "Connect a machine first" (or that the machines are
 * offline) instead of failing. Shared agents and boards the account can already see are left as they are: these are only the empty states. */
import { machineState } from './launch_server.js';

/** Show or hide the first-run screens by a class on the page (the welcome screen's cards and launch cards follow it in CSS). Called whenever the list of servers is read. */
export function syncHubFirstRun() {
    const m = machineState();
    document.body.classList.toggle('hub-no-machine', m === 'none');
    document.body.classList.toggle('hub-machines-offline', m === 'offline');
}

/** The empty agent list for a machine state (the same list is the phone's Agents page), or '' when the ordinary empty line applies. Fixed text only. */
export function hubEmptyListHtml(m) {
    if (m === 'none') {
        return '<li class="empty-state hub-connect-empty"><strong>Connect your computers</strong> <span>Install Coral on each computer. In Coral, go to Settings &gt; Network and Hub &gt; Connect to Coral Hub, and the computer appears here.</span> '
            + '<a class="btn btn-primary btn-small" href="/machines/add">Add a computer</a></li>';
    }
    if (m === 'offline') return '<li class="empty-state hub-connect-empty"><strong>Your computers are offline</strong> <span>Keep Coral running on a connected computer to see its agents here.</span></li>';
    if (m === 'ready') return '<li class="empty-state hub-connect-empty"><strong>No agents yet</strong> <span>Start an agent on a connected computer to see it here.</span></li>';
    return '';
}
