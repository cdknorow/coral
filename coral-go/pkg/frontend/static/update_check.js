/* Update notification — checks for new Coral releases on GitHub */

import { escapeHtml, showToast, escapeAttr } from './utils.js';
import { registerActions, textOf } from './actions.js';
import { browserLocal } from './safe_storage.js';
import { reportUpdate } from './dashboard_analytics.js';

/**
 * Fetch update info from the backend and show a toast if a new version is available.
 * Pass force=true to bypass dismissal (e.g. manual "Check for Updates" click).
 */
export async function checkForUpdates(force = false) {
    // Respect opt-out (only for automatic checks)
    if (!force && browserLocal.get("coral-update-check-enabled") === "false") return;

    try {
        // A manual check asks again (the server answers from a cache and refreshes at most once a minute, #4133).
        const resp = await fetch(force ? "/api/system/update-check?refresh=1" : "/api/system/update-check");
        const data = await resp.json();

        if (!data.available) {
            if (force) showToast("You're on the latest version.");
            return;
        }

        // Skip if user already dismissed this version (automatic check only)
        if (!force) {
            const dismissed = browserLocal.get("coral-update-dismissed");
            if (dismissed === data.latest) return;
        }

        showUpdateToast(data);
    } catch (_e) {
        if (force) showToast("Could not check for updates.");
    }
}

/**
 * Dismiss the update toast and remember the dismissed version.
 */
export function dismissUpdateToast(version) {
    browserLocal.set("coral-update-dismissed", version);
    const toast = document.getElementById("update-toast");
    if (toast) toast.remove();
}

function showUpdateToast(data) {
    // Remove any existing toast
    const existing = document.getElementById("update-toast");
    if (existing) existing.remove();

    // Parse release notes into first 5 bullet points
    let notesHtml = "";
    if (data.release_notes) {
        const lines = data.release_notes
            .split("\n")
            .map(l => l.trim())
            .filter(l => l.startsWith("- ") || l.startsWith("* "));
        const items = lines.slice(0, 5).map(l => {
            const text = l.replace(/^[-*]\s*/, "");
            return `<li>${escapeHtml(text)}</li>`;
        });
        if (items.length) {
            notesHtml = `<ul class="update-toast-notes">${items.join("")}</ul>`;
        }
    }

    const downloadBtn = data.release_url
        ? `<a href="${escapeAttr(data.release_url)}" target="_blank" rel="noopener" class="update-toast-download" data-on-click="update-download" data-version="${escapeAttr(data.latest)}" data-channel="${escapeAttr(data.channel === "agentic" ? "agentic" : "approved")}">Download v${escapeHtml(data.latest)}</a>`
        : "";

    const toast = document.createElement("div");
    toast.id = "update-toast";
    toast.className = "update-toast";
    toast.innerHTML = `
        <div class="update-toast-body">
            <div class="update-toast-header">
                <span class="update-toast-title">
                    Coral v${escapeHtml(data.latest)} available${data.channel === "agentic" ? " (Agentic channel)" : ""}
                    <span class="update-toast-current">(you have v${escapeHtml(data.current)})</span>
                    ${data.channel === "agentic" && data.approved === false ? '<span class="update-toast-current update-toast-unapproved">Not yet approved.</span>' : ""}
                </span>
                <button class="update-toast-close" data-on-click="update-dismiss" data-version="${escapeAttr(data.latest)}" title="Dismiss">&times;</button>
            </div>
            ${notesHtml}
            ${downloadBtn}
        </div>`;

    document.body.appendChild(toast);
    reportUpdate('update_notice_shown', data.latest, data.channel); // the toast is on the page (#5952)
}

/**
 * Show the server's security warning (a data folder other people on this computer can read,
 * #3637) where the update notice appears. It stays until closed and comes back on the next load
 * while the folder is still open to others. Text only, never HTML.
 */
export function showSecurityWarning(message) {
    showWarningToast("security-warning-toast", "Security warning", message);
}

/** Damage the server's integrity check found in a database (#4157): what to do, as text. */
export function showFirewallWarning(message) {
    showWarningToast("firewall-warning-toast", "Windows Firewall", message);
}

export function showDatabaseWarning(message) {
    showWarningToast("database-warning-toast", "Database damage", message);
}

function showWarningToast(id, heading, message) {
    if (!message || document.getElementById(id)) return;
    const toast = document.createElement("div");
    toast.id = id;
    toast.className = "update-toast security-warning-toast";
    toast.setAttribute("role", "alert");
    const body = document.createElement("div");
    body.className = "update-toast-body";
    const header = document.createElement("div");
    header.className = "update-toast-header";
    const title = document.createElement("span");
    title.className = "update-toast-title";
    title.textContent = heading;
    const close = document.createElement("button");
    close.className = "update-toast-close";
    close.type = "button";
    close.title = "Dismiss";
    close.setAttribute("aria-label", `Dismiss: ${heading}`); // the × alone is read as "times"
    close.textContent = "\u00d7";
    close.addEventListener("click", () => toast.remove());
    header.append(title, close);
    const text = document.createElement("p");
    text.className = "security-warning-text";
    text.textContent = message;
    body.append(header, text);
    toast.append(body);
    warningStack().appendChild(toast);
}

// The warnings (security, firewall, database) share one column at the bottom right: each used to sit at that same corner, so two at once were drawn over each other.
function warningStack() {
    let stack = document.getElementById("warning-toasts");
    if (!stack) {
        stack = document.createElement("div");
        stack.id = "warning-toasts";
        document.body.appendChild(stack);
    }
    return stack;
}

// The update toast's close button (#3645).
registerActions({
    'update-dismiss': el => { const v = textOf(el.dataset.version, 100); if (v) window.dismissUpdateToast(v); },
    // The Download link opens the release page by its own href; this only reports that it was opened (#5952).
    'update-download': el => { const v = textOf(el.dataset.version, 100); if (v) reportUpdate('update_offer_opened', v, el.dataset.channel); },
});
