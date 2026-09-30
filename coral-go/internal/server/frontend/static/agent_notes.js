/* Agent notes — single markdown document with click-to-edit, blur-to-save */

import { state } from './state.js';
import { showToast } from './utils.js';

// ID of the persisted note row (null until first save)
let _noteId = null;
let _lastSaved = '';
let _isEditing = false;

export async function loadAgentNotes(agentName, sessionId, options) {
    if (!agentName) return;
    const sid = sessionId || (state.currentSession && state.currentSession.session_id);
    try {
        const params = new URLSearchParams();
        if (sid) params.set("session_id", sid);
        const qs = params.toString() ? `?${params}` : "";
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(agentName)}/notes${qs}`, options);
        if (!resp.ok) throw new Error(`notes fetch failed: ${resp.status}`);
        const notes = await resp.json();
        if (sid && state.currentSession?.session_id !== sid) return;
        if (notes.length > 0) {
            _noteId = notes[0].id;
            _lastSaved = notes[0].content || '';
        } else {
            _noteId = null;
            _lastSaved = '';
        }
    } catch (e) {
        if (e?.name === 'AbortError') return;
        if (sid && state.currentSession?.session_id !== sid) return;
        _noteId = null;
        _lastSaved = '';
    }
    // Update badge count
    const countEl = document.getElementById('note-bar-count');
    if (countEl) countEl.textContent = _lastSaved ? '1' : '';
    renderMarkdown();
}

function renderMarkdown() {
    const rendered = document.getElementById('note-md-rendered');
    const editor = document.getElementById('note-md-editor');
    if (!rendered || !editor) return;

    if (_isEditing) return; // don't clobber while editing

    editor.style.display = 'none';
    rendered.style.display = '';

    if (!_lastSaved.trim()) {
        rendered.innerHTML = '<div class="note-md-placeholder">Click to add notes...</div>';
    } else if (typeof marked !== 'undefined') {
        rendered.innerHTML = typeof DOMPurify !== 'undefined' ? DOMPurify.sanitize(marked.parse(_lastSaved)) : marked.parse(_lastSaved);
    } else {
        rendered.textContent = _lastSaved;
    }
}

function enterEditMode() {
    if (_isEditing) return;
    _isEditing = true;

    const rendered = document.getElementById('note-md-rendered');
    const editor = document.getElementById('note-md-editor');
    if (!rendered || !editor) return;

    rendered.style.display = 'none';
    editor.style.display = '';
    editor.value = _lastSaved;
    editor.focus();
}

async function exitEditMode() {
    if (!_isEditing) return;
    _isEditing = false;

    const editor = document.getElementById('note-md-editor');
    if (!editor) return;

    const content = editor.value;
    if (content !== _lastSaved) {
        await saveContent(content);
    }
    renderMarkdown();
}

async function saveContent(content) {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    const agentName = encodeURIComponent(state.currentSession.name);

    try {
        if (_noteId != null) {
            await fetch(`/api/sessions/live/${agentName}/notes/${_noteId}`, {
                method: 'PATCH',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ content }),
            });
        } else {
            const sid = state.currentSession.session_id;
            const resp = await fetch(`/api/sessions/live/${agentName}/notes`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ content, session_id: sid }),
            });
            const created = await resp.json();
            _noteId = created.id;
        }
        _lastSaved = content;
        const countEl = document.getElementById('note-bar-count');
        if (countEl) countEl.textContent = _lastSaved ? '1' : '';
    } catch {
        showToast('Failed to save notes', true);
    }
}

export function initNotesMd() {
    const rendered = document.getElementById('note-md-rendered');
    const editor = document.getElementById('note-md-editor');
    if (!rendered || !editor) return;

    rendered.addEventListener('click', () => enterEditMode());
    editor.addEventListener('blur', () => exitEditMode());
    editor.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') {
            e.preventDefault();
            editor.blur();
        }
    });
}
