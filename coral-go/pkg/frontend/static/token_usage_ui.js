/* Token usage in the header and the team's token usage view, and the team's knowledge button (moved out of render.js, #4996; no behaviour change). */
import { historyFetch } from './history_source.js';
import { serverFetch, serverForSession, teamFetch, keyLabel } from './server_base.js';
import { formatTokens } from './format.js';
import { _formatCost } from './session_state.js';
import { formatDurationSec as formatDuration, escapeHtml } from './utils.js';
import { importOnce } from './import_once.js';

export async function updateTokenUsage(sessionId) {
    const el = document.getElementById('session-token-usage');
    if (!el) return;
    if (!sessionId) { el.style.display = 'none'; return; }

    try {
        const resp = await serverFetch(serverForSession(null, sessionId), `/api/token-usage?session_id=${encodeURIComponent(sessionId)}`);
        if (!resp.ok) { el.style.display = 'none'; return; }
        const data = await resp.json();
        if (!data.totals.total_tokens || data.totals.total_tokens === 0) { el.style.display = 'none'; return; }

        const parts = [formatTokens(data.totals.total_tokens) + ' tokens'];
        if (data.totals.cost_usd > 0) parts.push(_formatCost(data.totals.cost_usd));
        el.textContent = parts.join(' · ');
        el.style.display = '';
    } catch {
        el.style.display = 'none';
    }
}

export async function updateHistoryTokenUsage(sessionId) {
    const container = document.getElementById('history-token-usage');
    if (!container) return;
    if (!sessionId) { container.style.display = 'none'; return; }

    try {
        const resp = await historyFetch(`/api/token-usage?session_id=${encodeURIComponent(sessionId)}`);
        if (!resp.ok) { container.style.display = 'none'; return; }
        const data = await resp.json();
        const t = data.totals;
        if (!t || !t.total_tokens || t.total_tokens === 0) { container.style.display = 'none'; return; }

        document.getElementById('htu-input').textContent = formatTokens(t.input_tokens || 0);
        document.getElementById('htu-output').textContent = formatTokens(t.output_tokens || 0);
        const cache = (t.cache_read_tokens || 0) + (t.cache_write_tokens || 0);
        document.getElementById('htu-cache').textContent = cache > 0 ? formatTokens(cache) : '—';
        document.getElementById('htu-requests').textContent = String(t.num_sessions || '—');
        document.getElementById('htu-cost').textContent = t.cost_usd > 0 ? _formatCost(t.cost_usd) : '—';
        container.style.display = '';
    } catch (error) {
        if (error.name === 'AbortError') return;
        container.style.display = 'none';
    }
}

export async function getTeamTokenUsage(boardName) {
    try {
        const resp = await teamFetch(boardName, n => `/api/token-usage?board_name=${encodeURIComponent(n)}`);
        if (!resp.ok) return null;
        const data = await resp.json();
        if (!data.totals.total_tokens || data.totals.total_tokens === 0) return null;
        const parts = [formatTokens(data.totals.total_tokens) + ' tokens'];
        if (data.totals.cost_usd > 0) parts.push(_formatCost(data.totals.cost_usd));
        if (data.totals.execution_time_sec > 0) parts.push(formatDuration(data.totals.execution_time_sec));
        return parts.join(' · ');
    } catch {
        return null;
    }
}

