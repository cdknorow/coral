/* Controls the Coral Hub's access tier does not allow (#3769). Through the Hub a machine can be limited to View
 * or Interact: the server refuses what needs more, but a control that looks available and answers with a
 * "Blocked" toast only when clicked is misleading. Each control that needs Interact (send, keys, macros, Kill,
 * Rename, Sleep, Archive, ...) or Launch (Restart, Wake) is dimmed and gets the reason as its title when its
 * agent's machine is below that tier. It stays clickable: the click still says why (hubBlocked, or the
 * hub's refusal). Which tier each needs follows the hub's route table (pkg/tunnel/routes.go).
 *
 * Agent rows and their menus use that row's machine; the command pane and the sleeping bar use the agent on
 * screen, which is also marked read-only when the hub told the terminal socket so. Controls are found by their
 * action name after any redraw, so the many places that draw them need no change. Not on a local dashboard. */
import { state } from './state.js';
import { isCloud, sessionServer } from './server_base.js';
import { blockedReason } from './hub_access.js';
import { pollEvery } from './overload.js';

const INTERACT = new Set(['cmd-send', 'cmd-send-menu', 'cmd-send-team', 'send-menu-send', 'send-menu-team', 'send-menu-resend',
    'send-menu-protocol', 'cmd-quick', 'cmd-raw-keys', 'cmd-macro', 'macro-show', 'macro-delete', 'cmd-mode-toggle',
    'agent-kill', 'agent-rename', 'agent-icon', 'agent-sleep', 'agent-archive', 'agent-auto-accept']);
const LAUNCH = new Set(['agent-restart', 'agent-attach']);
// The sleeping bar's buttons name their action in data-action.
const BAR = { wake: 'launch', restart: 'launch', unarchive: 'interact' };

export function tierOf(el) {
    if (el.closest('#sleeping-composer')) return BAR[el.dataset.action] || '';
    if (el.classList.contains('mobile-send-btn')) return 'interact';
    const a = el.dataset.onClick;
    return INTERACT.has(a) ? 'interact' : LAUNCH.has(a) ? 'launch' : '';
}

function serverOf(el) {
    const row = el.closest('[data-session-id]');
    const id = (row && row.dataset.sessionId) || el.dataset.sessionId;
    if (id) {
        const s = (state.liveSessions || []).find(x => x.session_id === id);
        if (s) return sessionServer(s);
    }
    return state.currentSession ? sessionServer(state.currentSession) : '';
}

function reasonFor(el, tier) {
    const r = blockedReason(serverOf(el), tier);
    if (r) return r;
    // The hub's own word on the terminal socket for the agent on screen (it stays until the agent is selected again;
    // what the Servers list says is checked first and is always current).
    const ro = state.currentSession && state.currentSession.hubReadOnly;
    return tier === 'interact' && ro && ro.fromFrame && !el.closest('[data-session-id]') ? ro.message : '';
}

/** Mark (or unmark) every control under `root` by what its agent's machine allows. */
export function markBlockedControls(root = document) {
    for (const el of root.querySelectorAll('[data-on-click], #sleeping-composer button[data-action], .mobile-send-btn')) {
        const tier = tierOf(el);
        const was = el.classList.contains('hub-blocked');
        if (!tier) { if (was) _clear(el); continue; }
        const reason = reasonFor(el, tier);
        if (reason) {
            el.classList.add('hub-blocked');
            el.setAttribute('aria-disabled', 'true');
            // The reason replaces the control's own tip (title, and the custom tooltip some carry) until it is allowed again.
            if (!el.dataset.hubBlockedTip) {
                el.dataset.hubBlockedTip = '1';
                el.dataset.hubOrigTitle = el.getAttribute('title') ?? '\u0000';
                if (el.hasAttribute('data-tooltip')) el.dataset.hubOrigTooltip = el.getAttribute('data-tooltip');
            }
            el.title = reason;
            if (el.hasAttribute('data-tooltip')) el.setAttribute('data-tooltip', reason);
        } else if (was) _clear(el);
    }
}

function _clear(el) {
    el.classList.remove('hub-blocked');
    el.removeAttribute('aria-disabled');
    if (el.dataset.hubBlockedTip) {
        const t = el.dataset.hubOrigTitle;
        if (t === '\u0000') el.removeAttribute('title'); else el.setAttribute('title', t);
        if ('hubOrigTooltip' in el.dataset) el.setAttribute('data-tooltip', el.dataset.hubOrigTooltip);
        delete el.dataset.hubBlockedTip; delete el.dataset.hubOrigTitle; delete el.dataset.hubOrigTooltip;
    }
}

let _timer = 0;
function _soon() {
    if (_timer) return;
    _timer = setTimeout(() => { _timer = 0; markBlockedControls(); }, 120);
}

if (typeof document !== 'undefined') {
    document.addEventListener('coral:hub-access-changed', _soon);
    document.addEventListener('coral:server-access-loaded', _soon);
    if (isCloud()) {
        new MutationObserver(_soon).observe(document.body || document.documentElement, { childList: true, subtree: true });
        // A redraw that changes nothing draws nothing new, and the hub's list is cached for 15 s: look again now and then, so a
        // tier changed at the hub shows up here without a click.
        pollEvery(() => markBlockedControls(), 5000);
        _soon();
    }
}
