/* Custom Views — user-generated sidebar tabs rendered in sandboxed iframes */

import { state } from './state.js';
import { escapeHtml } from './utils.js';

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
  // Example: fetch live sessions
  fetch('/api/sessions/live')
    .then(r => r.json())
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
        `<button class="agentic-tab" onclick="switchAgenticTab('custom-view-${v.id}', 'top')"
                 id="agentic-tab-custom-view-${v.id}" title="${escapeHtml(v.name)}" aria-label="${escapeHtml(v.name)}"
                 oncontextmenu="event.preventDefault(); window._showViewContextMenu(event, ${v.id})">
            <span class="material-icons">dashboard</span>
        </button>`
    ).join('');

    // Render panels
    const panelContainer = document.getElementById('custom-view-panels');
    if (!panelContainer) return;

    panelContainer.innerHTML = _views.map(v =>
        `<div id="agentic-panel-custom-view-${v.id}" class="agentic-panel custom-view-panel"></div>`
    ).join('');
}

/** Create the iframe for a custom view when its tab is activated. */
export function activateCustomView(viewId) {
    const panel = document.getElementById(`agentic-panel-custom-view-${viewId}`);
    if (!panel) return;

    // Only create iframe if not already present
    if (panel.querySelector('iframe')) return;

    const view = _views.find(v => v.id === viewId);
    if (!view) return;

    const iframe = document.createElement('iframe');
    iframe.className = 'custom-view-iframe';
    iframe.sandbox = 'allow-scripts allow-same-origin';
    iframe.srcdoc = _injectContext(view.html);
    panel.innerHTML = '';
    panel.appendChild(iframe);
}

/** Inject CORAL_CONTEXT into the view HTML. */
function _injectContext(html) {
    const ctx = {
        agentName: state.currentSession ? state.currentSession.name : null,
        sessionId: state.currentSession ? state.currentSession.session_id : null,
        agentType: state.currentSession ? state.currentSession.agent_type : null,
    };
    const script = `<script>window.CORAL_CONTEXT = ${JSON.stringify(ctx)};<\/script>`;
    // Inject before </head> or at the start of <body>
    if (html.includes('</head>')) {
        return html.replace('</head>', script + '</head>');
    }
    return script + html;
}

/* ── Modal: Create / Edit View ────────────────────────────── */

window._showCreateViewModal = function() {
    const modal = document.getElementById('custom-view-modal');
    document.getElementById('custom-view-modal-title').textContent = 'Create Custom View';
    document.getElementById('custom-view-edit-id').value = '';
    document.getElementById('custom-view-name').value = '';
    modal.style.display = 'flex';
    _initViewEditor(DEFAULT_VIEW_HTML);
};

window._showEditViewModal = function(viewId) {
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

function _initViewEditor(content) {
    const container = document.getElementById('custom-view-editor');
    if (!container) return;
    container.innerHTML = '';

    if (_cmEditor) { _cmEditor.destroy(); _cmEditor = null; }

    const cm = window.CoralCM;
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
        <button onclick="window._showEditViewModal(${viewId}); this.parentElement.remove()">Edit</button>
        <button onclick="window._deleteView(${viewId}); this.parentElement.remove()">Delete</button>
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
