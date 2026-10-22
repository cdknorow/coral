/* Session-scoped generated panels. Frames never receive ambient Coral privileges. */
import { state } from './state.js';
import { addPendingMessage } from './live_chat.js';
import { serverFetch, serverForSession, isCloud, dropBody } from './server_base.js';
import { createPanelFrame, openPanel, isPanelEvent, createEventThrottle, MAX_PANEL_BYTES, MAX_PANELS } from './panel_sandbox.js';
import { sessionUnreachable, backoffBlocked, backoffRecord } from './remote_health.js';
import { responseErrorText } from './server_status.js';
import { pollEvery } from './overload.js';
import { agentViewVisible } from './agent_view.js';
import { browserSession } from './safe_storage.js';

let session = null;
let generation = 0;
let busy = false;
// A refresh asked for while one is in flight (a panel click during a poll) runs once more when that one
// finishes, instead of being dropped until the next poll; callers that await it wait for that run.
let followUp = null, followUpDone = null;
let activePanel = 'home';
const mounted = new Map();
const seen = new Map();

const currentID = () => state.currentSession?.type === 'live' ? state.currentSession.session_id : null;
const endpoint = (sid, id = '') => '/api/agent/ui' + (id ? '/' + encodeURIComponent(id) : '') + '?session_id=' + encodeURIComponent(sid);

// Keep draft/attempt identity through polling, agent switches, and page reloads.
const requests = new Map();
function newRequestID() {
    if (typeof crypto.randomUUID === 'function') return crypto.randomUUID();
    return Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2,'0')).join('');
}
function requestState(sid) {
    if (!requests.has(sid)) {
        let saved = {};
        try { saved = JSON.parse(browserSession.get('coral-panel-request:' + sid) || '{}'); } catch {}
        requests.set(sid, {draft:typeof saved.draft === 'string' ? saved.draft : '', attempt:saved.attempt || null, blockedRequest:saved.blockedRequest || null, pending:false, message:saved.blockedRequest ? 'Delivery could not be confirmed. Check with your agent before sending this request again.' : '', error:!!saved.blockedRequest});
    }
    return requests.get(sid);
}
function saveRequest(sid, value) {
    try { browserSession.set('coral-panel-request:' + sid, JSON.stringify({draft:value.draft, attempt:value.attempt, blockedRequest:value.blockedRequest})); } catch {}
}
function updateRequestForm(form, value) {
    const input = form.querySelector('textarea');
    const button = form.querySelector('button');
    input.disabled = value.pending;
    const blocked = value.blockedRequest === value.draft.trim();
    button.disabled = blocked || value.pending || !value.draft.trim() || new TextEncoder().encode(value.draft.trim()).length > 8000;
    button.textContent = blocked ? 'Delivery uncertain' : value.pending ? 'Sending…' : value.error ? 'Retry request' : 'Send request';
    form.setAttribute('aria-busy', String(value.pending));
    const status = form.querySelector('.agent-ui-request-status');
    status.setAttribute('role', value.error ? 'alert' : 'status');
    status.textContent = blocked ? 'Delivery could not be confirmed. Check with your agent before sending this request again.' : new TextEncoder().encode(value.draft.trim()).length > 8000 ? 'Please shorten your request (maximum 8,000 bytes).' : value.message;
}
function createRequestForm(sid) {
    const value = requestState(sid);
    const form = document.createElement('form'); form.className = 'agent-ui-guide agent-ui-request';
    const title = document.createElement('label'); title.htmlFor = 'agent-ui-request-text'; title.textContent = 'What would you like your agent to show here?';
    const hint = document.createElement('p'); hint.id = 'agent-ui-request-hint'; hint.className = 'agent-ui-help';
    hint.textContent = 'Diagrams, dashboards and interactive panels appear here.';
    const input = document.createElement('textarea'); input.id = 'agent-ui-request-text'; input.className = 'agent-ui-prompt'; input.value = value.draft;
    input.placeholder = 'For example, build an interactive diagram of this project’s architecture.';
    input.setAttribute('aria-describedby', hint.id); input.rows = 4;
    const button = document.createElement('button'); button.type = 'submit'; button.className = 'agent-ui-send';
    const status = document.createElement('p'); status.className = 'agent-ui-request-status'; status.setAttribute('aria-live','polite');
    input.addEventListener('input', () => { value.draft=input.value; value.error=false; value.message=''; saveRequest(sid,value); updateRequestForm(form,value); });
    input.addEventListener('keydown', event => { if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) { event.preventDefault(); form.requestSubmit(); } });
    form.addEventListener('submit', async event => {
        event.preventDefault();
        const request = value.draft.trim();
        if (currentID() !== sid || value.pending || value.blockedRequest === request || !request || new TextEncoder().encode(request).length > 8000) return;
        if (!value.attempt || value.attempt.request !== request) value.attempt = {request, id:newRequestID()};
        const attempt = value.attempt;
        const sentAt = Date.now();
        value.pending=true; value.error=false; value.message='Sending request…'; saveRequest(sid,value); updateRequestForm(form,value);
        try {
            const response = await serverFetch(serverForSession(null, sid), '/api/agent/ui-request?session_id=' + encodeURIComponent(sid), {
                method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({request,request_id:attempt.id}),
            });
            const result = await response.json();
            if ((!response.ok || !result.delivered) && result.retryable === false) value.blockedRequest = request;
            if (!response.ok || !result.delivered) throw new Error(result.error || `Request was not delivered (${response.status}).`);
            if (result.session_id !== sid || result.request_id !== attempt.id || typeof result.notification !== 'string' || !result.notification.startsWith('[Coral UI panel request ')) throw new Error('Could not confirm delivery. Retry to check this request.');
            addPendingMessage(sid, result.notification, sentAt);
            value.draft=''; value.attempt=null; value.message='Request sent. Your agent will publish the panel here when it is ready.';
            if (result.recorded === false || result.warning) value.message += ' ' + (typeof result.warning === 'string' && result.warning ? result.warning : 'Delivery could not be recorded.');
            input.value='';
        } catch (error) {
            value.error=true; value.message=error.message || 'Could not confirm delivery. Retry this request.';
        } finally {
            value.pending=false; saveRequest(sid,value);
            // A switched-away form can be detached while this request settles.
            if (currentID() === sid) {
                const current=document.querySelector('.agent-ui-request');
                if(current) {current.querySelector('textarea').value=value.draft;updateRequestForm(current,value);}
            }
        }
    });
    form.append(title,hint,input,button,status); updateRequestForm(form,value); return form;
}

