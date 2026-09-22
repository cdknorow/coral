/* Token Usage Dashboard — proxy token tracking and cost UI */

import { showView, escapeHtml, escapeAttr } from './utils.js';
import { state } from './state.js';

function _formatTokens(n) {
    if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
    if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
    return String(n);
}

function _formatCost(c) {
    if (c === 0) return '$0.00';
    if (c < 0.01) return '$' + c.toFixed(4);
    return '$' + c.toFixed(2);
}

function _formatLatency(ms) {
    if (ms == null) return '\u2014';
    if (ms >= 1000) return (ms / 1000).toFixed(1) + 's';
    return ms + 'ms';
}

function _formatDate(isoStr) {
    if (!isoStr) return '\u2014';
    try {
        const d = new Date(isoStr);
        return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
            + ' ' + d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
    } catch { return isoStr; }
}

function _formatTime(isoStr) {
    if (!isoStr) return '\u2014';
    try {
        const d = new Date(isoStr);
        return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
            + ' ' + d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    } catch { return isoStr; }
}

function _formatDuration(startStr, endStr) {
    if (!startStr) return '\u2014';
    try {
        const start = new Date(startStr);
        const end = endStr ? new Date(endStr) : new Date();
        const diffMs = end - start;
        if (diffMs < 0) return '\u2014';
        const secs = Math.floor(diffMs / 1000);
        if (secs < 60) return secs + 's';
        const mins = Math.floor(secs / 60);
        const remSecs = secs % 60;
        if (mins < 60) return mins + 'm ' + remSecs + 's';
        const hrs = Math.floor(mins / 60);
        const remMins = mins % 60;
        return hrs + 'h ' + remMins + 'm';
    } catch { return '\u2014'; }
}

function _formatPRLink(prNumber, remoteURL) {
    if (!prNumber) return '—';
    if (!remoteURL) return `#${prNumber}`;
    let base = remoteURL.replace(/\.git$/, '');
    if (base.startsWith('git@')) {
        const parts = base.split(':');
        if (parts.length === 2) base = 'https://' + parts[0].slice(4) + '/' + parts[1];
    }
    return `<a href="${escapeAttr(base)}/pull/${prNumber}" target="_blank" rel="noopener">#${prNumber}</a>`;
}

function _findSession(sessionId) {
    if (!sessionId || !state.liveSessions) return null;
    return state.liveSessions.find(s => s.session_id === sessionId) || null;
}

let _costRefreshTimer = null;

// ── Sort & Filter State ─────────────────────────────────────

const _sortState = {
    model:   { field: 'cost_usd', asc: false },
    agent:   { field: 'cost_usd', asc: false },
    team:    { field: 'cost_usd', asc: false },
    branch:  { field: 'cost_usd', asc: false },
    task:    { field: 'cost_usd', asc: false },
    request: { field: 'started_at', asc: false },
};

// Per-column dropdown filters: { "table:field": Set of selected values }
// Empty set = show all (no filter active)
const _colFilters = {};

// Raw data caches — populated on fetch, re-rendered on sort/filter
const _rawData = {
    model: [], agent: [], team: [], branch: [], task: [], request: [],
};

// Currently open dropdown (only one at a time)
let _openDropdown = null;

function _toggleSort(table, field) {
    const s = _sortState[table];
    if (s.field === field) {
        s.asc = !s.asc;
    } else {
        s.field = field;
        s.asc = false;
    }
    _rerender(table);
}

function _rerender(table) {
    switch (table) {
        case 'model':   _renderModelTable(_rawData.model); break;
        case 'agent':   _renderAgentTable(_rawData.agent); break;
        case 'team':    _renderTeamTable(_rawData.team); break;
        case 'branch':  _renderBranchTable(_rawData.branch); break;
        case 'task':    _renderTaskTable(_rawData.task); break;
        case 'request': _renderRequestLog(_rawData.request); break;
    }
}

function _sortArrow(table, field) {
    const s = _sortState[table];
    if (s.field !== field) return '';
    return s.asc ? ' \u25B2' : ' \u25BC';
}

function _sortRows(rows, table, fieldAccessor) {
    const s = _sortState[table];
    const sorted = [...rows];
    sorted.sort((a, b) => {
        const va = fieldAccessor(a, s.field);
        const vb = fieldAccessor(b, s.field);
        if (va == null && vb == null) return 0;
        if (va == null) return 1;
        if (vb == null) return -1;
        if (typeof va === 'string') {
            const cmp = va.localeCompare(vb);
            return s.asc ? cmp : -cmp;
        }
        return s.asc ? va - vb : vb - va;
    });
    return sorted;
}

// Apply all active column filters for a table
function _applyColFilters(rows, table, fieldExtractors) {
    let result = rows;
    for (const [field, extractor] of Object.entries(fieldExtractors)) {
        const key = `${table}:${field}`;
        const selected = _colFilters[key];
        if (selected && selected.size > 0) {
            result = result.filter(r => selected.has(extractor(r)));
        }
    }
    return result;
}

function _hasActiveFilter(table, field) {
    const key = `${table}:${field}`;
    const s = _colFilters[key];
    return s && s.size > 0;
}

// Sort header with optional filter icon
function _sortHeader(table, field, label, extraClass, filterable, valueExtractor) {
    const cls = extraClass ? `${extraClass} cost-th-sort` : 'cost-th-sort';
    const arrow = _sortArrow(table, field);
    let filterIcon = '';
    if (filterable) {
        const active = _hasActiveFilter(table, field) ? ' cost-filter-active' : '';
        filterIcon = `<span class="cost-col-filter${active}" onclick="event.stopPropagation(); _costOpenFilter('${table}','${field}', this)" title="Filter"><span class="material-icons" style="font-size:13px">filter_list</span></span>`;
    }
    return `<th class="${cls}" onclick="_costToggleSort('${table}','${field}')">${label}${arrow}${filterIcon}</th>`;
}

