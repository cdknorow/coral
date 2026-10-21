/* The agent list's display switches: server names, grouping, summaries, sleeping groups (moved out of render.js, #4996; no behaviour change). */
import { state } from './state.js';
import { redrawLiveList } from './list_hooks.js';
import { isServerShownOnRows, isGroupedByServer, applySessionSummaryPref, isSessionSummaryShown, isBranchShown, isAgentLineShown, setAgentLineShown } from './session_rows.js';
import { browserLocal } from './safe_storage.js';

export async function toggleShowServer() {
    const previous = state.settings?.show_session_server;
    const next = !isServerShownOnRows();
    state.settings = { ...state.settings, show_session_server: next ? 'True' : 'False' };
    redrawLiveList();
    try {
        const resp = await fetch('/api/settings', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ show_session_server: next }),
        });
        if (!resp.ok) throw new Error(`settings ${resp.status}`);
    } catch (e) {
        console.error('Failed to save server display setting:', e);
        state.settings = { ...state.settings, show_session_server: previous };
        redrawLiveList();
    }
}

export function toggleGroupByServer() {
    try { browserLocal.set('coral-group-by-server', isGroupedByServer() ? 'false' : 'true'); } catch { /* per-viewer only */ }
    redrawLiveList();
}

export function toggleShowBranches() {
    try { browserLocal.set('coral-show-branches', isBranchShown() ? 'false' : 'true'); } catch { /* per-viewer only */ }
    redrawLiveList();
}

export function toggleAgentLine(part) {
    setAgentLineShown(part, !isAgentLineShown(part));
    redrawLiveList();
}
export const toggleShowProvider = () => toggleAgentLine('provider');
export const toggleShowModel = () => toggleAgentLine('model');
export const toggleShowThinking = () => toggleAgentLine('thinking');

export async function toggleSessionSummary() {
    const previous = state.settings?.show_session_summary;
    const next = !isSessionSummaryShown();
    state.settings = { ...state.settings, show_session_summary: next ? 'True' : 'False' };
    applySessionSummaryPref();
    try {
        const resp = await fetch('/api/settings', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ show_session_summary: next }),
        });
        if (!resp.ok) throw new Error(`settings ${resp.status}`);
    } catch (e) {
        console.error('Failed to save summary setting:', e);
        state.settings = { ...state.settings, show_session_summary: previous };
        applySessionSummaryPref();
    }
}

export function toggleGroupByTeam() {
    const current = browserLocal.get('coral-group-by-team') !== 'false';
    browserLocal.set('coral-group-by-team', !current ? 'true' : 'false');
    const check = document.getElementById('group-by-team-check-top');
    if (check) check.style.opacity = !current ? '1' : '0.2';
    redrawLiveList();
}

export function expandSleepingGroup(groupName) {
    const key = `coral-sleeping-expanded:${groupName}`;
    const current = browserLocal.get(key) === 'true';
    browserLocal.set(key, current ? 'false' : 'true');
    redrawLiveList();
}
