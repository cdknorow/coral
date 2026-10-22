/* Settings > Run as a hub (#3460): turns this Coral into a multi-server hub (--hub) at its
 * next start. The setting is saved at once through /api/system/hub-mode, which answers only
 * the dashboard on this computer (never Coral Hub, never another local user). Where the
 * process can restart itself (the desktop apps) a Restart button does it; otherwise the text
 * says to restart Coral. --hub or CORAL_HUB=1 keep it on whatever is saved. */

const URL_MODE = '/api/system/hub-mode';
const $ = id => document.getElementById(id);

let _wired = false;
let _last = null;

function render(st, error = '') {
    const box = $('settings-hub-mode-enabled');
    const status = $('settings-hub-mode-status');
    const restart = $('settings-hub-mode-restart');
    if (!box || !status || !restart) return;
    if (!st) {
        box.disabled = true;
        status.textContent = error || 'Not available here.';
        restart.hidden = true;
        return;
    }
    _last = st;
    box.checked = !!(st.saved || st.forced);
    box.disabled = !!st.forced;
    let text;
    if (st.forced) {
        text = 'On: Coral was started with --hub (or CORAL_HUB=1). To turn it off, start Coral without it' +
            (st.saved ? ' and clear this setting.' : '.');
    } else if (st.restart_required) {
        text = (st.saved ? 'Saved: this Coral becomes a hub' : 'Saved: this Coral stops being a hub') +
            (st.can_restart ? ' when it restarts.' : ' when it restarts. Quit Coral and start it again to apply.');
    } else {
        text = st.running ? 'On: this Coral is a hub (the Servers tab).' : '';
    }
    status.textContent = error ? error + ' ' + text : text;
    restart.hidden = !(st.restart_required && st.can_restart && !st.forced);
}

async function call(method, body) {
    const opts = { method, headers: {} };
    if (body !== undefined) { opts.headers['Content-Type'] = 'application/json'; opts.body = JSON.stringify(body); }
    const r = await fetch(method === 'POST' ? '/api/system/restart' : URL_MODE, opts);
    let data = null; try { data = await r.json(); } catch { /* empty */ }
    if (!r.ok) throw new Error((data && data.error) || `HTTP ${r.status}`);
    return data;
}

// The new server comes back with the saved mode: then reload the dashboard.
async function waitForRestart(wantHub) {
    const status = $('settings-hub-mode-status');
    const end = Date.now() + 90000;
    let wentDown = false;
    while (Date.now() < end) {
        await new Promise(res => setTimeout(res, 1000)); // sleep-ok: polling a restarting server
        try {
            const r = await fetch('/api/health', { cache: 'no-store' });
            const h = r.ok ? await r.json() : null;
            if (h && wentDown && !!h.hub === wantHub) { location.reload(); return; }
            if (!h) wentDown = true;
        } catch { wentDown = true; }
    }
    if (status) status.textContent = 'Coral has not come back yet. If it does not, start it again.';
}

/** Load the current state into the section (called when Settings opens). */
export async function loadHubModeSetting() {
    wire();
    try { render(await call('GET')); } catch (e) { render(null, `Could not load: ${e.message}`); }
}

function wire() {
    if (_wired) return;
    const box = $('settings-hub-mode-enabled');
    const restart = $('settings-hub-mode-restart');
    if (!box || !restart) return;
    _wired = true;
    box.addEventListener('change', async () => {
        const want = box.checked;
        box.disabled = true;
        try { render(await call('PUT', { enabled: want })); }
        catch (e) { box.checked = !want; render(_last, `Not saved: ${e.message}.`); }
        finally { if (_last && !_last.forced) box.disabled = false; }
    });
    restart.addEventListener('click', async () => {
        restart.disabled = true;
        try {
            await call('POST', {});
            $('settings-hub-mode-status').textContent = 'Restarting Coral…';
            waitForRestart(!!(_last && (_last.saved || _last.forced)));
        } catch (e) {
            restart.disabled = false;
            render(_last, `Could not restart: ${e.message}.`);
        }
    });
}
