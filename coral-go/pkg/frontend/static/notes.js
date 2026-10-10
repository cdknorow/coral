/* Session notes: loading, editing, rendering, and tab switching */

import { state } from './state.js';
import { showToast } from './utils.js';

let currentNotesData = null;
let isEditing = false;
let pollTimer = null;

function hasSummaryContent(data) {
    if (!data) return false;
    return Boolean((data.notes_md || data.auto_summary || "").trim());
}

function updateSummaryActionButton(data, options = {}) {
    const btn = document.getElementById("history-generate-summary-btn");
    if (!btn) return;

    const loading = Boolean(options.loading);
    btn.disabled = loading;
    btn.textContent = loading
        ? "Generating..."
        : (hasSummaryContent(data) ? "Regenerate Summary" : "Generate Summary");
}

/**
 * Extract the first markdown header from notes/summary content
 * and update the history session title.
 */
function updateHistoryTitleFromNotes(data) {
    const content = data.notes_md || data.auto_summary;
    if (!content) return;

    // Match the first markdown header (#, ##, ###, etc.)
    const match = content.match(/^#{1,6}\s+(.+)$/m);
    if (match) {
        const titleEl = document.getElementById("history-session-title");
        if (titleEl) {
            titleEl.textContent = match[1].trim();
        }
    }
}

export async function loadSessionNotes(sessionId) {
    // Reset state
    currentNotesData = null;
    isEditing = false;
    if (pollTimer) {
        clearTimeout(pollTimer);
        pollTimer = null;
    }

    // Reset UI
    document.getElementById("notes-rendered").innerHTML = "";
    document.getElementById("notes-spinner").style.display = "none";
    document.getElementById("notes-edit-area").style.display = "none";
    document.getElementById("notes-edit-btn").textContent = "Edit";
    document.getElementById("notes-rendered").style.display = "";
    updateSummaryActionButton(null);

    try {
        const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/notes`);
        const data = await resp.json();
        currentNotesData = data;
        updateSummaryActionButton(data, { loading: Boolean(data.summarizing) });

        updateHistoryTitleFromNotes(data);

        if (data.summarizing) {
            document.getElementById("notes-spinner").style.display = "flex";
            document.getElementById("notes-rendered").innerHTML = "";
            // Poll for the summary to complete
            pollForSummary(sessionId);
        } else {
            renderNotes(data);
        }
    } catch (e) {
        console.error("Failed to load session notes:", e);
        document.getElementById("notes-rendered").innerHTML =
            '<div class="empty-notes">Failed to load notes</div>';
    }
}

function pollForSummary(sessionId) {
    pollTimer = setTimeout(async () => {
        try {
            const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/notes`);
            const data = await resp.json();
            currentNotesData = data;

            if (data.summarizing) {
                updateSummaryActionButton(data, { loading: true });
                pollForSummary(sessionId);
            } else {
                document.getElementById("notes-spinner").style.display = "none";
                updateHistoryTitleFromNotes(data);
                updateSummaryActionButton(data);
                renderNotes(data);
            }
        } catch (e) {
            document.getElementById("notes-spinner").style.display = "none";
            updateSummaryActionButton(currentNotesData);
            document.getElementById("notes-rendered").innerHTML =
                '<div class="empty-notes">Failed to load summary</div>';
        }
    }, 3000);
}

function renderNotes(data) {
    const container = document.getElementById("notes-rendered");
    document.getElementById("notes-spinner").style.display = "none";

    const content = data.notes_md || data.auto_summary;
    if (!content) {
        container.innerHTML = '<div class="empty-notes">No notes yet. Click "Edit" to add notes, or "Generate Summary" to create an AI summary.</div>';
        return;
    }

    // Show label for auto-summary vs user notes
    let label = "";
    if (data.is_user_edited && data.notes_md) {
        label = "";
    } else if (data.auto_summary) {
        label = '<div style="font-size:11px;color:var(--text-muted);margin-bottom:8px;font-style:italic;">Auto-generated summary</div>';
    }

    try {
        const html = marked.parse(content);
        container.innerHTML = label + (typeof DOMPurify !== 'undefined' ? DOMPurify.sanitize(html) : html);
    } catch (e) {
        // Fallback if marked.js didn't load
        container.innerHTML = label + '<pre>' + content.replace(/</g, '&lt;') + '</pre>';
    }
}