// Open/close the column filter dropdown
function _openFilter(table, field, anchorEl) {
    _closeDropdown();

    const key = `${table}:${field}`;
    const data = _rawData[table] || [];
    const extractor = _getFilterExtractor(table, field);

    // Collect unique values
    const uniqueVals = [...new Set(data.map(r => extractor(r)))].filter(v => v).sort();
    if (uniqueVals.length === 0) return;

    const selected = _colFilters[key] || new Set();

    // Create dropdown
    const dropdown = document.createElement('div');
    dropdown.className = 'cost-filter-dropdown';
    dropdown.onclick = (e) => e.stopPropagation();

    // Search input
    const search = document.createElement('input');
    search.type = 'search';
    search.className = 'cost-filter-search';
    search.placeholder = 'Search...';
    search.autocomplete = 'off';
    dropdown.appendChild(search);

    // Actions row
    const actions = document.createElement('div');
    actions.className = 'cost-filter-actions';
    actions.innerHTML = `<button class="cost-filter-action-btn" data-action="all">Select All</button><button class="cost-filter-action-btn" data-action="none">Clear</button>`;
    dropdown.appendChild(actions);

    // Items container
    const itemsContainer = document.createElement('div');
    itemsContainer.className = 'cost-filter-items';
    dropdown.appendChild(itemsContainer);

    function renderItems(query) {
        const q = (query || '').toLowerCase();
        const filtered = q ? uniqueVals.filter(v => v.toLowerCase().includes(q)) : uniqueVals;
        itemsContainer.innerHTML = filtered.map(val => {
            const checked = selected.size === 0 || selected.has(val) ? 'checked' : '';
            return `<label class="cost-filter-item"><input type="checkbox" ${checked} data-val="${escapeHtml(val)}"><span>${escapeHtml(val)}</span></label>`;
        }).join('');
    }

    renderItems('');

    search.addEventListener('input', () => renderItems(search.value));

    // Handle checkbox changes
    itemsContainer.addEventListener('change', (e) => {
        const cb = e.target;
        if (!cb.dataset.val) return;
        if (cb.checked) {
            // If all are now checked, clear filter (show all)
            const allChecked = itemsContainer.querySelectorAll('input[type=checkbox]');
            const checkedCount = [...allChecked].filter(c => c.checked).length;
            if (checkedCount === uniqueVals.length) {
                _colFilters[key] = new Set();
            } else {
                if (!_colFilters[key]) _colFilters[key] = new Set();
                _colFilters[key].add(cb.dataset.val);
            }
        } else {
            // If unchecking and no filter exists yet, create one with all except this
            if (!_colFilters[key] || _colFilters[key].size === 0) {
                _colFilters[key] = new Set(uniqueVals.filter(v => v !== cb.dataset.val));
            } else {
                _colFilters[key].delete(cb.dataset.val);
            }
        }
        _rerender(table);
    });

    // Select All / Clear
    actions.addEventListener('click', (e) => {
        const action = e.target.dataset.action;
        if (action === 'all') {
            _colFilters[key] = new Set();
            renderItems(search.value);
            _rerender(table);
        } else if (action === 'none') {
            _colFilters[key] = new Set(['__none__']); // impossible value to match nothing
            itemsContainer.querySelectorAll('input[type=checkbox]').forEach(c => c.checked = false);
            _rerender(table);
        }
    });

    // Position relative to anchor
    const rect = anchorEl.getBoundingClientRect();
    dropdown.style.position = 'fixed';
    dropdown.style.top = (rect.bottom + 4) + 'px';
    dropdown.style.left = Math.min(rect.left, window.innerWidth - 220) + 'px';
    document.body.appendChild(dropdown);

    _openDropdown = dropdown;

    // Close on outside click
    setTimeout(() => {
        document.addEventListener('click', _closeDropdown, { once: true });
    }, 0);
}

function _closeDropdown() {
    if (_openDropdown) {
        _openDropdown.remove();
        _openDropdown = null;
    }
}

function _getFilterExtractor(table, field) {
    const extractors = {
        'model:model': r => r.model || '',
        'agent:agent_name': r => r.display_name || r.agent_name || '',
        'agent:board_name': r => r.board_name || '',
        'agent:folder': r => r.folder || '',
        'team:board_name': r => r.board_name || '(no team)',
        'branch:branch': r => (r.repo_name && r.branch ? `${r.repo_name} : ${r.branch}` : r.branch) || '(unknown)',
        'branch:board_name': r => r.board_name || '(no team)',
        'task:assigned_to': r => r.assigned_to || '',
        'task:priority': r => r.priority || 'medium',
        'request:model_used': r => r.model_used || '',
        'request:status': r => r.status || '',
    };
    return extractors[`${table}:${field}`] || (() => '');
}

// Expose to window for onclick handlers
window._costToggleSort = _toggleSort;
window._costOpenFilter = _openFilter;

export async function showCostDashboard() {
    showView('cost-dashboard-view');
    await _refreshCostDashboard();
    stopCostDashboard(); // clear any existing timer
    _costRefreshTimer = setInterval(_refreshCostDashboard, 15000);
}

export function stopCostDashboard() {
    if (_costRefreshTimer) {
        clearInterval(_costRefreshTimer);
        _costRefreshTimer = null;
    }
}

