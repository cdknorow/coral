/* Read-only setup checks. Installation commands are displayed, never executed. */
import { serverFetch, normServer } from './server_base.js';
import { fetchSystemStatus } from './system_status.js';
import { setHidden } from './dom_write.js';
const cache = new Map();
const requests = new WeakMap();

function start(element) {
    requests.get(element)?.abort();
    const controller = new AbortController();
    requests.set(element, controller);
    return controller;
}
function current(element, controller) { return element.isConnected && requests.get(element) === controller; }
// Only an https address becomes a link (a server's answer is data, not markup).
const httpsURL = u => typeof u === 'string' && /^https:\/\/[^\s"'<>]+$/.test(u) ? u : '';
/** A CLI that is missing, or a configured path that cannot run, blocks the launch that needs it: the form listens (launch_dialog.js) and keeps its Launch button off, with this message as the reason. */
function blocksLaunch(element, on) {
    if (on) element.dataset.blocksLaunch = '1'; else delete element.dataset.blocksLaunch;
    element.dispatchEvent(new CustomEvent('cli-blocks-launch', { bubbles: true }));
}
function renderCLI(element, text, recheck, {pending = false, command = '', url = '', note = ''} = {}) {
    element.replaceChildren(); element.style.display = '';
    element.setAttribute('role','status'); element.setAttribute('aria-live','polite');
    const label = document.createElement('span'); label.textContent = text; element.append(label);
    // The vendor's own install page first (it does not assume Node and npm); the one-line command is the shortcut for a person who has them.
    if (httpsURL(url)) {
        const p = document.createElement('p'); p.className = 'cli-install-link'; p.append('Install it from the official page: ');
        const a = document.createElement('a'); a.href = url; a.target = '_blank'; a.rel = 'noopener noreferrer'; a.textContent = url.replace(/^https:\/\//, ''); p.append(a); element.append(p);
    }
    if (command) {
        const help = document.createElement('p'); help.textContent = 'Or in your terminal, then check again: ';
        const code = document.createElement('code'); code.textContent = command; help.append(code); element.append(help);
    }
    if (note && typeof note === 'string') { const p = document.createElement('p'); p.className = 'cli-install-note'; p.textContent = String(note); element.append(p); }
    const button = document.createElement('button'); button.type = 'button'; button.className = 'btn btn-small';
    button.textContent = pending ? 'Checking…' : 'Check again'; button.disabled = pending;
    button.addEventListener('click', recheck); element.append(button);
}

export async function checkAgentCLI(type, element, {force = false, command = '', url = '', note = '', server = 'local'} = {}) {
    if (!element) return;
    // Multi-server hub: the CLI must exist on the server the agent will run on.
    server = normServer(server);
    const cacheKey = server === 'local' ? type : `${server}|${type}`;
    const controller = start(element);
    if (type === 'terminal') { element.style.display = 'none'; return; }
    const recheck = () => checkAgentCLI(type, element, {force:true,command,url,note,server});
    const show = data => {
        if (data.found && (data.status === 'probe_failed' || data.status === 'timeout')) {
            renderCLI(element, `${type} CLI found, but version check ${data.status === 'timeout' ? 'timed out' : 'failed'}.`, recheck); blocksLaunch(element, false); return;
        }
        if (data.found) { element.style.display = 'none'; blocksLaunch(element, false); return; }
        // A configured path that cannot be run (#4354): say so, not that the CLI is missing (it is never swapped for the one on PATH).
        if (data.configured && data.message) { renderCLI(element, `${type}: ${data.message}`, recheck); blocksLaunch(element, true); return; }
        blocksLaunch(element, true);
        renderCLI(element, `${type} CLI was not found.`, recheck, {command:data.install_command || command, url:data.install_url || url, note:data.install_note || note});
    };
    if (force) cache.delete(cacheKey);
    const cached = cache.get(cacheKey);
    if (!force && cached && Date.now() - cached.at < 30000) { show(cached.data); return; }
    renderCLI(element, `Checking ${type} CLI…`, recheck, {pending:true});
    try {
        const response = await serverFetch(server, `/api/system/cli-check?type=${encodeURIComponent(type)}${force ? '&source=cli_recheck' : ''}`, {signal:controller.signal,cache:force?'no-store':'default'});
        // Through the Coral Hub the check is a Launch-tier request (it runs the CLI's --version on that computer).
        if (response.status === 403) throw new Error('Needs Launch access on this machine.');
        if (!response.ok) throw new Error(`Could not check ${type} CLI (HTTP ${response.status}).`);
        const data = await response.json();
        if (!data || typeof data.found !== 'boolean' || (data.agent_type && data.agent_type !== type)) throw new Error(`Could not check ${type} CLI: unexpected response.`);
        if (data.status !== undefined && (!['available','missing','probe_failed','timeout'].includes(data.status) || (data.status === 'missing') === data.found)) throw new Error(`Could not check ${type} CLI: inconsistent response.`);
        if (!current(element,controller)) return;
        cache.set(cacheKey,{at:Date.now(),data}); show(data);
    } catch (error) {
        if (!current(element,controller) || error.name === 'AbortError') return;
        renderCLI(element, error instanceof SyntaxError ? `Could not check ${type} CLI: invalid response.` : error.message || `Could not check ${type} CLI.`, recheck);
    }
}

export async function checkTmux(force = false) {
    const banner = document.getElementById('tmux-missing-banner');
    if (!banner) return;
    const controller = start(banner);
    const title = banner.querySelector('strong');
    const message = document.getElementById('tmux-check-message');
    const command = banner.querySelector('.tmux-missing-banner-cmd');
    const button = document.getElementById('tmux-check-again');
    button.disabled = true; button.textContent = 'Checking…';
    try {
        const response = await fetchSystemStatus({fresh:force});
        if (!response.ok) throw new Error(`Could not check tmux (HTTP ${response.status}).`);
        const data = await response.json();
        if (!data || typeof data.tmux_available !== 'boolean') throw new Error('Could not check tmux: unexpected response.');
        if (!current(banner,controller)) return;
        if (data.tmux_available || data.tmux_required === false || data.effective_backend === 'pty') { banner.style.display='none'; return; }
        title.textContent = 'tmux is not installed.';
        message.textContent = 'Install it to use tmux-backed agents.';
        const install = data.tmux_install_command;
        command.hidden = !install;
        document.getElementById('tmux-missing-banner-cmd-text').textContent = install || '';
        banner.style.display = '';
    } catch (error) {
        if (!current(banner,controller) || error.name === 'AbortError') return;
        title.textContent = 'tmux check failed.'; message.textContent = error instanceof SyntaxError ? 'The server returned an invalid response.' : error.message;
        command.hidden = true; banner.style.display = '';
    } finally {
        if (current(banner,controller)) { button.disabled=false;button.textContent='Check again'; }
    }
}

/** First run: tmux missing is said once in the launch form, with the one command for this OS and a Check again; Launch stays off with it as the reason (#6046).
 *  Only where the backend needs tmux; nothing is installed. */
export async function checkTmuxForLaunch(element, {force = false} = {}) {
    if (!element) return;
    const controller = start(element);
    const recheck = () => checkTmuxForLaunch(element, {force:true});
    try {
        const response = await fetchSystemStatus({fresh:force});
        if (!response.ok) throw new Error(`Could not check tmux (HTTP ${response.status}).`);
        const data = await response.json();
        if (!data || typeof data.tmux_available !== 'boolean') throw new Error('Could not check tmux: unexpected response.');
        if (!current(element,controller)) return;
        if (data.tmux_available || data.tmux_required === false || data.effective_backend === 'pty') { element.replaceChildren(); setHidden(element, true); blocksLaunch(element, false); return; }
        blocksLaunch(element, true); setHidden(element, false);
        renderCLI(element, 'tmux is not installed. Coral needs it to run agents.', recheck, {command:data.tmux_install_command || ''});
    } catch (error) {
        if (!current(element,controller) || error.name === 'AbortError') return;
        // A check that failed says nothing about tmux: it does not hold the launch.
        element.replaceChildren(); setHidden(element, true); blocksLaunch(element, false);
    }
}

export function initPrerequisiteChecks() {
    document.getElementById('tmux-check-again')?.addEventListener('click',()=>checkTmux(true));
    checkTmux();
}
