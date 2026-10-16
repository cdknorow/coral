/* Custom Views — user-generated sidebar tabs rendered in sandboxed iframes */

import { state } from './state.js';
import { escapeHtml, escapeAttr } from './utils.js';
import { registerActions, idOf, textOf, tokenOf } from './actions.js';
import { isCloud } from './server_base.js';
import { lockFrame, sandboxedDoc } from './sandbox_frame.js';
import { lockViewFrame, viewDocument, attachViewBridge, API_VIEW_SEES } from './view_sandbox.js';
import { loadCodeMirror } from './vendor_loader.js';

let _views = [];       // cached views from API
let _cmEditor = null;  // CodeMirror editor for the view code

const DEFAULT_VIEW_HTML = `<!DOCTYPE html>
<html>
<head>
<style>
  body { font-family: -apple-system, sans-serif; color: #e0e0e4; background: #1a1a1e; padding: 12px; font-size: 13px; }
  h3 { margin: 0 0 12px; font-size: 14px; color: #8c8c94; }
</style>
</head>
<body>
<h3>My Custom View</h3>
<div id="content">Loading...</div>
<script>
  const ctx = window.CORAL_CONTEXT || {};
  // The view runs in a sandbox: it can read Coral's status through coral.get() (GET endpoints listed at /api/spec.json)
  // but cannot call anything else.
  coral.get('/api/sessions/live')
    .then(data => {
      const sessions = data.sessions || [];
      document.getElementById('content').innerHTML =
        sessions.map(s => '<div>' + s.display_name + ' (' + s.agent_type + ')</div>').join('');
    });
</script>
</body>
</html>`;

/** Load saved views from the API and render tabs. */
export async function loadCustomViews() {
    try {
        const resp = await fetch('/api/views');
        const data = await resp.json();
        _views = Array.isArray(data) ? data : (data.views || []);
    } catch {
        _views = [];
    }
    _renderViewTabs();
}

/** Render tab buttons for each saved view. */
function _renderViewTabs() {
    const container = document.getElementById('custom-view-tabs');
    if (!container) return;

    container.innerHTML = _views.map(v =>
        `<button class="agentic-tab" data-on-click="custom-view-tab" data-view-id="${escapeAttr(v.id)}"
                 id="agentic-tab-custom-view-${Number(v.id)}" title="${escapeHtml(v.name)}" aria-label="${escapeHtml(v.name)}"
                 data-on-contextmenu="custom-view-menu">
            <span class="material-icons">dashboard</span>
        </button>`
    ).join('');

    // Render panels
    const panelContainer = document.getElementById('custom-view-panels');
    if (!panelContainer) return;

    panelContainer.innerHTML = _views.map(v =>
        `<div id="agentic-panel-custom-view-${Number(v.id)}" class="agentic-panel custom-view-panel"></div>`
    ).join('');
}

const _approved = new Set();   // ids of API-created views the user chose to run in this page
const _detach = new Map();     // view id -> function that removes its message bridge

/** Create the iframe for a custom view when its tab is activated. */
export function activateCustomView(viewId) {
    const panel = document.getElementById(`agentic-panel-custom-view-${viewId}`);
    if (!panel) return;

    // Only create iframe if not already present
    if (panel.querySelector('iframe') || panel.querySelector('.custom-view-gate')) return;

    const view = _views.find(v => v.id === viewId);
    if (!view) return;

    // A view written by a caller that was not this dashboard (an agent, a script) does not run until the user says so.
    if (!isCloud() && view.created_via === 'api' && !_approved.has(viewId)) {
        _renderGate(panel, view);
        return;
    }
    _mountViewFrame(panel, view);
}