export async function _refreshCostDashboard() {
    const period = document.getElementById('cost-time-range')?.value || 'day';

    // Map period to a 'since' timestamp for the unified token-usage API
    const sinceMap = { 'hour': 1, 'day': 24, 'week': 168, 'month': 720, 'all': 0 };
    const hoursAgo = sinceMap[period] || 24;
    const since = hoursAgo > 0 ? new Date(Date.now() - hoursAgo * 3600000).toISOString() : '';
    const sinceParam = since ? `?since=${encodeURIComponent(since)}` : '';

    // Choose time-series interval based on period
    const intervalMap = { 'hour': '5m', 'day': '1h', 'week': '1h', 'month': '1d', 'all': '1d' };
    const interval = intervalMap[period] || '1h';
    const tsParams = since
        ? `?since=${encodeURIComponent(since)}&interval=${interval}`
        : `?interval=${interval}`;

    // Flash live indicator
    _flashLiveIndicator();

    try {
        const [summaryResp, reqResp, taskResp, tsResp, teamResp, branchResp] = await Promise.all([
            fetch(`/api/token-usage/summary${sinceParam}`).catch(() => null),
            fetch('/api/proxy/requests?limit=100').catch(() => null),
            fetch('/api/board/tasks').catch(() => null),
            fetch(`/api/token-usage/timeseries${tsParams}`).catch(() => null),
            fetch(`/api/token-usage/by-team${sinceParam}`).catch(() => null),
            fetch(`/api/token-usage/by-branch${sinceParam}`).catch(() => null),
        ]);

        if (summaryResp && summaryResp.ok) {
            const data = await summaryResp.json();
            const t = data.totals || {};
            _setText('cost-input-tokens', _formatTokens(t.input_tokens || 0));
            _setText('cost-output-tokens', _formatTokens(t.output_tokens || 0));
            _setText('cost-cache-read', _formatTokens(t.cache_read_tokens || 0));
            _setText('cost-cache-write', _formatTokens(t.cache_write_tokens || 0));
            _setText('cost-total-requests', String(t.num_sessions || 0));
            _setText('cost-total-cost', _formatCost(t.cost_usd || 0));

            // Render per-agent-type breakdown as model table
            _renderModelTable((data.by_agent_type || []).map(a => ({
                model: a.agent_type || 'unknown',
                requests: a.num_sessions || 0,
                input_tokens: a.input_tokens || 0,
                output_tokens: a.output_tokens || 0,
                cache_read_tokens: a.cache_read_tokens || 0,
                cache_write_tokens: a.cache_write_tokens || 0,
                cost_usd: a.cost_usd || 0,
            })));

            // Render per-agent (session) breakdown
            _renderAgentTable((data.by_agent || []).map(a => ({
                session_id: a.session_id,
                agent_name: a.agent_name,
                display_name: a.agent_name || 'Agent',
                board_name: a.board_name || '',
                folder: a.folder || '',
                working_dir: a.working_dir || '',
                requests: a.requests || 0,
                input_tokens: a.input_tokens || 0,
                output_tokens: a.output_tokens || 0,
                cache_read_tokens: a.cache_read_tokens || 0,
                cache_write_tokens: a.cache_write_tokens || 0,
                cost_usd: a.cost_usd || 0,
                first_seen: a.first_seen || '',
                last_seen: a.last_seen || '',
                launched_at: a.launched_at || '',
                stopped_at: a.stopped_at || '',
            })));
        }

        // Render time-series chart and sparklines
        if (tsResp && tsResp.ok) {
            const data = await tsResp.json();
            const buckets = data.buckets || [];
            _renderSpendChart(buckets);
            _renderSparklines(buckets);
            _renderBurnRate(buckets, hoursAgo);
        }

        // Render team breakdown
        if (teamResp && teamResp.ok) {
            const data = await teamResp.json();
            _renderTeamTable(data.teams || []);
        }

        // Render branch breakdown
        if (branchResp && branchResp.ok) {
            const data = await branchResp.json();
            _renderBranchTable(data.branches || []);
        }

        if (reqResp && reqResp.ok) {
            const data = await reqResp.json();
            _renderRequestLog(data.requests || []);
        }

        if (taskResp && taskResp.ok) {
            const data = await taskResp.json();
            _renderTaskTable(data.tasks || []);
        }
    } catch (e) {
        console.error('[cost-dashboard] refresh error:', e);
    }
}

export function _costTimeRangeChanged() {
    _refreshCostDashboard();
}

function _setText(id, text) {
    const el = document.getElementById(id);
    if (el) el.textContent = text;
}

function _renderModelTable(rows) {
    _rawData.model = rows;
    const container = document.getElementById('cost-by-model');
    if (!container) return;

    const filtered = _applyColFilters(rows, 'model', { model: r => r.model || '' });
    const sorted = _sortRows(filtered, 'model', (r, f) => {
        if (f === 'model') return r.model || '';
        return r[f] || 0;
    });

    if (sorted.length === 0) {
        container.innerHTML = '<div class="cost-empty">No usage data yet</div>';
        return;
    }

    let html = `<table class="cost-table">
        <thead><tr>
            ${_sortHeader('model', 'model', 'Model', '', true)}
            ${_sortHeader('model', 'requests', 'Requests', 'cost-col-right')}
            ${_sortHeader('model', 'input_tokens', 'Input', 'cost-col-right')}
            ${_sortHeader('model', 'output_tokens', 'Output', 'cost-col-right')}
            ${_sortHeader('model', 'cache_read_tokens', 'Cache Read', 'cost-col-right')}
            ${_sortHeader('model', 'cache_write_tokens', 'Cache Write', 'cost-col-right')}
            ${_sortHeader('model', 'cost_usd', 'Cost', 'cost-col-right')}
        </tr></thead><tbody>`;

    for (const r of sorted) {
        html += `<tr>
            <td class="cost-model-name">${escapeHtml(r.model)}</td>
            <td class="cost-col-right">${r.requests}</td>
            <td class="cost-col-right">${_formatTokens(r.input_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(r.output_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(r.cache_read_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(r.cache_write_tokens || 0)}</td>
            <td class="cost-col-right cost-cost-cell">${_formatCost(r.cost_usd)}</td>
        </tr>`;
    }

    html += '</tbody></table>';
    container.innerHTML = html;
}