export async function showTeamTokenUsage(boardName) {
    const modal = document.getElementById('task-detail-modal');
    const titleEl = document.getElementById('task-detail-modal-title');
    const content = document.getElementById('task-detail-content');
    if (!modal || !content) return;

    titleEl.textContent = `Token Usage — ${keyLabel(boardName)}`;
    content.innerHTML = '<div style="text-align:center;padding:16px;color:var(--text-muted)">Loading...</div>';
    modal.style.display = '';
    modal.onclick = (e) => { if (e.target === modal) { modal.style.display = 'none'; } };

    // Token usage is read from Coral's unified usage API.
    const usageData = await teamFetch(boardName, n => `/api/token-usage?board_name=${encodeURIComponent(n)}`)
        .then(resp => resp.ok ? resp.json() : null)
        .catch(() => null);
    const agents = (usageData?.by_agent || []).map(a => ({
        name: a.agent_name || a.session_id || 'Unknown agent',
        total_input_tokens: a.input_tokens || 0,
        total_output_tokens: a.output_tokens || 0,
        total_cache_read_tokens: a.cache_read_tokens || 0,
        total_cache_write_tokens: a.cache_write_tokens || 0,
        total_cost_usd: a.cost_usd || 0,
        total_requests: a.num_sessions || a.requests || 0,
    })).filter(a => a.total_requests > 0 || a.total_cost_usd > 0);

    // Compute totals
    let totalIn = 0, totalOut = 0, totalCacheR = 0, totalCacheW = 0, totalCost = 0, totalReqs = 0;
    for (const a of agents) {
        totalIn += a.total_input_tokens || 0;
        totalOut += a.total_output_tokens || 0;
        totalCacheR += a.total_cache_read_tokens || 0;
        totalCacheW += a.total_cache_write_tokens || 0;
        totalCost += a.total_cost_usd || 0;
        totalReqs += a.total_requests || 0;
    }

    if (agents.length === 0) {
        content.innerHTML = '<div style="text-align:center;padding:16px;color:var(--text-muted)">No token usage recorded for this team</div>';
        return;
    }

    // Render summary + agent table
    const executionTimeRow = usageData?.totals?.execution_time_sec > 0
        ? `<span class="info-token-label">Execution Time</span><span class="info-token-value">${formatDuration(usageData.totals.execution_time_sec)}</span>`
        : '';
    let html = `<div class="info-token-grid" style="margin-bottom:16px">
        <span class="info-token-label">Input</span><span class="info-token-value">${formatTokens(totalIn)}</span>
        <span class="info-token-label">Output</span><span class="info-token-value">${formatTokens(totalOut)}</span>
        <span class="info-token-label">Cache Read</span><span class="info-token-value">${formatTokens(totalCacheR)}</span>
        <span class="info-token-label">Cache Write</span><span class="info-token-value">${formatTokens(totalCacheW)}</span>
        <span class="info-token-label">Requests</span><span class="info-token-value">${totalReqs}</span>
        <span class="info-token-label">Cost</span><span class="info-token-value">${_formatCost(totalCost)}</span>
        ${executionTimeRow}
    </div>`;

    html += `<table class="cost-table" style="font-size:0.75rem">
        <thead><tr>
            <th>Agent</th>
            <th style="text-align:right">Input</th>
            <th style="text-align:right">Output</th>
            <th style="text-align:right">Cache R</th>
            <th style="text-align:right">Cache W</th>
            <th style="text-align:right">Cost</th>
        </tr></thead><tbody>`;

    for (const a of agents.sort((x, y) => (y.total_cost_usd || 0) - (x.total_cost_usd || 0))) {
        html += `<tr>
            <td style="font-weight:500">${escapeHtml(a.name)}</td>
            <td style="text-align:right">${formatTokens(a.total_input_tokens || 0)}</td>
            <td style="text-align:right">${formatTokens(a.total_output_tokens || 0)}</td>
            <td style="text-align:right">${formatTokens(a.total_cache_read_tokens || 0)}</td>
            <td style="text-align:right">${formatTokens(a.total_cache_write_tokens || 0)}</td>
            <td style="text-align:right;font-weight:600;color:var(--accent)">${_formatCost(a.total_cost_usd || 0)}</td>
        </tr>`;
    }
    html += '</tbody></table>';
    content.innerHTML = html;
}

export async function distillTeamKnowledge(boardName) {
    if (window.enterTeamContext) await window.enterTeamContext(boardName, { tab: 'knowledge' });
    const { distillKnowledgeInTab } = await importOnce('./team_artifacts.js');
    distillKnowledgeInTab(boardName);
}