function _renderGate(panel, view) {
    const gate = document.createElement('div');
    gate.className = 'custom-view-gate';
    const title = document.createElement('strong');
    title.textContent = view.name || 'Custom view';
    const note = document.createElement('p');
    note.textContent = 'This view was created by an automated caller (an agent or a script), not in this dashboard. If you run it, it will be able to read ' + API_VIEW_SEES + ', and nothing else: it cannot change anything, read terminal output, files or notes, or reach the network. Review its source before you run it.';
    const src = document.createElement('pre');
    src.className = 'custom-view-gate-source';
    src.textContent = String(view.html || '').slice(0, 4000) + (String(view.html || '').length > 4000 ? '\n…' : '');
    const run = document.createElement('button');
    run.type = 'button'; run.className = 'btn btn-small btn-primary'; run.textContent = 'Run this view';
    run.addEventListener('click', () => { _approved.add(view.id); gate.remove(); _mountViewFrame(panel, view); });
    gate.append(title, note, src, run);
    panel.innerHTML = '';
    panel.appendChild(gate);
}

function _mountViewFrame(panel, view) {
    const iframe = document.createElement('iframe');
    iframe.className = 'custom-view-iframe';
    if (isCloud()) {
        // A saved view is text an account stored on the hub: it is only ever shown inert (empty sandbox, no scripts, no
        // same-origin access, CSP), never run on the hub's origin and never given the page's context.
        lockFrame(iframe);
        iframe.srcdoc = sandboxedDoc(view.html);
    } else {
        // Scripts only, an opaque origin and no network: the view reads Coral through the allowlisted bridge
        // (view_sandbox.js), never as the dashboard. It runs in a child frame of a trusted host frame whose CSP refuses any
        // navigation of the view (nothing else can stop a document sending data out by navigating itself).
        lockViewFrame(iframe);
        _detach.get(view.id)?.();
        const stop = () => {
            _detach.get(view.id)?.(); _detach.delete(view.id);
            iframe.remove();
            _renderNotice(panel, view, 'This view tried to navigate away from Coral and was stopped.');
        };
        _detach.set(view.id, attachViewBridge(iframe, { doc: viewDocument(view.html, _viewContext()), tier: view.created_via === 'api' ? 'api' : 'ui', onNavigated: stop }));
        let loads = 0;
        iframe.addEventListener('load', () => { if (++loads > 1) stop(); }); // the host itself must load exactly once
    }
    panel.innerHTML = '';
    panel.appendChild(iframe);
}

function _renderNotice(panel, view, text) {
    const box = document.createElement('div');
    box.className = 'custom-view-gate';
    const title = document.createElement('strong');
    title.textContent = view.name || 'Custom view';
    const p = document.createElement('p');
    p.textContent = text;
    box.append(title, p);
    panel.replaceChildren(box);
}

function _viewContext() {
    return {
        agentName: state.currentSession ? state.currentSession.name : null,
        sessionId: state.currentSession ? state.currentSession.session_id : null,
        agentType: state.currentSession ? state.currentSession.agent_type : null,
    };
}

/* ── Modal: Create / Edit View ────────────────────────────── */

window._showCreateViewModal = function() {
    if (isCloud()) return; // views cannot run scripts on the hub, so there is no editor there (saved views are shown read-only)
    const modal = document.getElementById('custom-view-modal');
    document.getElementById('custom-view-modal-title').textContent = 'Create Custom View';
    document.getElementById('custom-view-edit-id').value = '';
    document.getElementById('custom-view-name').value = '';
    modal.style.display = 'flex';
    _initViewEditor(DEFAULT_VIEW_HTML);
};

window._showEditViewModal = function(viewId) {
    if (isCloud()) return; // views cannot run scripts on the hub, so there is no editor there (saved views are shown read-only)
    const view = _views.find(v => v.id === viewId);
    if (!view) return;
    const modal = document.getElementById('custom-view-modal');
    document.getElementById('custom-view-modal-title').textContent = 'Edit View: ' + view.name;
    document.getElementById('custom-view-edit-id').value = viewId;
    document.getElementById('custom-view-name').value = view.name;
    modal.style.display = 'flex';
    _initViewEditor(view.html);
};

window._hideViewModal = function() {
    document.getElementById('custom-view-modal').style.display = 'none';
    if (_cmEditor) { _cmEditor.destroy(); _cmEditor = null; }
};

let _editorGen = 0;

