/* The task detail dialog's markup (moved out of tasks.js, #4996; no behaviour change). */
import { escapeHtml, escapeAttr } from './utils.js';
import { _priorityLabel, formatTaskDate, _formatCost, _formatTokenCount } from './task_format.js';
import { serverUrl, artifactPath, contextServer, keyLabel, boardKeyName, splitKey, identityKey } from './server_base.js';
import { _claimedFor } from './subagents.js';
import { _verifyLine } from './task_verify_marks.js';
import { _getBoardProject } from './task_board_context.js';

/** The top of the task detail: its title, lifecycle, priority, result and verification line. */
function _taskDetailHeadHtml(task) {
    const statusLabel = task.workflow?.outcome === 'failed' ? 'Finished' : task.status === 'completed' ? 'Finished'
        : task.status === 'review_pending' ? 'Open · Review pending (slot released)'
        : task.workflow?.completion_review && task.status === 'in_progress' ? 'Open · Completion review requested (slot occupied)'
        : task.status === 'in_progress' ? 'Open · In Progress'
        : task.status === 'skipped' ? 'Cancelled'
        : task.status === 'blocked' ? 'Blocked'
        : task.status === 'draft' ? 'Open · Draft'
        : 'Open';
    const statusClass = task.status === 'completed' ? 'task-detail-status-completed'
        : task.status === 'in_progress' ? 'task-detail-status-inprogress'
        : task.status === 'skipped' ? 'task-detail-status-cancelled'
        : task.status === 'blocked' ? 'task-detail-status-blocked'
        : task.status === 'draft' ? 'task-detail-status-draft'
        : 'task-detail-status-pending';
    const priorityClass = 'board-task-priority-' + (task.priority || 'medium');

    let html = `
        <div class="task-detail-title" id="task-detail-heading">#${Number(task.id)} · ${escapeHtml(task.title)}</div>
        <div class="task-detail-meta">
            <span class="task-detail-meta-item"><span class="task-detail-meta-label">Lifecycle</span><span class="task-detail-status ${statusClass}">${statusLabel}</span></span>
            <span class="task-detail-meta-item"><span class="task-detail-meta-label">Priority</span><span class="board-task-priority ${priorityClass}" title="Claim priority: ${escapeAttr(task.priority || 'none')}">${task.priority ? (_priorityLabel[task.priority] || escapeHtml(task.priority)) : '\u2014'}</span></span>
            ${task.workflow?.outcome === 'failed' ? '<span class="task-detail-meta-item"><span class="task-detail-meta-label">Result</span><span class="task-detail-outcome-failed">Failed</span></span>' : ''}
        </div>${_verifyLine(task)}`;
    return html;
}

/** The instruction revision and what changed since the task was created; nothing for a task never amended. */
function _taskRevisionHtml(task) {
    let html = '';
    const revision = Number(task.revision || 1);
    const amendments = task.workflow?.amendments || [];
    const preview = value => {
        const text = String(value || '').replace(/\s+/g, ' ').trim();
        return text.length > 180 ? `${text.slice(0, 177)}…` : text || '(empty)';
    };
    // Revisions only matter once the instructions have changed; a new task has nothing to show.
    if (revision > 1 || amendments.length) html += `<div class="task-detail-section task-detail-revision" data-task-revision="${Number(revision)}">
        <div class="task-detail-label">Instruction revision</div>
        <div class="task-detail-body">Current revision <strong>${revision}</strong>${amendments.length ? ` · ${amendments.length} amendment${amendments.length === 1 ? '' : 's'}` : ''}</div>
        ${amendments.length ? `<details class="task-amendment-history"><summary>Changed since task creation</summary>${amendments.map(amendment => {
            const previous = amendment.previous_snapshot || {};
            const effective = amendment.effective_snapshot || {};
            const changes = amendment.changes || {};
            const fields = Object.keys(changes).map(field => `<div><strong>${escapeHtml(field === 'workflow_instructions' ? 'Instructions' : 'Description')}</strong>: ${escapeHtml(preview(previous[field]))} → ${escapeHtml(preview(effective[field] ?? changes[field]))}</div>`).join('');
            return `<div class="task-amendment-entry"><strong>Revision ${Number(amendment.revision)}</strong> · ${escapeHtml(amendment.actor || 'Planner')} · ${escapeHtml(amendment.created_at || '')}<br><span>${escapeHtml(amendment.reason || '')}</span>${fields}</div>`;
        }).join('')}</details>` : ''}
    </div>`;
    return html;
}

