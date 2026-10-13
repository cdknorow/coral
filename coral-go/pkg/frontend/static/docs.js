/* Documentation: browsable agent_docs viewer */

import { escapeHtml as esc, showView, jsStr } from './utils.js';
import { apiFetch } from './api.js';
import { loadHighlight } from './vendor_loader.js';

let docsList = [];
let selectedDoc = null;

// ── Initialization ─────────────────────────────────────────────────────

export async function showDocsTab() {
    showView('docs-view');
    if (!docsList.length) await loadDocsList();
}

/** Load the list and open README; a failed load shows an error with Retry
 *  instead of a blank page (#3152). */
async function loadDocsList() {
    const content = document.getElementById('docs-content');
    if (content) content.innerHTML = '<div class="docs-empty"><span class="material-icons" style="font-size:24px;animation:spin 1s linear infinite">refresh</span></div>';
    const ok = await fetchDocsList();
    renderDocNav();
    if (!ok) {
        showDocsError('Could not load the documentation. Check the connection to Coral, then try again.', loadDocsList);
        return;
    }
    // Auto-select README, else the first document; an empty list says so.
    const first = docsList.find(d => d.name === 'README') || docsList[0];
    if (first) selectDoc(first.name);
    else if (content) content.innerHTML = '<div class="docs-empty"><p>No documentation found.</p></div>';
}

/** An error in the content pane with a Retry button that calls retry. */
function showDocsError(message, retry) {
    const content = document.getElementById('docs-content');
    if (!content) return;
    const box = document.createElement('div');
    box.className = 'docs-empty docs-error';
    box.setAttribute('role', 'alert');
    const p = document.createElement('p');
    p.textContent = message;
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn btn-small';
    btn.textContent = 'Retry';
    btn.addEventListener('click', () => retry());
    box.append(p, btn);
    content.replaceChildren(box);
}

// ── API ────────────────────────────────────────────────────────────────

/** Fetch the docs list; false when it could not be loaded. */
async function fetchDocsList() {
    try {
        const list = await apiFetch('/api/agent-docs');
        docsList = Array.isArray(list) ? list : [];
        return Array.isArray(list);
    } catch (e) {
        console.error('Failed to fetch docs list:', e);
        docsList = [];
        return false;
    }
}

async function fetchDocContent(name) {
    try {
        return await apiFetch(`/api/agent-docs/${encodeURIComponent(name)}`);
    } catch (e) {
        console.error('Failed to fetch doc:', e);
        return null;
    }
}

// ── Nav rendering ──────────────────────────────────────────────────────

function renderDocNav() {
    const list = document.getElementById('docs-nav-list');
    if (!list) return;

    list.innerHTML = docsList.map(d => {
        const active = selectedDoc === d.name ? ' active' : '';
        return `<li class="session-list-item${active}" onclick="selectDoc('${jsStr(d.name)}')">${esc(d.title)}</li>`;
    }).join('');
}

// ── Content rendering ──────────────────────────────────────────────────

export async function selectDoc(name) {
    selectedDoc = name;
    renderDocNav();

    const content = document.getElementById('docs-content');
    if (!content) return;

    content.innerHTML = '<div class="docs-empty"><span class="material-icons" style="font-size:24px;animation:spin 1s linear infinite">refresh</span></div>';

    const doc = await fetchDocContent(name);
    if (!doc || !doc.content) {
        showDocsError('Could not load this document. Check the connection to Coral, then try again.', () => selectDoc(name));
        return;
    }

    // Render markdown using marked.js (already loaded globally)
    if (window.marked) {
        content.innerHTML = DOMPurify.sanitize(marked.parse(doc.content));
    } else {
        // Fallback: pre-formatted text
        content.innerHTML = '<pre style="white-space:pre-wrap">' + esc(doc.content) + '</pre>';
    }

    // Highlight code blocks once highlight.js has loaded (they show plain until
    // then, and stay plain if it cannot load).
    const blocks = content.querySelectorAll('pre code');
    if (blocks.length) loadHighlight().then(h => blocks.forEach(b => { if (b.isConnected) h.highlightElement(b); }), () => {});

    // Intercept internal doc links (e.g. "auth.md" → selectDoc("auth"))
    content.querySelectorAll('a').forEach(a => {
        const href = a.getAttribute('href') || '';
        if (href.endsWith('.md') && !href.includes('://')) {
            const docName = href.replace(/\.md$/, '');
            a.href = 'javascript:void(0)';
            a.onclick = (e) => { e.preventDefault(); selectDoc(docName); };
        }
    });
}

// ── Helpers ────────────────────────────────────────────────────────────