function _renderAgentTable(rows) {
    _rawData.agent = rows;
    const container = document.getElementById('cost-by-agent');
    if (!container) return;

    const filtered = _applyColFilters(rows, 'agent', {
        agent_name: r => r.display_name || r.agent_name || '',
        board_name: r => r.board_name || '',
        folder: r => r.folder || '',
    });
    const sorted = _sortRows(filtered, 'agent', (r, f) => {
        if (f === 'agent_name') return r.display_name || r.agent_name || '';
        if (f === 'board_name') return r.board_name || '';
        if (f === 'folder') return r.folder || '';
        if (f === 'first_seen') return r.first_seen || '';
        return r[f] || 0;
    });

    if (sorted.length === 0) {
        container.innerHTML = '<div class="cost-empty">No usage data yet</div>';
        return;
    }

    let html = `<table class="cost-table">
        <thead><tr>
            ${_sortHeader('agent', 'agent_name', 'Agent', '', true)}
            ${_sortHeader('agent', 'board_name', 'Team', '', true)}
            ${_sortHeader('agent', 'folder', 'Folder', '', true)}
            ${_sortHeader('agent', 'first_seen', 'Launched', '')}
            ${_sortHeader('agent', 'duration', 'Duration', 'cost-col-right')}
            ${_sortHeader('agent', 'requests', 'Requests', 'cost-col-right')}
            ${_sortHeader('agent', 'input_tokens', 'Input', 'cost-col-right')}
            ${_sortHeader('agent', 'output_tokens', 'Output', 'cost-col-right')}
            ${_sortHeader('agent', 'cache_read_tokens', 'Cache Read', 'cost-col-right')}
            ${_sortHeader('agent', 'cache_write_tokens', 'Cache Write', 'cost-col-right')}
            ${_sortHeader('agent', 'cost_usd', 'Cost', 'cost-col-right')}
        </tr></thead><tbody>`;

    for (const r of sorted) {
        const session = _findSession(r.session_id);
        const displayName = r.display_name || (session && session.display_name) || r.agent_name || r.session_id || '\u2014';
        let nameHtml;
        if (session) {
            nameHtml = `<a href="#" class="cost-agent-link" onclick="event.preventDefault(); switchNavTab('agents'); selectLiveSession('${escapeAttr(session.name)}', '${escapeAttr(session.agent_type)}', '${escapeAttr(session.session_id)}')">${escapeHtml(displayName)}</a>`;
        } else if (r.session_id) {
            const endedSuffix = r.is_live === false ? ' <span class="cost-agent-ended">(ended)</span>' : '';
            nameHtml = `<a href="#" class="cost-agent-link cost-agent-terminated" onclick="event.preventDefault(); switchNavTab('history'); selectHistorySession('${escapeAttr(r.session_id)}')">${escapeHtml(displayName)}</a>${endedSuffix}`;
        } else {
            nameHtml = escapeHtml(displayName);
        }
        const teamHtml = r.board_name ? escapeHtml(r.board_name) : '\u2014';
        const folderHtml = r.folder
            ? `<span title="${escapeAttr(r.working_dir || r.folder)}">${escapeHtml(r.folder)}</span>`
            : '\u2014';
        const launchTime = r.launched_at || r.first_seen;
        const endTime = r.stopped_at || r.last_seen;
        const launchedHtml = _formatTime(launchTime);
        const durationHtml = _formatDuration(launchTime, endTime);
        html += `<tr data-session-id="${escapeAttr(r.session_id || '')}">
            <td class="cost-agent-name">${nameHtml}</td>
            <td class="cost-agent-team">${teamHtml}</td>
            <td class="cost-agent-team">${folderHtml}</td>
            <td>${launchedHtml}</td>
            <td class="cost-col-right">${durationHtml}</td>
            <td class="cost-col-right">${r.requests}</td>
            <td class="cost-col-right">${_formatTokens(r.input_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(r.output_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(r.cache_read_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(r.cache_write_tokens || 0)}</td>
            <td class="cost-col-right cost-cost-cell">${_formatCost(r.cost_usd)}</td>
        </tr>`;
    }

    html += '</tbody></table>';
    container.innerHTML = html;

    // Attach hover chart handlers
    _attachAgentChartHovers(container);
}

// ── Turns-vs-Cost Hover Chart ─────────────────────────────────

const _turnsCache = {};
let _chartTooltip = null;

function _ensureChartTooltip() {
    if (_chartTooltip) return _chartTooltip;
    _chartTooltip = document.createElement('div');
    _chartTooltip.className = 'agent-cost-chart-tooltip';
    document.body.appendChild(_chartTooltip);
    return _chartTooltip;
}

