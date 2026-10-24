/* Polling the server's startup status (moved out of app.js, #5082; no behaviour change). */
import { markReady } from './ready.js';
import { fetchSystemStatus } from './system_status.js';
import { showDatabaseWarning, showFirewallWarning, showSecurityNotice, showSecurityWarning } from './update_check.js';
import { importOnce } from './import_once.js';
import { dashboardSessionsLoaded, dashboardStartupComplete, dashboardFailed } from './dashboard_analytics.js';
import { loadLiveSessions } from './api.js';
import { isCloud } from './server_base.js';
import { startSigninLine } from './signin_line.js';

// window.__coralReady 'liveListLoaded': the page start loads the live list twice, once at
// once and once when the startup check finds the server ready (the two join when they
// overlap). The part is marked when both have settled, loaded or failed, so a test that
// injects state after it can't have a late list answer land on top. A startup check that
// fails, or a page without one, counts as settled for this, so the signal never hangs.
let _startListLoads = 2;

export const _startListLoadSettled = () => { if (--_startListLoads === 0) markReady('liveListLoaded'); };

let _pollListLoadSettled = false;

const _settlePollListLoad = () => { if (!_pollListLoadSettled) { _pollListLoadSettled = true; _startListLoadSettled(); } };

// The server checks its databases a minute after it starts (#4157); a page opened before
// then asks once more a little after, so a damaged database is not only in the log.
const DATABASE_RECHECK_MS = 90_000;

async function recheckDatabases() {
    try {
        const resp = await fetchSystemStatus({ fresh: true });
        if (!resp.ok) return;
        const data = await resp.json();
        if (data && data.database_warning) showDatabaseWarning(data.database_warning);
    } catch { /* not shown; the next page load asks again */ }
}

export function pollStartupStatus() {
    const el = document.getElementById('startup-loading');
    if (!el) { _settlePollListLoad(); return; }
    let firstCheck = true;   // the first answer may be the one the page's other startup callers share; a retry must be fresh
    const check = async () => {
        // The hub has no local startup phase: each machine is its own server, and the merged list
        // fills in as the feed reports them, so there is nothing to wait for here.
        if (isCloud()) {
            el.classList.add('hidden');
            dashboardStartupComplete();
            loadLiveSessions(dashboardFailed).then(dashboardSessionsLoaded).finally(_settlePollListLoad);
            return;
        }
        try {
            const resp = await fetchSystemStatus({ fresh: !firstCheck });
            firstCheck = false;
            if (!resp.ok) { dashboardFailed('status_fetch_http'); _settlePollListLoad(); setTimeout(check, 2000); return; }
            const data = await resp.json();
            if (!data || typeof data.startup_complete !== 'boolean') throw new SyntaxError('Invalid startup status');
            if (data.startup_complete) {
                // The local sign-in banner's strip is settled before the page shows, so the terminal is sized once with it (or without it). Only the
                // startup overlay waits for it (a check that is slow or fails does not hold the page past 1.5 s): the rest of startup, the list load
                // included, keeps the timing it had, so nothing else moves when the check is slow (#4291).
                Promise.race([importOnce('./local_auth_banner.js').then(m => m.startLocalAuthBanner()), new Promise(r => setTimeout(r, 1500))])
                    .catch(() => {}).then(() => el.classList.add('hidden'));
                if (data.security_warning) showSecurityWarning(data.security_warning); // #3637
                if (data.security_notice) showSecurityNotice(data.security_notice); // #295: a folder Coral made private is told once, as a fix
                if (data.firewall_warning) showFirewallWarning(data.firewall_warning); // #97: a broad Windows Firewall rule for Coral's programs
                startSigninLine(data); // #5478: the one calm line while Claude is not signed in
                // The server checks its databases a minute after it starts (#4157): look again then.
                if (data.database_warning) showDatabaseWarning(data.database_warning);
                else setTimeout(recheckDatabases, DATABASE_RECHECK_MS);
                dashboardStartupComplete();
                // One question at a time: set a password (#3948, once, while none is set), then Coral Hub sign-in (#3966, once, for a
                // computer already linked to Coral Hub). The second waits if the first is still open.
                importOnce('./password_ui.js').then(m => m.maybePromptSetPassword()).catch(() => {})
                    .then(() => importOnce('./hub_notice.js')).then(m => m.maybeOfferHubSignin()).catch(() => {});
                loadLiveSessions(dashboardFailed).then(dashboardSessionsLoaded).finally(_settlePollListLoad);
                return;
            }
        } catch (error) { dashboardFailed(error instanceof SyntaxError ? 'status_fetch_invalid' : 'status_fetch_network'); _settlePollListLoad(); }
        setTimeout(check, 2000);
    };
    check();
}