/** Show the selected panel (or the home list) from what is already mounted: no request needed. */
function showActivePanel() {
    for (const [id, item] of mounted) item.card.hidden = activePanel !== id;
    const home = document.querySelector('#agentic-panel-agent-ui .agent-ui-home');
    if (home) home.hidden = activePanel !== 'home';
}

export async function refreshAgentUI() {
    const container = document.getElementById('agentic-panel-agent-ui');
    if (!container) return;
    const sid = currentID();
    if (sid !== session) {
        session = sid; generation++; busy = false; activePanel = 'home';
        if (followUpDone) { followUpDone(); followUp = followUpDone = null; }
        mounted.clear(); container.replaceChildren();
        document.getElementById('agent-ui-count').textContent = '';
        const dialog = document.getElementById('agent-ui-expanded');
        if (dialog) dialog.remove();
    }
    if (!sid) { container.textContent = 'Select a live agent to see its UI.'; return; }
    if (busy) {
        if (!followUp) followUp = new Promise(resolve => { followUpDone = resolve; });
        return followUp;
    }
    // Not while the agent's server is unreachable; back off after gateway errors (#3151).
    const srv = serverForSession(null, sid);
    if (sessionUnreachable(state.currentSession) || backoffBlocked('ui:' + srv)) return;
    busy = true;
    const token = generation;
    try {
        const response = await serverFetch(srv, endpoint(sid));
        backoffRecord('ui:' + srv, response.ok ? 200 : response.status);
        if (!response.ok) { const text = await responseErrorText(response, srv, `Unable to load agent UI (HTTP ${response.status}).`); dropBody(response); throw new Error(text); }
        let allPanels = await response.json();
        if (token !== generation || sid !== currentID()) return;
        // On the hub at most MAX_PANELS panels per agent are shown (the newest), however many the machine lists.
        if (isCloud() && Array.isArray(allPanels) && allPanels.length > MAX_PANELS) allPanels = allPanels.slice(-MAX_PANELS);
        const panels = allPanels;
        if (activePanel !== 'home' && !panels.some(p => p.id === activePanel)) activePanel = 'home';
        container.querySelector('.agent-ui-notice')?.remove();
        let home = container.querySelector('.agent-ui-home');
        if (!home) { home = document.createElement('section'); home.className = 'agent-ui-home'; container.append(home); }
        home.hidden = activePanel !== 'home';
        if (!home.querySelector('.agent-ui-request')) home.append(createRequestForm(sid));
        let listing = home.querySelector('.agent-ui-listing');
        if (!listing) { listing=document.createElement('div'); listing.className='agent-ui-listing';home.append(listing); }
        listing.replaceChildren();
        if (allPanels.length) {
            const heading=document.createElement('h3');heading.textContent='Published panels';listing.append(heading);
        }
        for (const p of allPanels) {
            const row = document.createElement('button'); row.className = 'agent-ui-home-row';
            const label = document.createElement('strong'); label.textContent = p.title;
            const meta = document.createElement('span'); meta.textContent = `Revision ${p.revision} · ${Number(p.event_count || 0)} event${Number(p.event_count || 0) === 1 ? '' : 's'}`;
            // Show the panel at once from what is already mounted; the refresh only brings newer data.
            row.append(label, meta); row.onclick = () => { activePanel = p.id; showActivePanel(); refreshAgentUI(); }; listing.append(row);
        }
        for (const [id, item] of mounted) {
            if (!panels.some(p => p.id === id)) { item.close?.(); item.panel?.detach(); item.card.remove(); mounted.delete(id); }
        }
        for (const panel of panels) {
            if (mounted.get(panel.id)?.revision === panel.revision) continue;
            const previous = mounted.get(panel.id);
            previous?.close?.(); previous?.panel?.detach(); previous?.card.remove();
            const card = document.createElement('section'); card.className = 'agent-ui-card';
            const header = document.createElement('header');
            const title = document.createElement('strong'); title.textContent = `${panel.title} · v${panel.revision}`;
            const metric = document.createElement('span'); metric.className = 'agent-ui-event-count';
            metric.textContent = `${Number(panel.event_count || 0)} event${Number(panel.event_count || 0) === 1 ? '' : 's'}`;
            metric.title = 'Persisted interactions from this panel';
            title.append(' ', metric);
            // Always said, on this computer too (#5090): what is inside the frame is the agent's, not Coral's, whatever it draws.
            const label = document.createElement('span'); label.className = 'agent-ui-authored';
            label.textContent = 'agent-authored panel'; label.title = 'This content was written by the agent and runs in a restricted frame. Coral never asks for a password or a key in a panel.';
            title.append(' ', label);
            const expand = document.createElement('button'); expand.innerHTML = '<span class="material-icons" aria-hidden="true">open_in_full</span>'; expand.title = 'Expand panel';
            expand.setAttribute('aria-label', 'Expand panel');
            const popout = document.createElement('button'); popout.innerHTML = '<span class="material-icons" aria-hidden="true">open_in_new</span>'; popout.title = 'Open in new tab';
            popout.setAttribute('aria-label', 'Open panel in new tab');
            const dismiss = document.createElement('button'); dismiss.innerHTML = '<span class="material-icons" aria-hidden="true">close</span>'; dismiss.title = 'Close panel';
            dismiss.className = 'agent-ui-dismiss'; dismiss.title = 'Close panel';
            dismiss.setAttribute('aria-label', `Close panel: ${panel.title}`);
            const remove = document.createElement('button'); remove.innerHTML = '<span class="material-icons" aria-hidden="true">delete</span>'; remove.title = 'Delete panel';
            remove.className = 'agent-ui-delete'; remove.setAttribute('aria-label', `Delete panel: ${panel.title}`);
            const actions = document.createElement('div'); actions.className = 'agent-ui-actions';
            actions.append(expand, popout, dismiss, remove); header.append(title, actions);
            const status = document.createElement('p'); status.className = 'agent-ui-status'; status.setAttribute('role', 'status');
            const contentPath = endpoint(sid, panel.id).replace('?','/content?') + '&revision=' + panel.revision;
            // The panel is read as TEXT and run in the sandboxed host frame (panel_host.html), on this computer as on the hub, never as a URL of its own
            // (#5090): a document loaded by URL could navigate itself anywhere (the dashboard's frame-src allows https for the Linked preview) and carry what the
            // Operator typed into it out in the address. The host's frame-src forbids that and the suite proves it.
            const frame = createPanelFrame(panel.title);
            card.append(header, frame, status); container.append(card); card.hidden = activePanel !== panel.id;
            const item = { throttle: createEventThrottle(), card, frame, status, metric, eventCount: Number(panel.event_count || 0), revision: panel.revision, sid, id: panel.id };
            dismiss.onclick = () => {
                if (activePanel === panel.id) activePanel = 'home';
                showActivePanel(); // at once, like a row click: no need to wait for a request in flight
                refreshAgentUI();
            };
            remove.onclick = () => window.showConfirmModal('Delete Panel', `Delete “${panel.title}”? This removes the panel and its saved responses.`, async () => {
                remove.disabled = true;
                try {
                    const response = await serverFetch(serverForSession(null, sid), endpoint(sid, panel.id), { method: 'DELETE' });
                    if (!response.ok) {
                        const result = await response.json().catch(() => ({}));
                        throw new Error(result.error || `Delete failed (${response.status})`);
                    }
                    item.close?.();
                    // Take the card off the page too: a refresh only removes cards it still finds in mounted,
                    // so a deleted panel's card used to stay on screen.
                    item.panel?.detach(); card.remove();
                    mounted.delete(panel.id);
                    activePanel = 'home';
                    showActivePanel();
                    await refreshAgentUI();
                } catch (error) {
                    remove.disabled = false;
                    status.textContent = error.message;
                }
            });
            popout.onclick = () => window.open(new URL(contentPath, location.href).href, '_blank', 'noopener,noreferrer');
            if (isCloud()) {
                // Publishing and removing panels is the agent's own action on its machine (the hub never forwards PUT/DELETE), and a
                // sandboxed panel has no URL of its own to open in a tab.
                remove.hidden = true; popout.hidden = true;
            }
            item.panel = openPanel(frame, {
                onReady: () => { status.textContent = ''; },
                onNavigated: () => { status.textContent = 'This panel tried to navigate away from Coral and was stopped.'; frame.remove(); },
            });
            loadPanel(item, contentPath, status);
            expand.onclick = () => {
                const dialog = document.createElement('dialog'); dialog.id = 'agent-ui-expanded';
                // A named dialog with a visible Close button: a backdrop tap is not the only way out (touch, keyboard).
                dialog.setAttribute('role', 'dialog');
                dialog.setAttribute('aria-labelledby', 'agent-ui-expanded-title');
                const bar = document.createElement('div'); bar.className = 'agent-ui-expanded-bar';
                const heading = document.createElement('h2'); heading.id = 'agent-ui-expanded-title'; heading.textContent = panel.title;
                const close = document.createElement('button'); close.type = 'button'; close.className = 'agent-ui-expanded-close';
                close.innerHTML = '<span class="material-icons" aria-hidden="true">close</span>';
                close.title = 'Close'; close.setAttribute('aria-label', `Close ${panel.title}`);
                bar.append(heading, close);
                // Moving an iframe reloads it in browsers. Expanded view reloads the same revision.
                const onEscape = event => { if (event.key === 'Escape') item.close(); };
                item.close = () => {
                    document.removeEventListener('keydown', onEscape, true); card.append(frame, status); dialog.remove(); item.close = null;
                    if (expand.isConnected) expand.focus(); // back to where the user opened it
                };
                close.addEventListener('click', () => item.close());
                document.addEventListener('keydown', onEscape, true);
                dialog.addEventListener('keydown', onEscape);
                dialog.addEventListener('cancel', e => { e.preventDefault(); item.close(); });
                dialog.addEventListener('click', e => { if (e.target === dialog) item.close(); });
                dialog.append(bar, frame, status); document.body.append(dialog); dialog.showModal();
                close.focus();
            };
            mounted.set(panel.id, item);
        }
        showActivePanel();
        if (container.classList.contains('active')) {
            for (const p of panels) seen.set(sid + '/' + p.id, p.revision);
        }
        const unread = panels.filter(p => seen.get(sid + '/' + p.id) !== p.revision).length;
        document.getElementById('agent-ui-count').textContent = unread ? String(unread) : '';
    } catch (error) {
        if (token === generation && sid === currentID()) {
            let notice = container.querySelector('.agent-ui-notice');
            if (!notice) { notice = document.createElement('p'); notice.className = 'agent-ui-notice'; container.append(notice); }
            notice.textContent = error.message;
        }
    } finally {
        if (token === generation) {
            busy = false;
            if (followUp) {
                const done = followUpDone;
                followUp = followUpDone = null;
                refreshAgentUI().finally(done);
            }
        }
    }
}