function _attachAgentChartHovers(container) {
    const rows = container.querySelectorAll('tr[data-session-id]');
    for (const row of rows) {
        const sessionId = row.getAttribute('data-session-id');
        if (!sessionId) continue;

        row.addEventListener('mouseenter', async (e) => {
            const tooltip = _ensureChartTooltip();

            // Fetch turns data (cached)
            if (!_turnsCache[sessionId]) {
                try {
                    const resp = await fetch(`/api/token-usage/session/${encodeURIComponent(sessionId)}/turns`);
                    if (resp.ok) {
                        const data = await resp.json();
                        _turnsCache[sessionId] = data.turns || [];
                    } else {
                        _turnsCache[sessionId] = [];
                    }
                } catch { _turnsCache[sessionId] = []; }
            }

            const turns = _turnsCache[sessionId];
            if (turns.length < 2) { tooltip.style.display = 'none'; return; }

            tooltip.innerHTML = `<div class="chart-title">Cumulative Cost over ${turns.length} turns</div>` + _renderCostSVG(turns);

            // Position near cursor
            const x = Math.min(e.clientX + 16, window.innerWidth - 340);
            const y = Math.min(e.clientY - 100, window.innerHeight - 220);
            tooltip.style.left = x + 'px';
            tooltip.style.top = Math.max(8, y) + 'px';
            tooltip.style.display = 'block';
        });

        row.addEventListener('mouseleave', () => {
            const tooltip = _ensureChartTooltip();
            tooltip.style.display = 'none';
        });
    }
}

function _renderCostSVG(turns) {
    const W = 300, H = 160, PAD = 32;
    const plotW = W - PAD * 2, plotH = H - PAD * 2;

    const maxCost = turns[turns.length - 1].cumulative_cost || 1;
    const maxTurn = turns.length;

    // Build polyline points
    const points = turns.map((t, i) => {
        const x = PAD + (i / (maxTurn - 1)) * plotW;
        const y = PAD + plotH - (t.cumulative_cost / maxCost) * plotH;
        return `${x.toFixed(1)},${y.toFixed(1)}`;
    });

    // Area fill points (close path at bottom)
    const areaPoints = [...points, `${(PAD + plotW).toFixed(1)},${(PAD + plotH).toFixed(1)}`, `${PAD.toFixed(1)},${(PAD + plotH).toFixed(1)}`];

    return `<svg width="${W}" height="${H}" viewBox="0 0 ${W} ${H}">
        <line class="chart-grid" x1="${PAD}" y1="${PAD}" x2="${PAD}" y2="${PAD + plotH}" />
        <line class="chart-grid" x1="${PAD}" y1="${PAD + plotH}" x2="${PAD + plotW}" y2="${PAD + plotH}" />
        <polygon class="chart-area" points="${areaPoints.join(' ')}" />
        <polyline class="chart-line" points="${points.join(' ')}" />
        <text class="chart-axis-label" x="${PAD - 4}" y="${PAD + 4}" text-anchor="end">${_formatCost(maxCost)}</text>
        <text class="chart-axis-label" x="${PAD - 4}" y="${PAD + plotH + 4}" text-anchor="end">$0</text>
        <text class="chart-axis-label" x="${PAD}" y="${PAD + plotH + 16}" text-anchor="start">1</text>
        <text class="chart-axis-label" x="${PAD + plotW}" y="${PAD + plotH + 16}" text-anchor="end">${maxTurn}</text>
    </svg>`;
}

// Cached tasks for click-to-detail
let _costTaskCache = [];

function _showCostTaskDetail(taskId) {
    // Merge into state so showTaskDetailModal can find it
    const existing = state.currentBoardTasks || [];
    const task = _costTaskCache.find(t => t.id === taskId);
    if (task && !existing.find(t => t.id === taskId)) {
        state.currentBoardTasks = [...existing, task];
    }
    window.showTaskDetailModal(taskId);
}
window._showCostTaskDetail = _showCostTaskDetail;

function _renderTaskTable(tasks) {
    const container = document.getElementById('cost-by-task');
    if (!container) return;

    // Only show tasks with cost data
    const costTasks = tasks.filter(t => t.cost_usd > 0);
    _rawData.task = costTasks;

    const filtered = _applyColFilters(costTasks, 'task', {
        assigned_to: t => t.assigned_to || '',
        priority: t => t.priority || 'medium',
    });
    const priorityOrder = { critical: 0, high: 1, medium: 2, low: 3 };
    const sorted = _sortRows(filtered, 'task', (t, f) => {
        if (f === 'title') return t.title || '';
        if (f === 'assigned_to') return t.assigned_to || '';
        if (f === 'priority') return priorityOrder[t.priority] ?? 2;
        if (f === 'claimed_at') return t.claimed_at || '';
        return t[f] || 0;
    });

    _costTaskCache = sorted;

    if (sorted.length === 0) {
        container.innerHTML = '<div class="cost-empty">No task data yet</div>';
        return;
    }

    const statusIcon = (status) => {
        if (status === 'completed') return '<span class="material-icons board-task-status-icon completed" style="font-size:14px">check_circle</span>';
        if (status === 'in_progress') return '<span class="task-spinner" title="In progress"></span>';
        if (status === 'skipped') return '<span class="material-icons board-task-status-icon skipped" style="font-size:14px">block</span>';
        return '<span class="material-icons board-task-status-icon pending" style="font-size:14px">radio_button_unchecked</span>';
    };

    let html = `<table class="cost-table">
        <thead><tr>
            <th style="width:24px"></th>
            ${_sortHeader('task', 'title', 'Task', '')}
            ${_sortHeader('task', 'assigned_to', 'Agent', '', true)}
            ${_sortHeader('task', 'priority', 'Priority', '', true)}
            ${_sortHeader('task', 'claimed_at', 'Start Time', '')}
            ${_sortHeader('task', 'duration', 'Duration', 'cost-col-right')}
            ${_sortHeader('task', 'input_tokens', 'Input', 'cost-col-right')}
            ${_sortHeader('task', 'output_tokens', 'Output', 'cost-col-right')}
            ${_sortHeader('task', 'cache_read_tokens', 'Cache Read', 'cost-col-right')}
            ${_sortHeader('task', 'cache_write_tokens', 'Cache Write', 'cost-col-right')}
            ${_sortHeader('task', 'cost_usd', 'Cost', 'cost-col-right')}
        </tr></thead><tbody>`;

    for (const t of sorted) {
        const priorityClass = 'board-task-priority-' + (t.priority || 'medium');
        const startTime = t.claimed_at ? _formatTime(t.claimed_at) : '\u2014';
        const duration = _formatDuration(t.claimed_at, t.completed_at);
        html += `<tr onclick="_showCostTaskDetail(${t.id})" style="cursor:pointer">
            <td>${statusIcon(t.status)}</td>
            <td>${escapeHtml(t.title || '')}</td>
            <td class="cost-agent-name">${escapeHtml(t.assigned_to || '\u2014')}</td>
            <td><span class="board-task-priority ${priorityClass}">${escapeHtml(t.priority || 'medium')}</span></td>
            <td>${startTime}</td>
            <td class="cost-col-right">${duration}</td>
            <td class="cost-col-right">${_formatTokens(t.input_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(t.output_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(t.cache_read_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(t.cache_write_tokens || 0)}</td>
            <td class="cost-col-right cost-cost-cell">${_formatCost(t.cost_usd || 0)}</td>
        </tr>`;
    }

    html += '</tbody></table>';
    container.innerHTML = html;
}

