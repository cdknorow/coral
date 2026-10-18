/* Coral Hub's first run (#4586): what the Agents page says depends on what the hub's owned-machine list says, once it has loaded (machineState, launch_server.js):
 *   none     no machine is connected: "Connect your first machine" with a Connect a machine button (the hub's /enroll), in place of the launch cards
 *   offline  machines, none usable: "Your machines are offline"
 *   ready    a machine is usable: with no agents, "No agents yet"
 * A list that has not loaded or failed is never shown as the first run, and a local Coral shows none of this. A folder Browse says "Connect a machine first" (or that the machines are
 * offline) instead of failing. Shared agents and boards the account can already see are left as they are: these are only the empty states. */
import { registerActions } from './actions.js';
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
        return '<li class="empty-state hub-connect-empty"><strong>Connect your first machine</strong> <span>Run Coral on your machine, then connect it to see your agents here.</span> '
            + '<button type="button" class="btn btn-primary btn-small" data-on-click="hub-connect-machine">Connect a machine</button> '
            + '<span>Start the connection in Coral on your machine, then enter its code here.</span></li>';
    }
    if (m === 'offline') return '<li class="empty-state hub-connect-empty"><strong>Your machines are offline</strong> <span>Keep Coral running on a connected machine to see its agents here.</span></li>';
    if (m === 'ready') return '<li class="empty-state hub-connect-empty"><strong>No agents yet</strong> <span>Start an agent on your machine to see it here.</span></li>';
    return '';
}

// The hub's own short-code page: the code Coral shows on the machine is entered there.
registerActions({ 'hub-connect-machine': () => { location.href = '/enroll'; } });
