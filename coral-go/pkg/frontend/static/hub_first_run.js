/* Coral Hub's first run (#4586): an account with no machine connected has nothing to launch on. The Agents page says so and offers "Connect a machine" (the Servers tab's Add a machine
 * steps) instead of the launch cards, an empty agent list says it too, and a folder Browse or a launch says "Connect a machine first" instead of failing. noMachine() (launch_server.js)
 * is the one test: the hub, its servers known, none usable. A local Coral never shows any of this. */
import { registerActions } from './actions.js';
import { noMachine } from './launch_server.js';
import { importOnce } from './import_once.js';

/** Show or hide the first-run screens by a class on the page (the welcome screen's connect card and launch cards follow it in CSS). Called whenever the list of servers is read. */
export function syncHubFirstRun() {
    document.body.classList.toggle('hub-no-machine', noMachine());
}

/** The Servers tab at its Add a machine steps, with the enrollment code box ready to type in. */
async function connectMachine() {
    try {
        window.hideLaunchModal?.();
        const m = await importOnce('./cloud_servers.js');
        await m.showCloudServers();
        // The tab puts the focus on its heading when its list is drawn, after this returns: take it from there (no more than about three seconds).
        for (let i = 0; i < 30; i++) {
            await new Promise(r => setTimeout(r, 100));
            const input = document.getElementById('cs-enroll-code');
            if (input && document.activeElement && document.activeElement.id === 'servers-tab-title') { input.focus(); return; }
        }
        document.getElementById('cs-enroll-code')?.focus();
    } catch { /* the Servers tab shows its own errors */ }
}

registerActions({ 'hub-connect-machine': () => { connectMachine(); } });