function _renderRequestLog(requests) {
    _rawData.request = requests;
    const container = document.getElementById('cost-request-log');
    if (!container) return;

    const filtered = _applyColFilters(requests, 'request', {
        model_used: r => r.model_used || '',
        status: r => r.status || '',
    });
    const sorted = _sortRows(filtered, 'request', (r, f) => {
        if (f === 'model_used') return r.model_used || '';
        if (f === 'agent_name') return r.agent_name || '';
        if (f === 'status') return r.status || '';
        if (f === 'started_at') return r.started_at || '';
        return r[f] || 0;
    });

    if (sorted.length === 0) {
        container.innerHTML = '<div class="cost-empty">No requests yet</div>';
        return;
    }

    let html = `<table class="cost-table">
        <thead><tr>
            ${_sortHeader('request', 'model_used', 'Model', '', true)}
            ${_sortHeader('request', 'agent_name', 'Agent', '')}
            ${_sortHeader('request', 'input_tokens', 'Input', 'cost-col-right')}
            ${_sortHeader('request', 'output_tokens', 'Output', 'cost-col-right')}
            ${_sortHeader('request', 'cache_read_tokens', 'Cache R', 'cost-col-right')}
            ${_sortHeader('request', 'cache_write_tokens', 'Cache W', 'cost-col-right')}
            ${_sortHeader('request', 'cost_usd', 'Cost', 'cost-col-right')}
            ${_sortHeader('request', 'latency_ms', 'Latency', 'cost-col-right')}
            ${_sortHeader('request', 'status', 'Status', '', true)}
            ${_sortHeader('request', 'started_at', 'Time', '')}
        </tr></thead><tbody>`;

    for (const r of sorted) {
        const session = _findSession(r.session_id);
        const agentName = r.display_name || (session && session.display_name) || r.agent_name || '\u2014';
        const statusClass = r.status === 'completed' ? 'cost-status-ok'
            : r.status === 'error' ? 'cost-status-error'
            : 'cost-status-pending';
        html += `<tr>
            <td class="cost-model-name">${escapeHtml(r.model_used)}</td>
            <td class="cost-agent-name">${escapeHtml(agentName)}</td>
            <td class="cost-col-right">${_formatTokens(r.input_tokens)}</td>
            <td class="cost-col-right">${_formatTokens(r.output_tokens)}</td>
            <td class="cost-col-right">${_formatTokens(r.cache_read_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(r.cache_write_tokens || 0)}</td>
            <td class="cost-col-right cost-cost-cell">${_formatCost(r.cost_usd)}</td>
            <td class="cost-col-right">${_formatLatency(r.latency_ms)}</td>
            <td><span class="cost-status ${statusClass}">${escapeHtml(r.status)}</span></td>
            <td class="cost-date">${_formatDate(r.started_at)}</td>
        </tr>`;
    }

    html += '</tbody></table>';
    container.innerHTML = html;
}

// ── Live Indicator ───────────────────────────────────────────

function _flashLiveIndicator() {
    const el = document.getElementById('cost-live-indicator');
    if (!el) return;
    el.classList.add('cost-live-flash');
    setTimeout(() => el.classList.remove('cost-live-flash'), 1000);
}

// ── Cumulative Spend Chart ───────────────────────────────────

