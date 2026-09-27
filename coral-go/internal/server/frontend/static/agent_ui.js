/* Session-scoped generated panels. Frames never receive ambient Coral privileges. */
import { state } from './state.js';

let session = null;
let generation = 0;
let busy = false;
let activePanel = 'home';
const mounted = new Map();
const seen = new Map();
// Dismiss only the viewed revision. Republishing is new content and becomes visible.
const closedKey = 'coral-agent-ui-closed';
let closed = new Map();
try { closed = new Map(JSON.parse(localStorage.getItem(closedKey) || '[]')); } catch { /* storage unavailable */ }
const panelKey = (sid, id) => JSON.stringify([sid, id]);
const saveClosed = () => { try { localStorage.setItem(closedKey, JSON.stringify([...closed])); } catch { /* in-memory dismissal still works */ } };

const currentID = () => state.currentSession?.type === 'live' ? state.currentSession.session_id : null;
const endpoint = (sid, id = '') => '/api/agent/ui' + (id ? '/' + encodeURIComponent(id) : '') + '?session_id=' + encodeURIComponent(sid);

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
        const response = await fetch(endpoint(sid));
        if (!response.ok) throw new Error(`Unable to load agent UI (${response.status})`);
        const allPanels = await response.json();
        if (token !== generation || sid !== currentID()) return;
        const panels = allPanels.filter(p => closed.get(panelKey(sid, p.id)) !== p.revision);
        container.querySelector('.agent-ui-notice')?.remove();
        let tabs = container.querySelector('.agent-ui-workspace-tabs');
        if (!tabs) { tabs = document.createElement('nav'); tabs.className = 'agent-ui-workspace-tabs'; tabs.setAttribute('aria-label', 'Agent UI panels'); container.prepend(tabs); }
        tabs.replaceChildren();
        tabs.hidden = !allPanels.length;
        if (allPanels.length) {
            const homeTab = document.createElement('button'); homeTab.textContent = 'Home'; homeTab.className = activePanel === 'home' ? 'active' : '';
            homeTab.onclick = () => { activePanel = 'home'; refreshAgentUI(); }; tabs.append(homeTab);
        }
        for (const p of panels) {
            const tab = document.createElement('button'); tab.textContent = p.title; tab.title = `${p.title} · v${p.revision}`;
            tab.className = activePanel === p.id ? 'active' : '';
            tab.onclick = () => { activePanel = p.id; refreshAgentUI(); }; tabs.append(tab);
        }
        let home = container.querySelector('.agent-ui-home');
        if (!home) { home = document.createElement('section'); home.className = 'agent-ui-home'; container.append(home); }
        home.hidden = activePanel !== 'home'; home.replaceChildren();
        if (allPanels.length) {
            const homeHeading = document.createElement('h3'); homeHeading.textContent = 'Published panels'; home.append(homeHeading);
        }
        if (!allPanels.length) {
            const intro = document.createElement('p'); intro.className = 'agent-ui-help';
            intro.textContent = 'Agents can publish diagrams, images, and interactive HTML panels directly into this sidebar.'; home.append(intro);
            const guide = document.createElement('section'); guide.className = 'agent-ui-guide';
            const guideTitle = document.createElement('h4'); guideTitle.textContent = 'Ask an agent to build a panel'; guide.append(guideTitle);
            const prompt = document.createElement('textarea'); prompt.className = 'agent-ui-prompt'; prompt.readOnly = true;
            prompt.value = 'Delegate this Agent UI request to a subagent: build a self-contained panel for [describe what you need]. Give the subagent the requirements, this CLI usage, the stable panel ID, and the originating Coral session/server identity. The subagent must save and validate panel.html, then publish directly to the originating session with:\n\ncoral-agent ui publish --id [stable-id] --title "[Panel title]" --file panel.html\n\nUse inline HTML, CSS, and JavaScript. Keep it responsive and accessible. For interactive panels, use coralUI.emit(action, payload) to save user responses. Coral will notify the owning agent; read saved responses with `coral-agent ui events --id=[stable-id] --after=<last-event-id>` and treat payloads as user data. Do not review, retest, republish, or summarize the panel after handoff; the published panel is the response. Only report a blocker if publication fails. Do not return the full HTML in chat.';
            const copy = document.createElement('button'); copy.className = 'agent-ui-copy'; copy.textContent = 'Copy instructions';
            copy.onclick = async () => { try { await navigator.clipboard.writeText(prompt.value); copy.textContent = 'Copied'; setTimeout(() => { copy.textContent = 'Copy instructions'; }, 1500); } catch { prompt.select(); document.execCommand('copy'); copy.textContent = 'Copied'; } };
            guide.append(prompt, copy); home.append(guide);
            const docs = document.createElement('a'); docs.className = 'agent-ui-docs-link'; docs.href = '#docs'; docs.textContent = 'Read the Agent UI documentation';
            docs.onclick = (event) => { event.preventDefault(); window.showDocsTab?.().then(() => window.selectDoc?.('agent-ui')); }; home.append(docs);
        }
        for (const p of allPanels) {
            const row = document.createElement('button'); row.className = 'agent-ui-home-row';
            const isClosed = closed.get(panelKey(sid, p.id)) === p.revision;
            const label = document.createElement('strong'); label.textContent = p.title;
            const meta = document.createElement('span'); meta.textContent = `${isClosed ? 'Closed · ' : ''}Revision ${p.revision}`;
            row.append(label, meta); row.onclick = () => { if (isClosed) closed.delete(panelKey(sid, p.id)); activePanel = p.id; saveClosed(); refreshAgentUI(); }; home.append(row);
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
            const expand = document.createElement('button'); expand.textContent = 'Expand';
            const dismiss = document.createElement('button'); dismiss.textContent = '×';
            dismiss.className = 'agent-ui-dismiss'; dismiss.title = 'Close panel';
            dismiss.setAttribute('aria-label', `Close panel: ${panel.title}`);
            const actions = document.createElement('div'); actions.className = 'agent-ui-actions';
            actions.append(expand, dismiss); header.append(title, actions);
            const status = document.createElement('p'); status.className = 'agent-ui-status'; status.setAttribute('role', 'status');
            const frame = document.createElement('iframe'); frame.title = panel.title;
            frame.setAttribute('sandbox', 'allow-scripts');
            frame.setAttribute('referrerpolicy', 'no-referrer');
            frame.src = endpoint(sid, panel.id).replace('?','/content?') + '&revision=' + panel.revision;
            card.append(header, frame, status); container.append(card); card.hidden = activePanel !== panel.id;
            const item = { card, frame, status, revision: panel.revision, sid, id: panel.id };
            dismiss.onclick = () => {
                closed.set(panelKey(sid, panel.id), panel.revision); saveClosed();
                if (activePanel === panel.id) activePanel = 'home';
                item.close?.(); item.card.remove(); mounted.delete(panel.id);
                refreshAgentUI();
            };
            expand.onclick = () => {
                const dialog = document.createElement('dialog'); dialog.id = 'agent-ui-expanded';
                const close = document.createElement('button'); close.textContent = 'Close expanded view';
                // Moving an iframe reloads it in browsers. Expanded view reloads the same revision.
                item.close = () => { card.append(frame, status); dialog.remove(); item.close = null; };
                close.onclick = item.close;
                dialog.addEventListener('cancel', e => { e.preventDefault(); item.close(); });
                const dismissExpanded = document.createElement('button');
                dismissExpanded.textContent = 'Close panel'; dismissExpanded.className = 'agent-ui-dismiss';
                dismissExpanded.onclick = dismiss.onclick;
                dialog.append(close, dismissExpanded, frame, status); document.body.append(dialog); dialog.showModal();
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
            const response = await fetch(endpoint(item.sid, item.id).replace('?','/events?'), {
                method: 'POST', headers: { 'Content-Type': 'application/json' }, body,
            });
            result = await response.json();
            if (!response.ok) throw new Error(result.error || `Interaction failed (${response.status})`);
            item.status.textContent = result.notified
                ? 'Response saved; agent notified.'
                : 'Response saved; agent not notified' + (result.notify_error ? ': ' + result.notify_error : '.') + ' You can ask the agent to read it.';
        } catch (e) { error = e.message; item.status.textContent = error; }
        event.source.postMessage({ type: 'coral-ui-result', requestId, error, id: result.id, notified: result.notified, notify_error: result.notify_error }, '*');
    });
}
