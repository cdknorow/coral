/* Documentation: browsable agent_docs viewer */

import { escapeHtml as esc, showView, showToast, renderDocsMarkdown, escapeAttr, copyText } from './utils.js';
import { registerActions, textOf } from './actions.js';
import { apiFetch } from './api.js';
import { loadHighlight } from './vendor_loader.js';
import { note, currentRoute } from './nav.js';

/** The line the "Copy docs line for agents" button copies: what a user pastes to an agent so it can read
 *  these docs. The agent prompt says the same (internal/agent DocsPromptLine; a Go test keeps them equal). */
export const DOCS_FOR_AGENTS = "Coral's docs for agents: run `coral-agent docs` to list the topics, `coral-agent docs <topic>` to read one (`coral-agent docs all` prints everything).";

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
    if (content) content.innerHTML = '<div class="docs-empty"><span class="material-icons" style="font-size:1.5rem;animation:spin 1s linear infinite" aria-hidden="true">refresh</span></div>';
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

    // The topics are one Tab stop (the open one, or the first), and the arrow keys move between them (#6586): 37 stops stood between the top bar and the page's text.
    const stop = docsList.some(d => d.name === selectedDoc) ? selectedDoc : (docsList[0] || {}).name;
    list.innerHTML = docsList.map(d => {
        const active = selectedDoc === d.name ? ' active' : '';
        // A topic of the Docs list box: the open one is selected; Enter or Space opens the focused one like a click (#4026), the arrows move between them (#6586).
        return `<li class="session-list-item${escapeAttr(active)}" role="option" tabindex="${Number(d.name === stop ? 0 : -1)}" aria-selected="${escapeAttr(selectedDoc === d.name ? 'true' : 'false')}" data-on-click="doc-open" data-on-keydown="doc-open-key" data-doc="${escapeAttr(d.name)}">${esc(d.title)}</li>`;
    }).join('');
}

/** Say which topic is showing, politely (the text changes under a reader without a word otherwise, #6586). */
function sayDocs(text) { const s = document.getElementById('docs-status'); if (s && s.textContent !== text) s.textContent = text; }

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
    const hadFocus = !!document.activeElement?.closest?.('#docs-nav-list'); // the list is drawn again: focus goes back to the topic that was opened
    renderDocNav();
    if (hadFocus) [...document.querySelectorAll('#docs-nav-list [data-doc]')].find(e => e.dataset.doc === name)?.focus({ preventScroll: true });
    if (noteRoute && currentRoute()?.view === 'docs') note({ view: 'docs', id: name });

    const content = document.getElementById('docs-content');
    if (!content) return;

    content.innerHTML = '<div class="docs-empty"><span class="material-icons" style="font-size:1.5rem;animation:spin 1s linear infinite" aria-hidden="true">refresh</span></div>';

    const doc = await fetchDocContent(name);
    if (!doc || !doc.content) {
        showDocsError('Could not load this document. Check the connection to Coral, then try again.', () => selectDoc(name));
        return;
    }

    // Markdown through the dashboard's shared sanitizer and link policy (#3622 F4): a doc link is a
    // router link, a web link opens a new tab, anything else is text.
    const shown = docsList.find(d => d.name === name);
    sayDocs(`Showing ${shown ? shown.title : name}`);
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

/** The Docs page's "Copy docs line for agents" button (its action is registered in app.js: the button is in the page, this file loads with the Docs tab). */
export async function copyDocsLine() {
    if (await copyText(DOCS_FOR_AGENTS)) showToast('Copied');
    else showToast('Could not copy: select the text in the hint and copy it', true);
}

// ── Helpers ────────────────────────────────────────────────────────────

// A doc in the Docs list (#3645).
registerActions({
    'doc-open': el => { const d = textOf(el.dataset.doc, 200); if (d) window.selectDoc(d); },
    'doc-open-key': (el, event) => {
        if (event.target === el && ['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) { // between the topics: the one Tab stop goes with the focus
            const all = [...document.querySelectorAll('#docs-nav-list [data-doc]')], at = all.indexOf(el);
            const to = event.key === 'Home' ? 0 : event.key === 'End' ? all.length - 1 : event.key === 'ArrowDown' ? (at + 1) % all.length : (at + all.length - 1) % all.length;
            event.preventDefault();
            all.forEach((e, i) => { e.tabIndex = i === to ? 0 : -1; });
            all[to].focus();
            return;
        }
        if (event.target !== el || (event.key !== 'Enter' && event.key !== ' ')) return; event.preventDefault(); const d = textOf(el.dataset.doc, 200); if (d) window.selectDoc(d); },
});