/** The task's workflow: its gates, inputs, completion candidate and artifacts. */
function _taskWorkflowHtml(task) {
    let html = '';
    const workflow = task.workflow;
    if (workflow) {
        const artifactHtml = a => {
            const uri = String(a.uri || '');
            const coralMatch = /^coral:\/\/artifacts\/([a-f0-9]{64})$/i.exec(uri);
            const href = coralMatch ? serverUrl(artifactPath(coralMatch[1]), contextServer()) : uri;
            const uriDisplay = (/^https?:\/\//i.test(uri) || coralMatch)
                ? `<a href="${escapeAttr(coralMatch ? '#' : escapeAttr(href))}" data-artifact-uri="${escapeAttr(uri)}"${coralMatch ? ' data-on-click="task-artifact-open"' : ' target="_blank" rel="noopener noreferrer"'}>${escapeHtml(uri)}</a>`
                : escapeHtml(uri);
            return `<div class="task-detail-body"><strong>${escapeHtml(a.name)}</strong>${a.revision ? ` · ${escapeHtml(a.revision)}` : ''}${a.digest ? ` · ${escapeHtml(a.digest)}` : ''}${uri ? `<div>${uriDisplay}</div>` : ''}${a.content ? `<pre style="white-space:pre-wrap;overflow-wrap:anywhere">${escapeHtml(a.content)}</pre>` : ''}</div>`;
        };
        html += `<details class="task-detail-section"><summary>Workflow${workflow.name ? `: ${escapeHtml(workflow.name)}` : ''}${workflow.stage ? ` · ${escapeHtml(workflow.stage)}` : ''}</summary>
            <div class="task-detail-body">${escapeHtml(workflow.instructions || '')}</div>
            <div>Required outputs: ${escapeHtml((workflow.required_outputs || []).join(', ') || 'None')}</div>
            <div>Completion gates: ${(workflow.completion_gates || []).length ? `<em>experimental, disabled by default (inactive unless the server enables them)</em> — ` : ''}${escapeHtml((workflow.completion_gates || []).map(g => `${g.name || g.type}${g.check_id ? ` [${g.check_id}]` : ''} (${g.artifact || 'server check'})`).join(', ') || 'None')}</div>
            ${workflow.candidate_revision ? `<div>Candidate revision: <code>${escapeHtml(workflow.candidate_revision)}</code></div>` : ''}
            ${(workflow.gate_results || []).length ? `<div>Gate results: ${(workflow.gate_results || []).map(g => `<span class="task-gate-result ${g.passed ? 'passed' : 'failed'}">${escapeHtml(g.name || g.type)}: ${escapeHtml(g.details || (g.passed ? 'passed' : 'failed'))}</span>`).join(' · ')}</div>` : ''}
            ${workflow.parent_task_id ? `<div>Parent task #${Number(workflow.parent_task_id)}</div>` : ''}
            ${workflow.retry_of ? `<div>Retry of task #${Number(workflow.retry_of)}</div>` : ''}</details>`;
        for (const input of workflow.inputs || []) html += `<details class="task-detail-section"><summary>Input from ${escapeHtml(input.board_id)} #${Number(input.task_id)} (${escapeHtml(input.outcome)})</summary>${(input.artifacts || []).map(artifactHtml).join('')}</details>`;
        if (workflow.completion_review) {
            const review = workflow.completion_review;
            html += `<div class="task-detail-section"><div class="task-detail-label">Completion candidate · not an accepted result</div>
                <div class="task-detail-body">Submitted by ${escapeHtml(review.submitted_by)} · ${escapeHtml(review.submitted_at)}<br>
                Proposed outcome: ${escapeHtml(review.proposed_outcome)}<br>${escapeHtml(review.reason)}<br>${escapeHtml(review.message || '')}</div>
                ${(review.artifacts || []).map(artifactHtml).join('')}
                ${review.released_by ? `<div>Slot released by ${escapeHtml(review.released_by)} · ${escapeHtml(review.released_at)}: ${escapeHtml(review.release_reason)}</div>` : '<div>The worker slot is still occupied. An orchestrator can release it with coral-board task release-review.</div>'}</div>`;
        }
        if (workflow.artifacts?.length) html += `<div class="task-detail-section"><div class="task-detail-label">Completion artifacts</div>${workflow.artifacts.map(artifactHtml).join('')}</div>`;
    }
    return html;
}

/** Who the task is assigned to, who made and finished it, and when. */
function _taskFieldsHtml(task) {
    let html = '';
    const assignee = task.assigned_to || '\u2014';
    const createdBy = task.created_by || '\u2014';
    const createdAt = task.created_at ? formatTaskDate(task.created_at) : '\u2014';
    const claimedAt = task.claimed_at ? formatTaskDate(task.claimed_at) : null;
    const completedAt = task.completed_at ? formatTaskDate(task.completed_at) : null;
    const completedBy = task.completed_by || null;

    html += `<div class="task-detail-fields">
        <div class="task-detail-field">
            <span class="task-detail-label">Assigned To</span>
            <span class="task-detail-value">${escapeHtml(assignee)}</span>
        </div>
        <div class="task-detail-field">
            <span class="task-detail-label">Created By</span>
            <span class="task-detail-value">${escapeHtml(createdBy)}</span>
        </div>
        <div class="task-detail-field">
            <span class="task-detail-label">Created</span>
            <span class="task-detail-value">${createdAt}</span>
        </div>`;

    if (claimedAt) {
        const claimedFor = _claimedFor(task);
        html += `<div class="task-detail-field">
            <span class="task-detail-label">Claimed</span>
            <span class="task-detail-value">${claimedAt}${claimedFor ? ` <span class="task-detail-claimed-for">(${claimedFor} ago)</span>` : ''}</span>
        </div>`;
    }
    if (completedAt) {
        html += `<div class="task-detail-field">
            <span class="task-detail-label">${task.status === 'skipped' ? 'Cancelled' : 'Completed'}</span>
            <span class="task-detail-value">${completedAt}</span>
        </div>`;
    }
    if (completedBy) {
        html += `<div class="task-detail-field">
            <span class="task-detail-label">${task.status === 'skipped' ? 'Cancelled By' : 'Completed By'}</span>
            <span class="task-detail-value">${escapeHtml(completedBy)}</span>
        </div>`;
    }
    if (task.completion_message) {
        html += `<div class="task-detail-field task-detail-field-wide">
            <span class="task-detail-label">Message</span>
            <span class="task-detail-value">${escapeHtml(task.completion_message)}</span>
        </div>`;
    }
    if (task.workflow?.outcome === 'failed' && !task.completion_message) {
        html += `<div class="task-detail-field task-detail-field-wide task-detail-failure-reason">
            <span class="task-detail-label">Result details</span>
            <span class="task-detail-value">No failure reason was provided.</span>
        </div>`;
    }

    html += `</div>`;
    return html;
}

/** The tasks this one waits for, each with its status. */
function _taskBlockedByHtml(task) {
    let html = '';
    // Blocked by dependencies
    if (task.blocked_by && task.blocked_by.length > 0) {
        const boardProject = _getBoardProject();
        const depsHtml = task.blocked_by.map(dep => {
            const depStatusClass = dep.satisfied === false ? 'task-dep-status-blocked'
                : dep.status === 'completed' ? 'task-dep-status-completed'
                : dep.status === 'in_progress' ? 'task-dep-status-inprogress'
                : dep.status === 'skipped' ? 'task-dep-status-cancelled'
                : dep.status === 'blocked' ? 'task-dep-status-blocked'
                : dep.status === 'draft' ? 'task-dep-status-draft'
                : 'task-dep-status-pending';
            const depStatusLabel = dep.satisfied === false ? 'unmet'
                : dep.status === 'completed' ? 'completed'
                : dep.status === 'in_progress' ? 'in progress'
                : dep.status === 'skipped' ? 'cancelled'
                : dep.status === 'blocked' ? 'blocked'
                : dep.status === 'draft' ? 'draft'
                : 'pending';
            const isCrossBoard = task._source !== 'agent' && dep.board_id && dep.board_id !== boardKeyName(boardProject); // keys, not labels
            const boardPrefix = isCrossBoard ? `${escapeHtml(dep.board_label || keyLabel(identityKey(splitKey(boardProject).server, dep.board_id)))} ` : '';
            const depTitle = dep.title ? ` — ${escapeHtml(dep.title)}` : '';
            const clickable = !isCrossBoard ? ` data-on-click="task-open" data-kind="${escapeAttr(task._source === 'agent' ? 'agent' : 'board')}" data-task-id="${Number(dep.task_id)}" style="cursor:pointer"` : '';
            return `<div class="task-dep-item">
                <span class="task-dep-status ${depStatusClass}">${depStatusLabel}</span>
                <a class="task-dep-link"${clickable}>${boardPrefix}#${dep.task_id}${depTitle}</a>
                <span>${escapeHtml(dep.condition || 'success')}${dep.required_artifacts?.length ? ` · requires ${escapeHtml(dep.required_artifacts.join(', '))}` : ''}${dep.blocked_reason ? ` · ${escapeHtml(dep.blocked_reason)}` : ''}</span>
            </div>`;
        }).join('');
        html += `<div class="task-detail-section">
            <div class="task-detail-label">Blocked By</div>
            <div class="task-deps-list">${depsHtml}</div>
        </div>`;
    }
    return html;
}

/** The cost and token breakdown of a task that has cost data. */
function _taskCostHtml(task) {
    let html = '';
    // Cost & token breakdown for completed tasks with cost data
    if (task.cost_usd != null) {
        const warningClass = task.cost_usd >= 1.0 ? ' board-task-cost-warning' : '';
        html += `<div class="task-detail-section">
            <div class="task-detail-label">Cost</div>
            <div class="task-detail-cost-summary${warningClass}">${_formatCost(task.cost_usd, true)}</div>
            <div class="task-detail-tokens">
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Input</span>
                    <span class="task-detail-token-value">${_formatTokenCount(task.input_tokens)}</span>
                </div>
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Output</span>
                    <span class="task-detail-token-value">${_formatTokenCount(task.output_tokens)}</span>
                </div>
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Cache Read</span>
                    <span class="task-detail-token-value">${_formatTokenCount(task.cache_read_tokens)}</span>
                </div>
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Cache Write</span>
                    <span class="task-detail-token-value">${_formatTokenCount(task.cache_write_tokens)}</span>
                </div>
            </div>
        </div>`;
    }
    return html;
}

/** The running cost of a task in progress. */
function _taskLiveCostHtml(liveCost) {
    let html = '';
    // Live cost for in-progress tasks
    if (liveCost) {
        const lc = liveCost;
        const warningClass = lc.cost_usd >= 1.0 ? ' board-task-cost-warning' : '';
        html += `<div class="task-detail-section">
            <div class="task-detail-label">Running Cost</div>
            <div class="task-detail-cost-summary board-task-cost-live${warningClass}">~${_formatCost(lc.cost_usd, true)}</div>
            <div class="task-detail-tokens">
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Input</span>
                    <span class="task-detail-token-value">${_formatTokenCount(lc.input_tokens)}</span>
                </div>
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Output</span>
                    <span class="task-detail-token-value">${_formatTokenCount(lc.output_tokens)}</span>
                </div>
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Cache Read</span>
                    <span class="task-detail-token-value">${_formatTokenCount(lc.cache_read_tokens)}</span>
                </div>
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Cache Write</span>
                    <span class="task-detail-token-value">${_formatTokenCount(lc.cache_write_tokens)}</span>
                </div>
            </div>
            <div class="task-detail-token-item" style="margin-top:4px;opacity:0.6">
                <span class="task-detail-token-label">Requests</span>
                <span class="task-detail-token-value">${lc.request_count}</span>
            </div>
        </div>`;
    }
    return html;
}

export function _taskDetailHtml(task, liveCost) {
    let html = _taskDetailHeadHtml(task);
    html += _taskRevisionHtml(task);

    if (task.body) {
        html += `<div class="task-detail-section">
            <div class="task-detail-label">Description</div>
            <div class="task-detail-body">${escapeHtml(task.body)}</div>
        </div>`;
    }

    html += _taskWorkflowHtml(task);
    html += _taskFieldsHtml(task);
    html += _taskBlockedByHtml(task);
    html += _taskCostHtml(task);
    html += _taskLiveCostHtml(liveCost);
    return html;
}
