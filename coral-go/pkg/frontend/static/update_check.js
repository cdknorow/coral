/* Update notification — checks for new Coral releases on GitHub */

import { escapeHtml } from './utils.js';

/**
 * Fetch update info from the backend and show a toast if a new version is available.
 * Pass force=true to bypass dismissal (e.g. manual "Check for Updates" click).
 */
export async function checkForUpdates(force = false) {
    // Respect opt-out (only for automatic checks)
    if (!force && localStorage.getItem("coral-update-check-enabled") === "false") return;

    try {
        const resp = await fetch("/api/system/update-check");
        const data = await resp.json();

        if (!data.available) {
            if (force) showToast("You're on the latest version.");
            return;
        }

        // Skip if user already dismissed this version (automatic check only)
        if (!force) {
            const dismissed = localStorage.getItem("coral-update-dismissed");
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
    localStorage.setItem("coral-update-dismissed", version);
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
        ? `<a href="${escapeHtml(data.release_url)}" target="_blank" rel="noopener" class="update-toast-download">Download v${escapeHtml(data.latest)}</a>`
        : "";

    const toast = document.createElement("div");
    toast.id = "update-toast";
    toast.className = "update-toast";
    toast.innerHTML = `
        <div class="update-toast-body">
            <div class="update-toast-header">
                <span class="update-toast-title">
                    Coral v${escapeHtml(data.latest)} available
                    <span class="update-toast-current">(you have v${escapeHtml(data.current)})</span>
                </span>
                <button class="update-toast-close" onclick="dismissUpdateToast('${escapeHtml(data.latest)}')" title="Dismiss">&times;</button>
            </div>
            ${notesHtml}
            ${downloadBtn}
        </div>`;

    document.body.appendChild(toast);
}