export function initAgentUI() {
    refreshAgentUI();
    let lastRun = Date.now();
    pollEvery(() => {
        // Not at all while the agent's page is off screen. While it is showing, every 2 s when the Agent UI tab is
        // open, and slowly (for the tab's unread count) otherwise: every 30 s, 10 s on the hub (#3719).
        if (!agentViewVisible()) return;
        const open = !!document.getElementById('agentic-panel-agent-ui')?.classList.contains('active');
        const now = Date.now();
        if (!open && now - lastRun < (isCloud() ? 10000 : 30000)) return;
        lastRun = now;
        refreshAgentUI();
    }, 2000);

    window.addEventListener('message', async event => {
        if (event.data?.type !== 'coral-ui-event') return;
        if (!isPanelEvent(event.data)) return; // the host already validated it; so does the dashboard
        const item = [...mounted.values()].find(p => p.frame.contentWindow === event.source);
        if (!item || item.sid !== currentID()) return;
        const { action, payload, requestId } = event.data;
        if (typeof requestId !== 'string' || requestId.length > 64) return;
        let error = null, result = {};
        const refused = item.throttle.tryStart();
        if (refused) {
            item.status.textContent = refused;
            event.source.postMessage({ type: 'coral-ui-result', requestId, error: refused }, '*');
            return;
        }
        try {
            if (typeof action !== 'string' || !/^[A-Za-z0-9_-]{1,64}$/.test(action)) throw new Error('Invalid action');
            const body = JSON.stringify({ revision: item.revision, action, payload });
            if (new TextEncoder().encode(body).length > 16384) throw new Error('Interaction exceeds 16 KiB');
            const response = await serverFetch(serverForSession(null, item.sid), endpoint(item.sid, item.id).replace('?','/events?'), {
                method: 'POST', headers: { 'Content-Type': 'application/json' }, body,
            });
            result = await response.json();
            if (!response.ok) throw new Error(result.error || `Interaction failed (${response.status})`);
            item.eventCount += 1;
            item.metric.textContent = `${item.eventCount} event${item.eventCount === 1 ? '' : 's'}`;
            item.status.textContent = result.notified
                ? 'Response saved; agent notified.'
                : 'Response saved; agent not notified' + (result.notify_error ? ': ' + result.notify_error : '.') + ' You can ask the agent to read it.';
        } catch (e) { error = e.message; item.status.textContent = error; }
        item.throttle.done();
        event.source.postMessage({ type: 'coral-ui-result', requestId, error, id: result.id, notified: result.notified, notify_error: result.notify_error }, '*');
    });
}

// Read one panel as text and hand it to the sandboxed host frame.
async function loadPanel(item, contentPath, status) {
    try {
        status.textContent = 'Loading panel…';
        const response = await serverFetch(serverForSession(null, item.sid), contentPath);
        if (!response.ok) throw new Error(response.status === 409 ? 'The panel was updated; it will reload.' : `Unable to load the panel (${response.status})`);
        const html = await response.text();
        if (html.length > MAX_PANEL_BYTES) throw new Error('This panel is larger than 2 MiB and is not shown.');
        item.panel.setHtml(html);
    } catch (e) { status.textContent = e.message; }
}
