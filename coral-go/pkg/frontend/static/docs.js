/* Documentation: browsable agent_docs viewer */

import { escapeHtml as esc, showView, jsStr, showToast, renderDocsMarkdown } from './utils.js';
import { apiFetch } from './api.js';
import { loadHighlight } from './vendor_loader.js';
import { note, currentRoute } from './nav.js';

let docsList = [];
let selectedDoc = null;
let wantedDoc = null; // a /docs/<name> page opened before the list loaded (#3532)

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
    // The doc the page names, else README, else the first document; an empty list says so.
    const first = startDoc(wantedDoc);
    if (wantedDoc && (!first || first.name !== wantedDoc)) docNotFound();
    wantedDoc = null;
    if (first) selectDoc(first.name, { noteRoute: false });
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

/** A /docs/<name> page for a doc there isn't: say so, and the page is /docs (#3532, design 4.1). */
function docNotFound() {
    showToast('That document was not found', true);
    if (currentRoute()?.view === 'docs') note({ view: 'docs' }, { replace: true });
}

/** The doc to show: the named one if the list has it, else README, else the first. */
function startDoc(name) {
    return (name && docsList.find(d => d.name === name)) || docsList.find(d => d.name === 'README') || docsList[0];
}

/** Show the doc a /docs[/<name>] page names (the router already wrote the page). */
export function openDoc(name) {
    if (!docsList.length) { wantedDoc = name; return; } // loading (showDocsTab): it opens this one
    const doc = startDoc(name);
    if (name && doc && doc.name !== name) docNotFound();
    if (doc && doc.name !== selectedDoc) selectDoc(doc.name, { noteRoute: false });
}

export async function selectDoc(name, { noteRoute = true } = {}) {
    selectedDoc = name;
    renderDocNav();
    if (noteRoute && currentRoute()?.view === 'docs') note({ view: 'docs', id: name });

    const content = document.getElementById('docs-content');
    if (!content) return;

    content.innerHTML = '<div class="docs-empty"><span class="material-icons" style="font-size:24px;animation:spin 1s linear infinite">refresh</span></div>';

    const doc = await fetchDocContent(name);
    if (!doc || !doc.content) {
        showDocsError('Could not load this document. Check the connection to Coral, then try again.', () => selectDoc(name));
        return;
    }

    // Markdown through the dashboard's shared sanitizer and link policy (#3622 F4): a doc link is a
    // router link, a web link opens a new tab, anything else is text.
    const html = renderDocsMarkdown(doc.content);
    if (html !== null) {
        content.innerHTML = html;
    } else {
        // Fallback: pre-formatted text
        content.innerHTML = '<pre style="white-space:pre-wrap">' + esc(doc.content) + '</pre>';
    }

    // Highlight code blocks once highlight.js has loaded (they show plain until
    // then, and stay plain if it cannot load).
    const blocks = content.querySelectorAll('pre code');
    if (blocks.length) loadHighlight().then(h => blocks.forEach(b => { if (b.isConnected) h.highlightElement(b); }), () => {});

}

// ── Helpers ────────────────────────────────────────────────────────────
