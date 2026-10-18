/* Shared pieces of an Activity row: how long the activity took and what else is known about it */

import { escapeHtml, escapeAttr } from './utils.js';
import { formatDuration as fmtDuration } from './format.js';
import { browserLocal } from './safe_storage.js';
import { formatClockTime } from './format.js';
import { getToolIcon } from './event_icons.js';

/** 850 -> "850ms", 9100 -> "9.1s", 75000 -> "1m 15s", 3720000 -> "1h 2m". */
export function formatDuration(ms) {
    return fmtDuration(ms, { shape: 'precise' });
}

function parseDetail(ev) {
    if (!ev || !ev.detail_json) return null;
    if (typeof ev.detail_json === 'object') return ev.detail_json;
    try { return JSON.parse(ev.detail_json); } catch { return null; }
}

export function eventFailed(ev) {
    const d = parseDetail(ev);
    return !!(d && d.failed);
}

/** Extra facts for the row tooltip: duration, HTTP status and size, failure reason. */
export function eventDetailText(ev) {
    const parts = [];
    const dur = formatDuration(ev.duration_ms);
    if (dur) parts.push(`took ${dur}`);
    const d = parseDetail(ev) || {};
    const r = d.response || {};
    if (r.code != null) parts.push(`HTTP ${r.code}${r.codeText ? ' ' + r.codeText : ''}`);
    if (Number.isFinite(r.bytes)) parts.push(r.bytes >= 1024 ? `${Math.round(r.bytes / 1024)} KB` : `${r.bytes} B`);
    if (r.interrupted || d.interrupted) parts.push('interrupted');
    if (d.error) parts.push(`error: ${d.error}`);
    return parts.join(' · ');
}

/** Right-aligned duration chip; empty when the source reported no timing. */
export function durationChip(ev) {
    const dur = formatDuration(ev.duration_ms);
    if (!dur) return '';
    const slow = Number(ev.duration_ms) >= 10000 ? ' slow' : '';
    return `<span class="event-duration${slow}">${escapeHtml(dur)}</span>`;
}

const CHART_MODE_KEY = 'coral-activity-chart-mode';

function getChartMode() {
    // Time is the default; Count only when it was picked explicitly.
    try { return browserLocal.get(CHART_MODE_KEY) === 'count' ? 'count' : 'time'; } catch { return 'time'; }
}

/** Activity histogram per filter group: how many events, or how much time they took in total.
 *  The Count / Time switch is remembered across sessions and shared by the live and history tabs. */
export function renderActivityChart(container, events, groups) {
    if (!container) return;
    if (!events || events.length === 0) {
        container.innerHTML = '';
        container._chartHtml = '';
        return;
    }

    const rows = [];
    const matched = new Set();
    const tally = (key, label, cls, match) => {
        let count = 0, totalMs = 0;
        events.forEach((ev, idx) => {
            if (!match(ev, idx)) return;
            count++;
            matched.add(idx);
            const ms = Number(ev.duration_ms);
            if (Number.isFinite(ms) && ms > 0) totalMs += ms;
        });
        if (count > 0) rows.push({ key, label, cls, count, totalMs });
    };
    for (const group of groups) tally(group.key, group.title, group.cls, ev => group.match(ev));
    const seen = new Set(matched);
    tally('other', 'Other', 'tool-default', (ev, idx) => !seen.has(idx));

    const mode = getChartMode();
    const value = mode === 'time' ? (r => r.totalMs) : (r => r.count);
    const shown = rows.filter(r => value(r) > 0).sort((a, b) => value(b) - value(a));
    const max = shown.length ? value(shown[0]) : 0;

    const toggle = `<div class="activity-chart-mode" role="group" aria-label="Chart shows">
        <button type="button" data-chart-mode="time" class="${mode === 'time' ? 'active' : ''}" title="Total time spent per type">Time</button>
        <button type="button" data-chart-mode="count" class="${mode === 'count' ? 'active' : ''}" title="Number of events per type">Count</button>
    </div>`;

    const bars = shown.map(r => {
        const pct = Math.max(2, (value(r) / max) * 100);
        const total = formatDuration(r.totalMs);
        const tip = `${r.label}: ${r.count} event${r.count === 1 ? '' : 's'}${total ? ', ' + total + ' total' : ''}`;
        return `<div class="activity-chart-row" title="${escapeHtml(tip)}">
            <span class="activity-chart-label">${escapeHtml(r.label)}</span>
            <div class="activity-chart-bar-track">
                <div class="activity-chart-bar ${r.cls}" style="width: ${pct}%"></div>
            </div>
            <span class="activity-chart-count${mode === 'time' ? ' is-time' : ''}">${mode === 'time' ? escapeHtml(total) : r.count}</span>
        </div>`;
    }).join('');

    // The live tab re-renders on every poll; rewriting identical markup would
    // replace the switch mid-click and swallow the click.
    const html = toggle + (bars || '<div class="activity-chart-empty">No timing recorded yet</div>');
    if (container._chartHtml !== html) {
        container.innerHTML = html;
        container._chartHtml = html;
    }
    container.onclick = (e) => {
        const btn = e.target.closest('[data-chart-mode]');
        if (!btn) return;
        try { browserLocal.set(CHART_MODE_KEY, btn.dataset.chartMode); } catch {}
        renderActivityChart(container, events, groups);
    };
}

/** One row of the Activity list: the icon, the summary, how long it took and when (the live feed and a search draw the same). */
export function eventRowHtml(ev) {
    const icon = getToolIcon(ev.tool_name, ev.event_type);
    const typeCls = (ev.event_type ? `event-type-${ev.event_type}` : '') + (eventFailed(ev) ? ' event-failed' : '');
    const extra = eventDetailText(ev);
    return `<div class="event-item ${typeCls}" data-tooltip="${escapeAttr(icon.title === ev.summary ? '' : escapeAttr(icon.title) + ': ')}${escapeAttr(ev.summary)}${escapeAttr(extra ? escapeAttr(' (' + extra + ')') : '')}">
            <span class="event-icon ${icon.cls}">${icon.char}</span>
            <span class="event-body">
                <span class="event-summary">${escapeHtml(ev.summary)}</span>
            </span>
            ${durationChip(ev)}
            <span class="event-time">${formatClockTime(ev.created_at)}</span>
        </div>`;
}
