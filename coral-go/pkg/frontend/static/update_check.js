/* Update notification — checks for new Coral releases on GitHub */

import { escapeHtml, showToast, escapeAttr } from './utils.js';
import { registerActions, textOf } from './actions.js';
import { browserLocal } from './safe_storage.js';
import { importOnce } from './import_once.js';
import { reportUpdate } from './dashboard_analytics.js';
import { noticeColumn, announceNotice } from './notifications.js';

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
                <button type="button" class="update-toast-more" data-on-click="notice-more" aria-expanded="false" aria-label="Show details" title="Show details"><span class="material-icons" aria-hidden="true">expand_more</span></button>
                <button class="update-toast-close" data-on-click="update-dismiss" data-version="${escapeAttr(data.latest)}" title="Dismiss">&times;</button>
            </div>
            ${notesHtml}
            ${downloadBtn}
        </div>`;

    warningStack().prepend(toast); // first in the column: on a desktop it is still drawn at the top right; on a phone it heads the column of notices at the top, above the warnings (#6162)
    announceNotice(`Coral v${data.latest} is available`); // said once by the notices' own region: the notice carries no live role of its own (#6213)
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
export async function showFirewallWarning(message) {
    if (!message || document.getElementById("firewall-warning-toast")) return;
    const fw = await importOnce("./firewall_warning.js");
    if (fw.firewallDismissedForGood(message)) return; // "Don't show again": Settings > Security still has it, with the commands (#295)
    const toast = buildWarningToast("firewall-warning-toast", "Windows Firewall", fw.parseFirewallWarning(message).summary, "status");
    const body = toast.querySelector(".update-toast-body");
    body.append(fw.firewallDetails(message));
    const actions = document.createElement("div");
    actions.className = "firewall-actions";
    const never = document.createElement("button");
    never.type = "button";
    never.className = "btn btn-small";
    never.textContent = "Don't show again";
    never.title = "It stays in Settings, with the commands";
    never.addEventListener("click", () => { fw.dismissFirewallForGood(message); toast.remove(); });
    actions.append(never);
    body.append(actions);
    warningStack().appendChild(toast);
}

/** What Coral did to make an open data folder private: told once, as a fix, not as a warning (#295). */
export function showSecurityNotice(message) {
    if (!message || document.getElementById("security-notice-toast")) return;
    showWarningToast("security-notice-toast", "Data folder made private", message, "status");
    // The status keeps the notice until the page that showed it says so: a CLI or another tab reading the status does not use it up.
    fetch("/api/system/security-notice/ack", { method: "POST" }).catch(() => {});
}

export function showDatabaseWarning(message) {
    showWarningToast("database-warning-toast", "Database damage", message);
}

function showWarningToast(id, heading, message, role = "alert") {
    if (!message || document.getElementById(id)) return;
    warningStack().appendChild(buildWarningToast(id, heading, message, role));
}

function buildWarningToast(id, heading, message, role = "alert") {
    const toast = document.createElement("div");
    toast.id = id;
    toast.className = "update-toast security-warning-toast";
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
    const more = document.createElement("button");
    more.type = "button";
    more.className = "update-toast-more";
    more.setAttribute("aria-expanded", "false");
    more.setAttribute("aria-label", "Show details");
    more.title = "Show details";
    more.innerHTML = '<span class="material-icons" aria-hidden="true">expand_more</span>';
    more.addEventListener("click", () => toggleNotice(more));
    header.append(title, more, close);
    const text = document.createElement("p");
    text.className = "security-warning-text";
    text.textContent = message;
    body.append(header, text);
    toast.append(body);
    // The warning and its details, once, from the notices' own region: the buttons inside it are not read with it (#6213). A warning
    // assertively; a notice (a folder made private) and the firewall's rule to tidy politely (#295).
    announceNotice(`${heading}: ${message}`, role === "alert");
    return toast;
}

// The warnings (security, firewall, database) and the update notice share the page's column of notices (notifications.js noticeColumn), so two at once are not drawn over each other.
function warningStack() {
    noticeColumn();
    return document.getElementById("warning-toasts");
}

// On a phone a notice is one line (its title and two buttons) until opened; this opens or folds it, and says so to a screen reader (css hides the details by clipping, so an announced alert still carries them).
function toggleNotice(btn) {
    const toast = btn.closest(".update-toast");
    if (!toast) return;
    const open = toast.classList.toggle("expanded");
    btn.setAttribute("aria-expanded", String(open));
    const label = open ? "Hide details" : "Show details";
    btn.setAttribute("aria-label", label);
    btn.title = label;
}

// The update toast's close button (#3645).
registerActions({
    'notice-more': el => toggleNotice(el),
    'update-dismiss': el => { const v = textOf(el.dataset.version, 100); if (v) window.dismissUpdateToast(v); },
    // The Download link opens the release page by its own href; this only reports that it was opened (#5952).
    'update-download': el => { const v = textOf(el.dataset.version, 100); if (v) reportUpdate('update_offer_opened', v, el.dataset.channel); },
});
