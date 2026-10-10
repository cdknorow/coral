/* Session-scoped generated panels. Frames never receive ambient Coral privileges. */
import { state } from './state.js';
import { addPendingMessage } from './live_chat.js';
import { serverFetch, serverForSession } from './server_base.js';

let session = null;
let generation = 0;
let busy = false;
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
        try { saved = JSON.parse(sessionStorage.getItem('coral-panel-request:' + sid) || '{}'); } catch {}
        requests.set(sid, {draft:typeof saved.draft === 'string' ? saved.draft : '', attempt:saved.attempt || null, blockedRequest:saved.blockedRequest || null, pending:false, message:saved.blockedRequest ? 'Delivery could not be confirmed. Check with your agent before sending this request again.' : '', error:!!saved.blockedRequest});
    }
    return requests.get(sid);
}
function saveRequest(sid, value) {
    try { sessionStorage.setItem('coral-panel-request:' + sid, JSON.stringify({draft:value.draft, attempt:value.attempt, blockedRequest:value.blockedRequest})); } catch {}
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
    hint.textContent = 'Your agent can create diagrams, dashboards, and interactive UI panels and display them here.';
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

export async function refreshAgentUI() {
    const container = document.getElementById('agentic-panel-agent-ui');
    if (!container) return;
    const sid = currentID();
    if (sid !== session) {
        session = sid; generation++; busy = false; activePanel = 'home';
        mounted.clear(); container.replaceChildren();
        document.getElementById('agent-ui-count').textContent = '';
        const dialog = document.getElementById('agent-ui-expanded');
        if (dialog) dialog.remove();
    }
    if (!sid) { container.textContent = 'Select a live agent to see its UI.'; return; }
    if (busy) return;
    busy = true;
    const token = generation;
    try {
        const response = await serverFetch(serverForSession(null, sid), endpoint(sid));
        if (!response.ok) throw new Error(`Unable to load agent UI (${response.status})`);
        const allPanels = await response.json();
        if (token !== generation || sid !== currentID()) return;
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
            row.append(label, meta); row.onclick = () => { activePanel = p.id; refreshAgentUI(); }; listing.append(row);
        }
        for (const [id, item] of mounted) {
            if (!panels.some(p => p.id === id)) { item.close?.(); item.card.remove(); mounted.delete(id); }
        }
        for (const panel of panels) {
            if (mounted.get(panel.id)?.revision === panel.revision) continue;
            const previous = mounted.get(panel.id);
            previous?.close?.(); previous?.card.remove();
            const card = document.createElement('section'); card.className = 'agent-ui-card';
            const header = document.createElement('header');
            const title = document.createElement('strong'); title.textContent = `${panel.title} · v${panel.revision}`;
            const metric = document.createElement('span'); metric.className = 'agent-ui-event-count';
            metric.textContent = `${Number(panel.event_count || 0)} event${Number(panel.event_count || 0) === 1 ? '' : 's'}`;
            metric.title = 'Persisted interactions from this panel';
            title.append(' ', metric);
            const expand = document.createElement('button'); expand.innerHTML = '<span class="material-icons">open_in_full</span>'; expand.title = 'Expand panel';
            expand.setAttribute('aria-label', 'Expand panel');
            const popout = document.createElement('button'); popout.innerHTML = '<span class="material-icons">open_in_new</span>'; popout.title = 'Open in new tab';
            popout.setAttribute('aria-label', 'Open panel in new tab');
            const dismiss = document.createElement('button'); dismiss.innerHTML = '<span class="material-icons">close</span>'; dismiss.title = 'Close panel';
            dismiss.className = 'agent-ui-dismiss'; dismiss.title = 'Close panel';
            dismiss.setAttribute('aria-label', `Close panel: ${panel.title}`);
            const remove = document.createElement('button'); remove.innerHTML = '<span class="material-icons">delete</span>'; remove.title = 'Delete panel';
            remove.className = 'agent-ui-delete'; remove.setAttribute('aria-label', `Delete panel: ${panel.title}`);
            const actions = document.createElement('div'); actions.className = 'agent-ui-actions';
            actions.append(expand, popout, dismiss, remove); header.append(title, actions);
            const status = document.createElement('p'); status.className = 'agent-ui-status'; status.setAttribute('role', 'status');
            const frame = document.createElement('iframe'); frame.title = panel.title;
            frame.setAttribute('sandbox', 'allow-scripts');
            frame.setAttribute('referrerpolicy', 'no-referrer');
            frame.src = endpoint(sid, panel.id).replace('?','/content?') + '&revision=' + panel.revision;
            card.append(header, frame, status); container.append(card); card.hidden = activePanel !== panel.id;
            const item = { card, frame, status, metric, eventCount: Number(panel.event_count || 0), revision: panel.revision, sid, id: panel.id };
            dismiss.onclick = () => {
                if (activePanel === panel.id) activePanel = 'home';
                refreshAgentUI();
            };
            remove.onclick = async () => {
                if (!window.confirm(`Delete “${panel.title}”? This removes the panel and its saved responses.`)) return;
                remove.disabled = true;
                try {
                    const response = await serverFetch(serverForSession(null, sid), endpoint(sid, panel.id), { method: 'DELETE' });
                    if (!response.ok) {
                        const result = await response.json().catch(() => ({}));
                        throw new Error(result.error || `Delete failed (${response.status})`);
                    }
                    item.close?.();
                    mounted.delete(panel.id);
                    activePanel = 'home';
                    await refreshAgentUI();
                } catch (error) {
                    remove.disabled = false;
                    status.textContent = error.message;
                }
            };
            popout.onclick = () => window.open(frame.src, '_blank', 'noopener,noreferrer');
            expand.onclick = () => {
                const dialog = document.createElement('dialog'); dialog.id = 'agent-ui-expanded';
                // Moving an iframe reloads it in browsers. Expanded view reloads the same revision.
                const onEscape = event => { if (event.key === 'Escape') item.close(); };
                item.close = () => { document.removeEventListener('keydown', onEscape, true); card.append(frame, status); dialog.remove(); item.close = null; };
                document.addEventListener('keydown', onEscape, true);
                dialog.addEventListener('keydown', onEscape);
                dialog.addEventListener('cancel', e => { e.preventDefault(); item.close(); });
                dialog.addEventListener('click', e => { if (e.target === dialog) item.close(); });
                dialog.append(frame, status); document.body.append(dialog); dialog.showModal();
            };
            mounted.set(panel.id, item);
        }
        for (const [id, item] of mounted) item.card.hidden = activePanel !== id;
        home.hidden = activePanel !== 'home';
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
    } finally { if (token === generation) busy = false; }
}

export function initAgentUI() {
    refreshAgentUI();
    setInterval(() => { if (!document.hidden) refreshAgentUI(); }, 2000);
    window.addEventListener('message', async event => {
        if (event.data?.type !== 'coral-ui-event') return;
        const item = [...mounted.values()].find(p => p.frame.contentWindow === event.source);
        if (!item || item.sid !== currentID()) return;
        const { action, payload, requestId } = event.data;
        if (typeof requestId !== 'string' || requestId.length > 64) return;
        let error = null, result = {};
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
        event.source.postMessage({ type: 'coral-ui-result', requestId, error, id: result.id, notified: result.notified, notify_error: result.notify_error }, '*');
    });
}