function _renderSpendChart(buckets) {
    const container = document.getElementById('cost-spend-chart');
    if (!container) return;

    if (buckets.length < 2) {
        container.innerHTML = '<div class="cost-empty">Not enough data for chart</div>';
        return;
    }

    const W = container.clientWidth || 600;
    const H = 180;
    const PAD = { top: 24, right: 16, bottom: 32, left: 56 };
    const plotW = W - PAD.left - PAD.right;
    const plotH = H - PAD.top - PAD.bottom;

    const maxCost = buckets[buckets.length - 1].cumulative_cost || 1;
    const n = buckets.length;

    // Build polyline for cumulative cost
    const points = buckets.map((b, i) => {
        const x = PAD.left + (i / (n - 1)) * plotW;
        const y = PAD.top + plotH - (b.cumulative_cost / maxCost) * plotH;
        return `${x.toFixed(1)},${y.toFixed(1)}`;
    });

    // Area fill
    const areaPoints = [
        `${PAD.left.toFixed(1)},${(PAD.top + plotH).toFixed(1)}`,
        ...points,
        `${(PAD.left + plotW).toFixed(1)},${(PAD.top + plotH).toFixed(1)}`
    ];

    // Bar chart for per-bucket cost
    const maxBucketCost = Math.max(...buckets.map(b => b.cost_usd)) || 1;
    const barW = Math.max(1, (plotW / n) - 1);
    let bars = '';
    for (let i = 0; i < n; i++) {
        const x = PAD.left + (i / n) * plotW;
        const barH = (buckets[i].cost_usd / maxBucketCost) * plotH * 0.4;
        const y = PAD.top + plotH - barH;
        bars += `<rect class="spend-bar" x="${x.toFixed(1)}" y="${y.toFixed(1)}" width="${barW.toFixed(1)}" height="${barH.toFixed(1)}" rx="1"/>`;
    }

    // Y-axis labels
    const midCost = maxCost / 2;

    // X-axis labels (first, middle, last)
    const formatBucketLabel = (b) => {
        try {
            const d = new Date(b);
            return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
        } catch { return ''; }
    };

    const svg = `<svg width="${W}" height="${H}" viewBox="0 0 ${W} ${H}" class="spend-chart-svg">
        <!-- Grid lines -->
        <line class="spend-grid" x1="${PAD.left}" y1="${PAD.top}" x2="${PAD.left + plotW}" y2="${PAD.top}" />
        <line class="spend-grid" x1="${PAD.left}" y1="${PAD.top + plotH / 2}" x2="${PAD.left + plotW}" y2="${PAD.top + plotH / 2}" stroke-dasharray="4,4" />
        <line class="spend-grid" x1="${PAD.left}" y1="${PAD.top + plotH}" x2="${PAD.left + plotW}" y2="${PAD.top + plotH}" />
        <!-- Bars -->
        ${bars}
        <!-- Area + Line -->
        <polygon class="spend-area" points="${areaPoints.join(' ')}" />
        <polyline class="spend-line" points="${points.join(' ')}" />
        <!-- Dot on last point -->
        <circle class="spend-dot" cx="${points[points.length - 1].split(',')[0]}" cy="${points[points.length - 1].split(',')[1]}" r="3" />
        <!-- Y labels -->
        <text class="spend-label" x="${PAD.left - 8}" y="${PAD.top + 4}" text-anchor="end">${_formatCost(maxCost)}</text>
        <text class="spend-label" x="${PAD.left - 8}" y="${PAD.top + plotH / 2 + 4}" text-anchor="end">${_formatCost(midCost)}</text>
        <text class="spend-label" x="${PAD.left - 8}" y="${PAD.top + plotH + 4}" text-anchor="end">$0</text>
        <!-- X labels -->
        <text class="spend-label" x="${PAD.left}" y="${H - 4}" text-anchor="start">${formatBucketLabel(buckets[0].bucket)}</text>
        <text class="spend-label" x="${PAD.left + plotW / 2}" y="${H - 4}" text-anchor="middle">${formatBucketLabel(buckets[Math.floor(n / 2)].bucket)}</text>
        <text class="spend-label" x="${PAD.left + plotW}" y="${H - 4}" text-anchor="end">${formatBucketLabel(buckets[n - 1].bucket)}</text>
    </svg>`;

    container.innerHTML = svg;
}

// ── Sparklines in Summary Cards ──────────────────────────────

function _renderMiniSparkline(containerId, values, color) {
    const container = document.getElementById(containerId);
    if (!container || values.length < 2) {
        if (container) container.innerHTML = '';
        return;
    }

    const W = 80, H = 24;
    const max = Math.max(...values) || 1;
    const n = values.length;

    const points = values.map((v, i) => {
        const x = (i / (n - 1)) * W;
        const y = H - (v / max) * (H - 2) - 1;
        return `${x.toFixed(1)},${y.toFixed(1)}`;
    });

    container.innerHTML = `<svg width="${W}" height="${H}" viewBox="0 0 ${W} ${H}">
        <polyline fill="none" stroke="${color}" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" points="${points.join(' ')}" />
    </svg>`;
}

function _renderSparklines(buckets) {
    if (buckets.length < 2) return;

    _renderMiniSparkline('cost-spark-input', buckets.map(b => b.input_tokens), '#58a6ff');
    _renderMiniSparkline('cost-spark-output', buckets.map(b => b.output_tokens), '#3fb950');
    _renderMiniSparkline('cost-spark-cache-read', buckets.map(b => b.cache_read_tokens), '#d29922');
    _renderMiniSparkline('cost-spark-cache-write', buckets.map(b => b.cache_write_tokens), '#bc8cff');
    _renderMiniSparkline('cost-spark-requests', buckets.map(b => b.num_requests), '#8b949e');
    _renderMiniSparkline('cost-spark-cost', buckets.map(b => b.cumulative_cost), '#58a6ff');
}

// ── Burn Rate ────────────────────────────────────────────────

function _renderBurnRate(buckets, periodHours) {
    const el = document.getElementById('cost-burn-rate');
    if (!el || buckets.length === 0) return;

    const totalCost = buckets[buckets.length - 1].cumulative_cost || 0;

    if (totalCost === 0 || periodHours === 0) {
        el.textContent = '$0.00/hr';
        return;
    }

    // Calculate actual time span from data
    const firstTime = new Date(buckets[0].bucket).getTime();
    const lastTime = new Date(buckets[buckets.length - 1].bucket).getTime();
    const spanHours = Math.max((lastTime - firstTime) / 3600000, 1);

    const rate = totalCost / spanHours;
    el.textContent = _formatCost(rate) + '/hr';
}

// ── Team Table ───────────────────────────────────────────────