export function toggleNotesEdit() {
    if (isEditing) {
        // Cancel editing
        cancelNotesEdit();
        return;
    }

    isEditing = true;
    document.getElementById("notes-edit-btn").textContent = "Cancel";
    document.getElementById("notes-rendered").style.display = "none";
    document.getElementById("notes-edit-area").style.display = "flex";

    const textarea = document.getElementById("notes-textarea");
    // Pre-fill with user notes, or auto-summary if no user notes
    if (currentNotesData) {
        textarea.value = currentNotesData.notes_md || currentNotesData.auto_summary || "";
    }
    textarea.focus();
}

export function cancelNotesEdit() {
    isEditing = false;
    document.getElementById("notes-edit-btn").textContent = "Edit";
    document.getElementById("notes-rendered").style.display = "";
    document.getElementById("notes-edit-area").style.display = "none";
}

export async function saveNotes() {
    if (!state.currentSession || state.currentSession.type !== "history") return;

    const sessionId = state.currentSession.name;
    const notesMd = document.getElementById("notes-textarea").value;

    try {
        const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/notes`, {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ notes_md: notesMd }),
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
            return;
        }

        // Update local state and re-render
        currentNotesData = {
            notes_md: notesMd,
            auto_summary: currentNotesData ? currentNotesData.auto_summary : "",
            is_user_edited: true,
            updated_at: new Date().toISOString(),
        };
        updateSummaryActionButton(currentNotesData);
        cancelNotesEdit();
        renderNotes(currentNotesData);
        showToast("Notes saved");
    } catch (e) {
        showToast("Failed to save notes", true);
    }
}

export async function generateSummary() {
    if (!state.currentSession || state.currentSession.type !== "history") return;

    const sessionId = state.currentSession.name;
    document.getElementById("notes-spinner").style.display = "flex";
    document.getElementById("notes-rendered").innerHTML = "";
    updateSummaryActionButton(currentNotesData, { loading: true });

    try {
        const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/resummarize`, {
            method: "POST",
        });
        const result = await resp.json();
        document.getElementById("notes-spinner").style.display = "none";

        if (result.error) {
            updateSummaryActionButton(currentNotesData);
            showToast(result.error, true);
            document.getElementById("notes-rendered").innerHTML =
                '<div class="empty-notes">Summarization failed. Is claude-agent-sdk installed?</div>';
            return;
        }

        if (result.auto_summary) {
            currentNotesData = {
                notes_md: currentNotesData ? currentNotesData.notes_md : "",
                auto_summary: result.auto_summary,
                is_user_edited: currentNotesData ? currentNotesData.is_user_edited : false,
                updated_at: new Date().toISOString(),
            };
            updateHistoryTitleFromNotes(currentNotesData);
            updateSummaryActionButton(currentNotesData);
            renderNotes(currentNotesData);
            showToast("Summary generated");
        } else {
            updateSummaryActionButton(currentNotesData);
        }
    } catch (e) {
        document.getElementById("notes-spinner").style.display = "none";
        updateSummaryActionButton(currentNotesData);
        showToast("Failed to generate summary", true);
    }
}

export async function resummarize() {
    return generateSummary();
}

export function switchHistoryTab(tabName) {
    // Update tab buttons — match by onclick attribute since text may include count badges
    document.querySelectorAll(".history-tab-btn").forEach(btn => {
        const onclick = btn.getAttribute('onclick') || '';
        const match = onclick.match(/switchHistoryTab\('([^']+)'\)/);
        const btnTab = match ? match[1] : '';
        btn.classList.toggle("active", btnTab === tabName);
    });

    // Update tab content
    document.querySelectorAll(".history-tab-content").forEach(content => {
        content.classList.remove("active");
    });

    const tabId = `history-tab-${tabName}`;
    const tabContent = document.getElementById(tabId);
    if (tabContent) {
        tabContent.classList.add("active");
    }
}
