/* Persisted changes.diff tab for historical sessions. */

import { historyFetch, historyUrl } from './history_source.js';
import { state } from './state.js';

export async function loadHistoryChanges(sessionId) {
    const button = document.getElementById('history-tab-btn-changes');
    const content = document.getElementById('history-changes-content');
    const download = document.getElementById('history-changes-download');
    if (!button || !content || !download) return;

    button.style.display = 'none';
    content.textContent = '';
    const url = `/api/sessions/${encodeURIComponent(sessionId)}/changes`;
    try { download.href = historyUrl(url); } catch { return; }

    try {
        const resp = await historyFetch(url);
        // Ignore a stale response if the user selected another session.
        if (state.currentSession?.type !== 'history' || state.currentSession?.name !== sessionId) return;
        if (!resp.ok) return;

        content.textContent = await resp.text();
        if (state.currentSession?.type !== 'history' || state.currentSession?.name !== sessionId) return;
        button.style.display = '';
    } catch (e) {
        if (e.name === 'AbortError') return;
        console.error('Failed to load session changes.diff:', e);
    }
}