function _renderTeamTable(teams) {
    _rawData.team = teams;
    const container = document.getElementById('cost-by-team');
    if (!container) return;

    const filtered = _applyColFilters(teams, 'team', { board_name: t => t.board_name || '(no team)' });
    const sorted = _sortRows(filtered, 'team', (t, f) => {
        if (f === 'board_name') return t.board_name || '';
        return t[f] || 0;
    });

    if (sorted.length === 0) {
        container.innerHTML = '<div class="cost-empty">No team data yet</div>';
        return;
    }

    let html = `<table class="cost-table">
        <thead><tr>
            ${_sortHeader('team', 'board_name', 'Team', '', true)}
            ${_sortHeader('team', 'num_agents', 'Agents', 'cost-col-right')}
            ${_sortHeader('team', 'input_tokens', 'Input', 'cost-col-right')}
            ${_sortHeader('team', 'output_tokens', 'Output', 'cost-col-right')}
            ${_sortHeader('team', 'cache_read_tokens', 'Cache Read', 'cost-col-right')}
            ${_sortHeader('team', 'cache_write_tokens', 'Cache Write', 'cost-col-right')}
            ${_sortHeader('team', 'cost_usd', 'Cost', 'cost-col-right')}
            <th class="cost-col-right" style="width:100px">Share</th>
        </tr></thead><tbody>`;

    const totalCost = teams.reduce((s, t) => s + (t.cost_usd || 0), 0) || 1;

    for (const t of sorted) {
        const name = t.board_name || '(no team)';
        const pct = ((t.cost_usd / totalCost) * 100).toFixed(1);
        const barWidth = Math.max(2, (t.cost_usd / totalCost) * 100);
        html += `<tr>
            <td class="cost-agent-name">${escapeHtml(name)}</td>
            <td class="cost-col-right">${t.num_agents || 0}</td>
            <td class="cost-col-right">${_formatTokens(t.input_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(t.output_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(t.cache_read_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(t.cache_write_tokens || 0)}</td>
            <td class="cost-col-right cost-cost-cell">${_formatCost(t.cost_usd || 0)}</td>
            <td class="cost-col-right">
                <div class="cost-share-bar-wrapper">
                    <div class="cost-share-bar" style="width:${barWidth}%"></div>
                    <span class="cost-share-label">${pct}%</span>
                </div>
            </td>
        </tr>`;
    }

    html += '</tbody></table>';
    container.innerHTML = html;
}

// ── Branch Table ────────────────────────────────────────────

function _renderBranchTable(branches) {
    _rawData.branch = branches;
    const container = document.getElementById('cost-by-branch');
    if (!container) return;

    const filtered = _applyColFilters(branches, 'branch', {
        branch: b => (b.repo_name && b.branch ? `${b.repo_name} : ${b.branch}` : b.branch) || '(unknown)',
        board_name: b => b.board_name || '(no team)',
    });
    const sorted = _sortRows(filtered, 'branch', (b, f) => {
        if (f === 'branch') return (b.repo_name && b.branch ? `${b.repo_name} : ${b.branch}` : b.branch) || '';
        if (f === 'board_name') return b.board_name || '';
        return b[f] || 0;
    });

    if (sorted.length === 0) {
        container.innerHTML = '<div class="cost-empty">No branch data yet</div>';
        return;
    }

    let html = `<table class="cost-table">
        <thead><tr>
            ${_sortHeader('branch', 'branch', 'Branch', '', true)}
            ${_sortHeader('branch', 'board_name', 'Team', '', true)}
            ${_sortHeader('branch', 'pr_number', 'PR', 'cost-col-right')}
            ${_sortHeader('branch', 'num_agents', 'Agents', 'cost-col-right')}
            ${_sortHeader('branch', 'input_tokens', 'Input', 'cost-col-right')}
            ${_sortHeader('branch', 'output_tokens', 'Output', 'cost-col-right')}
            ${_sortHeader('branch', 'cache_read_tokens', 'Cache Read', 'cost-col-right')}
            ${_sortHeader('branch', 'cache_write_tokens', 'Cache Write', 'cost-col-right')}
            ${_sortHeader('branch', 'cost_usd', 'Cost', 'cost-col-right')}
            <th class="cost-col-right" style="width:100px">Share</th>
        </tr></thead><tbody>`;

    const totalCost = branches.reduce((s, b) => s + (b.cost_usd || 0), 0) || 1;

    for (const b of sorted) {
        const name = b.repo_name && b.branch ? `${b.repo_name} : ${b.branch}` : (b.branch || '(unknown)');
        const teamHtml = b.board_name ? escapeHtml(b.board_name) : '—';
        const prCell = _formatPRLink(b.pr_number, b.remote_url);
        const pct = ((b.cost_usd / totalCost) * 100).toFixed(1);
        const barWidth = Math.max(2, (b.cost_usd / totalCost) * 100);
        html += `<tr>
            <td class="cost-agent-name">${escapeHtml(name)}</td>
            <td class="cost-agent-team">${teamHtml}</td>
            <td class="cost-col-right">${prCell}</td>
            <td class="cost-col-right">${b.num_agents || 0}</td>
            <td class="cost-col-right">${_formatTokens(b.input_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(b.output_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(b.cache_read_tokens || 0)}</td>
            <td class="cost-col-right">${_formatTokens(b.cache_write_tokens || 0)}</td>
            <td class="cost-col-right cost-cost-cell">${_formatCost(b.cost_usd || 0)}</td>
            <td class="cost-col-right">
                <div class="cost-share-bar-wrapper">
                    <div class="cost-share-bar" style="width:${barWidth}%"></div>
                    <span class="cost-share-label">${pct}%</span>
                </div>
            </td>
        </tr>`;
    }

    html += '</tbody></table>';
    container.innerHTML = html;
}