async function _initViewEditor(content) {
    const container = document.getElementById('custom-view-editor');
    if (!container) return;
    container.innerHTML = '';

    if (_cmEditor) { _cmEditor.destroy(); _cmEditor = null; }

    // CodeMirror loads on first use (#3083). A newer open (another view) wins.
    const gen = ++_editorGen;
    const cm = await loadCodeMirror().catch(e => { console.error('[coral]', e); return null; });
    if (gen !== _editorGen) return;
    if (cm) {
        _cmEditor = new cm.EditorView({
            state: cm.EditorState.create({
                doc: content,
                extensions: [
                    cm.basicSetup,
                    cm.oneDark,
                    cm.html(),
                    cm.EditorView.lineWrapping,
                    cm.EditorView.theme({ '&': { height: '350px' }, '.cm-scroller': { overflow: 'auto' } }),
                ],
            }),
            parent: container,
        });
    } else {
        // Fallback textarea
        container.innerHTML = `<textarea class="inline-preview-fallback-editor" style="height:350px">${escapeHtml(content)}</textarea>`;
    }
}

function _getEditorContent() {
    if (_cmEditor) return _cmEditor.state.doc.toString();
    const ta = document.querySelector('#custom-view-editor textarea');
    return ta ? ta.value : '';
}

window._saveView = async function() {
    const name = document.getElementById('custom-view-name').value.trim();
    const html = _getEditorContent();
    const editId = document.getElementById('custom-view-edit-id').value;

    if (!name) { window.showAlertModal?.('View Name Required', 'Enter a name before saving this custom view.'); return; }

    try {
        const url = editId ? `/api/views/${editId}` : '/api/views';
        const method = editId ? 'PUT' : 'POST';
        const resp = await fetch(url, {
            method,
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name, html }),
        });
        const data = await resp.json();
        if (data.error) { window.showAlertModal?.('Unable to Save View', data.error); return; }

        window._hideViewModal();
        await loadCustomViews();
    } catch (e) {
        window.showAlertModal?.('Unable to Save View', `Failed to save view: ${e.message}`);
    }
};

/* ── Context Menu ─────────────────────────────────────────── */

window._showViewContextMenu = function(event, viewId) {
    // Remove existing menu
    document.querySelectorAll('.view-context-menu').forEach(el => el.remove());

    const menu = document.createElement('div');
    menu.className = 'view-context-menu';
    menu.style.left = event.clientX + 'px';
    menu.style.top = event.clientY + 'px';
    menu.innerHTML = `
        ${isCloud() ? '' : `<button data-on-click="custom-view-edit" data-view-id="${Number(viewId)}">Edit</button>`}
        <button data-on-click="custom-view-delete" data-view-id="${Number(viewId)}">Delete</button>
    `;
    document.body.appendChild(menu);

    // Close on click outside
    setTimeout(() => {
        document.addEventListener('click', function handler() {
            menu.remove();
            document.removeEventListener('click', handler);
        }, { once: true });
    }, 0);
};

window._deleteView = async function(viewId) {
    window.showConfirmModal?.('Delete Custom View', 'Delete this custom view?', async () => {
        try {
            await fetch(`/api/views/${viewId}`, { method: 'DELETE' });
            await loadCustomViews();
        } catch (e) {
            window.showAlertModal?.('Unable to Delete View', `Failed to delete view: ${e.message}`);
        }
    });
};

// A custom view's tab, its context menu and the menu's Edit / Delete (#3645). The view id is checked;
// Edit and Delete close the menu (their button's parent), as before.
registerActions({
    'custom-view-tab': el => { const id = tokenOf(el.dataset.viewId); if (id) window.switchAgenticTab('custom-view-' + id, 'top'); },
    'custom-view-menu': (el, event) => { event.preventDefault(); const id = idOf(el.dataset.viewId); if (id) window._showViewContextMenu(event, id); },
    'custom-view-edit': el => { const id = idOf(el.dataset.viewId); if (id) window._showEditViewModal(id); el.parentElement?.remove(); },
    'custom-view-delete': el => { const id = idOf(el.dataset.viewId); if (id) window._deleteView(id); el.parentElement?.remove(); },
});
