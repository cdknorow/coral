/* Webhook configuration management */

import { showToast, escapeHtml } from './utils.js';

// ── Modal open/close ──────────────────────────────────────────────────────

export function showWebhookModal() {
    document.getElementById("webhook-modal").style.display = "flex";
    loadWebhookList();
}

export function hideWebhookModal() {
    document.getElementById("webhook-modal").style.display = "none";
    _showView("list");
}

// ── Internal view switcher ────────────────────────────────────────────────

function _showView(view) {
    document.getElementById("webhook-list-view").style.display =
        view === "list" ? "flex" : "none";
    document.getElementById("webhook-form-view").style.display =
        view === "form" ? "flex" : "none";
    document.getElementById("webhook-history-view").style.display =
        view === "history" ? "flex" : "none";
}

export function showWebhookList() { _showView("list"); loadWebhookList(); }

// ── List ──────────────────────────────────────────────────────────────────

async function loadWebhookList() {
    try {
        const resp = await fetch("/api/webhooks");
        const webhooks = await resp.json();
        renderWebhookList(webhooks);
    } catch (e) {
        showToast("Failed to load webhooks", true);
    }
}

function renderWebhookList(webhooks) {
    const container = document.getElementById("webhook-list-body");
    if (!webhooks.length) {
        container.innerHTML =
            '<div class="webhook-empty">No webhooks configured. Click "+ Add Webhook".</div>';
        return;
    }
    container.innerHTML = webhooks.map(w => {
        const autoDisabled = !w.enabled && w.consecutive_failures >= 10;
        const statusLabel = autoDisabled
            ? `<span class="webhook-auto-disabled">Auto-disabled (${w.consecutive_failures} failures)</span>`
            : '';
        return `
        <div class="webhook-row">
            <div class="webhook-row-left">
                <span class="webhook-status-dot ${w.enabled ? 'dot-active' : 'dot-inactive'}"></span>
                <div class="webhook-row-info">
                    <span class="webhook-name">${escapeHtml(w.name)}</span>
                    <span class="webhook-meta">
                        ${escapeHtml(w.platform)}
                        ${w.agent_filter ? ` &middot; agent: ${escapeHtml(w.agent_filter)}` : ''}
                    </span>
                    ${statusLabel}
                </div>
            </div>
            <div class="webhook-row-actions">
                <button class="btn btn-small" onclick="testWebhook(${w.id})">Test</button>
                <button class="btn btn-small" onclick="showWebhookHistory(${w.id})">History</button>
                <button class="btn btn-small" onclick="showWebhookEdit(${w.id})">Edit</button>
                <button class="btn btn-small btn-danger"
                    onclick="deleteWebhook(${w.id})">Delete</button>
            </div>
        </div>`;
    }).join('');
}

// ── Create / Edit form ────────────────────────────────────────────────────

export function showWebhookCreate() {
    _showView("form");
    document.getElementById("webhook-form-title").textContent = "Add Webhook";
    document.getElementById("webhook-form").reset();
    document.getElementById("webhook-form-id").value = "";
    document.getElementById("wh-enabled").checked = true;
}

export async function showWebhookEdit(webhookId) {
    try {
        const resp = await fetch("/api/webhooks");
        const all = await resp.json();
        const w = all.find(x => x.id === webhookId);
        if (!w) return;
        _showView("form");
        document.getElementById("webhook-form-title").textContent = "Edit Webhook";
        document.getElementById("webhook-form-id").value = w.id;
        document.getElementById("wh-name").value = w.name;
        document.getElementById("wh-platform").value = w.platform;
        document.getElementById("wh-url").value = w.url;
        document.getElementById("wh-agent-filter").value = w.agent_filter || "";
        document.getElementById("wh-enabled").checked = !!w.enabled;
    } catch (e) {
        showToast("Failed to load webhook", true);
    }
}

export async function saveWebhook() {
    const id = document.getElementById("webhook-form-id").value;
    const payload = {
        name:    document.getElementById("wh-name").value.trim(),
        platform: document.getElementById("wh-platform").value,
        url:     document.getElementById("wh-url").value.trim(),
        agent_filter:
            document.getElementById("wh-agent-filter").value.trim() || null,
        enabled: document.getElementById("wh-enabled").checked ? 1 : 0,
    };
    if (!payload.name || !payload.url) {
        showToast("Name and URL are required", true);
        return;
    }
    try {
        if (id) {
            const resp = await fetch(`/api/webhooks/${id}`, {
                method: "PATCH",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify(payload),
            });
            const result = await resp.json();
            if (result.error) { showToast(result.error, true); return; }
            showToast("Webhook updated");
        } else {
            const resp = await fetch("/api/webhooks", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify(payload),
            });
            const result = await resp.json();
            if (result.error) { showToast(result.error, true); return; }
            showToast("Webhook created");
        }
        showWebhookList();
    } catch (e) {
        showToast("Failed to save webhook", true);
    }
}

// ── Delete ────────────────────────────────────────────────────────────────

export function deleteWebhook(webhookId) {
    window.showConfirmModal('Delete Webhook', 'Delete this webhook and all its history?', async () => {
        try {
            await fetch(`/api/webhooks/${webhookId}`, { method: "DELETE" });
            showToast("Webhook deleted");
            loadWebhookList();
        } catch (e) {
            showToast("Failed to delete webhook", true);
        }
    });
}

// ── Test ──────────────────────────────────────────────────────────────────

export async function testWebhook(webhookId) {
    showToast("Sending test notification...");
    try {
        const resp = await fetch(`/api/webhooks/${webhookId}/test`, {
            method: "POST",
        });
        const result = await resp.json();
        if (result.status === "delivered") {
            showToast("Test delivered successfully");
        } else if (result.error) {
            showToast(`Test failed: ${result.error}`, true);
        } else {
            showToast(`Queued (status: ${result.status || "pending"})`);
        }
    } catch (e) {
        showToast("Test failed", true);
    }
}

// ── Delivery history ──────────────────────────────────────────────────────

export async function showWebhookHistory(webhookId) {
    _showView("history");
    try {
        const resp = await fetch(
            `/api/webhooks/${webhookId}/deliveries?limit=50`
        );
        const deliveries = await resp.json();
        renderWebhookHistory(deliveries);
    } catch (e) {
        showToast("Failed to load history", true);
    }
}

function renderWebhookHistory(deliveries) {
    const container = document.getElementById("webhook-history-body");
    if (!deliveries.length) {
        container.innerHTML =
            '<div class="webhook-empty">No deliveries yet.</div>';
        return;
    }
    container.innerHTML = deliveries.map(d => {
        const statusCls =
            d.status === "delivered" ? "delivery-ok" :
            d.status === "failed"    ? "delivery-fail" : "delivery-pending";
        const ts = d.created_at
            ? new Date(d.created_at).toLocaleString() : "\u2014";
        const http = d.http_status ? ` (HTTP ${d.http_status})` : "";
        return `
            <div class="delivery-row ${statusCls}">
                <span class="delivery-status">${escapeHtml(d.status)}</span>
                <span class="delivery-agent">${escapeHtml(d.agent_name)}</span>
                <span class="delivery-event">${escapeHtml(d.event_type)}</span>
                <span class="delivery-summary" title="${escapeHtml(d.event_summary)}">
                    ${escapeHtml(d.event_summary)}
                </span>
                <span class="delivery-ts">${ts}</span>
                ${d.error_msg
                    ? `<span class="delivery-error">${escapeHtml(d.error_msg)}${http}</span>`
                    : ''}
            </div>
        `;
    }).join('');
}
