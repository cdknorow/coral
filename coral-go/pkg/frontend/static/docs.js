/* Documentation: browsable agent_docs viewer */

import { escapeHtml as esc, showView } from './utils.js';
import { apiFetch } from './api.js';

let docsList = [];
let selectedDoc = null;

// ── Initialization ─────────────────────────────────────────────────────

export async function showDocsTab() {
    showView('docs-view');
    if (!docsList.length) {
        await fetchDocsList();
        renderDocNav();
        // Auto-select README
        const readme = docsList.find(d => d.name === 'README');
        if (readme) selectDoc(readme.name);
    }
}

// ── API ────────────────────────────────────────────────────────────────

async function fetchDocsList() {
    try {
        docsList = await apiFetch('/api/agent-docs');
    } catch (e) {
        console.error('Failed to fetch docs list:', e);
        docsList = [];
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
        return `<li class="session-list-item${active}" onclick="selectDoc('${esc(d.name)}')">${esc(d.title)}</li>`;
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
        content.innerHTML = '<div class="docs-empty"><p>Failed to load document</p></div>';
        return;
    }

    // Render markdown using marked.js (already loaded globally)
    if (window.marked) {
        content.innerHTML = DOMPurify.sanitize(marked.parse(doc.content));
    } else {
        // Fallback: pre-formatted text
        content.innerHTML = '<pre style="white-space:pre-wrap">' + esc(doc.content) + '</pre>';
    }

    // Highlight code blocks if hljs is available
    content.querySelectorAll('pre code').forEach(block => {
        if (window.hljs) window.hljs.highlightElement(block);
    });

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
