/* Toasts and notifications (moved out of utils.js, #5065; no behaviour change). */
import { importOnce } from './import_once.js';
import { escapeHtml, escapeAttr } from './html_escape.js';
import { routeHref } from './markdown.js';

/**
 * Show a short message at the bottom right. Errors are announced at once
 * (role=alert) and informational toasts politely (role=status); every toast can
 * be dismissed (close button, or Escape while focused).
 *
 * opts.blocked: prefix the text with "Blocked: " (once) so a refusal is clear
 *   without relying on colour.
 * opts.action: {label, onClick} adds a button; clicking it dismisses the toast
 *   and runs onClick. A toast with an action stays at least 10s and does not
 *   fade, so the button stays usable.
 *
 * Returns the toast element. Text is set with textContent only.
 */
export function showToast(message, isError = false, duration = 4000, opts = {}) {
    const error = !!isError;
    const toast = document.createElement("div");
    toast.className = `toast ${error ? "error" : ""}`;
    toast.setAttribute("role", error ? "alert" : "status");
    toast.setAttribute("aria-live", error ? "assertive" : "polite");
    toast.setAttribute("aria-atomic", "true");

    let text = String(message ?? "");
    if (opts.blocked && !/^Blocked:/.test(text)) text = `Blocked: ${text}`;
    const body = document.createElement("span");
    body.className = "toast-message";
    body.textContent = text;
    toast.appendChild(body);

    let timer = 0;
    const dismiss = () => {
        clearTimeout(timer);
        toast.remove();
    };
    const action = opts.action;
    if (action && action.label && typeof action.onClick === "function") {
        const button = document.createElement("button");
        button.type = "button";
        button.className = "toast-action";
        button.textContent = action.label;
        button.addEventListener("click", () => {
            dismiss();
            try { action.onClick(); } catch (e) { console.error("toast action failed:", e); }
        });
        toast.appendChild(button);
        toast.classList.add("has-action");
        duration = Math.max(duration, 10000);
    }
    const close = document.createElement("button");
    close.type = "button";
    close.className = "toast-close";
    close.setAttribute("aria-label", "Dismiss");
    close.textContent = "×";
    close.addEventListener("click", dismiss);
    toast.appendChild(close);
    toast.addEventListener("keydown", (e) => { if (e.key === "Escape") dismiss(); });
    // On a phone a long toast shows two lines (css); a tap opens the rest. The full text is in the element either way, so a screen reader reads it all.
    toast.addEventListener("click", (e) => { if (!e.target.closest("button")) toast.classList.toggle("expanded"); });

    toastHost().appendChild(toast);
    timer = setTimeout(dismiss, duration);
    return toast;
}

// The short toasts share one column, so two at once do not sit on each other (the second hid the first, #6162): bottom right on a desktop, the top on a phone (css).
function toastHost() {
    let host = document.getElementById("toast-host");
    if (!host) {
        host = document.createElement("div");
        host.id = "toast-host";
        document.body.appendChild(host);
    }
    placeNotices();
    return host;
}

// On a phone the notices and the short toasts start below the top bar (the Agents picker, home, bookmarks, panel, settings), never over it: css reads --notice-top, set here from the bar's own bottom edge on the two
// columns themselves (not on <html>, where the theme code treats every inline custom property as a theme colour) and kept current when the window changes.
export function placeNotices() {
    const bar = document.querySelector("header.top-bar");
    const top = `${Math.max(bar ? Math.round(bar.getBoundingClientRect().bottom) : 0, 0) + 8}px`;
    for (const id of ["toast-host", "warning-toasts"]) document.getElementById(id)?.style.setProperty("--notice-top", top);
    if (!placeNotices._wired) { placeNotices._wired = true; window.addEventListener("resize", placeNotices); }
}

export function showWorkflowNotification(title, message, level = 'info', duration = 8000) {
    const icons = { success: '✅', warning: '⚠️', error: '❌', info: '💬' };
    const icon = icons[level] || icons.info;
    const toast = document.createElement("div");
    toast.className = "notification-toast";
    toast.innerHTML = `<div class="notification-toast-body">
            <div class="notification-toast-header">
                <span class="notification-toast-title">${icon} ${title ? `<strong>${escapeHtml(title)}</strong>` : 'Notification'}</span>
                <button class="notification-toast-close">✕</button>
            </div>
            ${message ? `<span class="notification-toast-detail">${escapeHtml(message)}</span>` : ''}
        </div>`;
    toast.querySelector(".notification-toast-close").addEventListener("click", () => toast.remove());
    document.body.appendChild(toast);
    setTimeout(() => toast.remove(), duration);
}

/** Where a notification's link may go (#3622 F2): an agent writes it (coral-agent notify), so it
 *  is one of docs://<name> (the Docs tab), an http(s) address (a new tab, no opener), or a
 *  dashboard route the router accepts (rebuilt from the parse). Anything else, javascript:,
 *  data:, file:, //host, /api/..., is refused (null) and the label is shown as text. The server
 *  refuses the same links (routes/notifications.go). */
export function notificationLinkTarget(url) {
    const u = String(url || '').trim();
    if (!u || u.length > 2048 || /[\u0000-\u001f\u007f\\]/.test(u)) return null;
    const doc = /^docs:\/\/([A-Za-z0-9][A-Za-z0-9_-]{0,127})$/.exec(u);
    if (doc) return { kind: 'docs', name: doc[1] };
    if (/^https?:\/\//i.test(u)) {
        let parsed;
        try { parsed = new URL(u); } catch { return null; }
        if (!/^https?:$/.test(parsed.protocol) || !parsed.hostname || parsed.username || parsed.password) return null;
        return { kind: 'web', href: parsed.href, host: parsed.host, insecure: parsed.protocol === 'http:' };
    }
    const route = routeHref(u);
    return route !== null ? { kind: 'route', href: route } : null;
}

let _alertHostSeq = 0;

export function showAlertNotification(title, message, level = 'info', link = null) {
    const icons = { success: '✅', warning: '⚠️', error: '❌', info: '💬' };
    const icon = icons[level] || icons.info;
    const overlay = document.createElement("div");
    overlay.className = "alert-notification-overlay";
    const target = link ? notificationLinkTarget(link.url) : null;
    const label = link ? String(link.label || link.url || '') : '';
    const linkHtml = !link ? ''
        // A web link shows where it goes next to its label (the label is the agent's own text), and
        // says so when it isn't encrypted (#3623, Security's NIT on #3622).
        // A screen reader hears the host too (aria-describedby). The host is URL's own form:
        // punycode (xn--...) for a look-alike name, with the port when there is one.
        : target && target.kind === 'web' ? (() => {
            const hostId = `alert-notification-host-${++_alertHostSeq}`;
            return `<a href="#" class="alert-notification-link" aria-describedby="${escapeAttr(hostId)}">${escapeHtml(label)}</a>`
                + ` <span class="alert-notification-host" id="${escapeAttr(hostId)}">${escapeHtml(target.host)}${target.insecure ? ' · not encrypted' : ''}</span>`;
        })()
        : target ? `<a href="#" class="alert-notification-link">${escapeHtml(label)}</a>`
        : `<span class="alert-notification-link blocked-link" title="Link not opened">${escapeHtml(label)}</span>`;
    overlay.innerHTML = `<div class="alert-notification">
            <div class="alert-notification-icon">${icon}</div>
            <div class="alert-notification-title">${title ? escapeHtml(title) : 'Notification'}</div>
            ${message ? `<div class="alert-notification-message">${escapeHtml(message)}</div>` : ''}
            ${linkHtml}
            <button class="alert-notification-ok btn btn-primary">OK</button>
        </div>`;
    overlay.querySelector(".alert-notification-ok").addEventListener("click", () => overlay.remove());
    overlay.addEventListener("click", (e) => { if (e.target === overlay) overlay.remove(); });
    if (target) {
        overlay.querySelector("a.alert-notification-link").addEventListener("click", (e) => {
            e.preventDefault();
            overlay.remove();
            if (target.kind === 'docs') {
                // docs:// links navigate to the Docs tab (e.g. "docs://workflow-quickstart")
                if (window.switchNavTab) window.switchNavTab('docs');
                importOnce('./lazy_panels.js').then(l => l.loadDocs()).then(m => m.selectDoc(target.name)).catch(() => {});
            } else if (target.kind === 'web') {
                window.open(target.href, '_blank', 'noopener,noreferrer');
            } else {
                window.location.assign(target.href);
            }
        });
    }
    document.body.appendChild(overlay);
}

export function showNotificationToast(agentLabel, detail, onClick) {
    const toast = document.createElement("div");
    toast.className = "notification-toast";
    const detailHtml = detail
        ? `<span class="notification-toast-detail">${detail}</span>`
        : "";
    toast.innerHTML = `<div class="notification-toast-body">
            <div class="notification-toast-header">
                <span class="notification-toast-title">⏳ <strong>${agentLabel}</strong> needs input</span>
                <button class="notification-toast-close">✕</button>
            </div>
            ${detailHtml}
            <a href="#" class="notification-toast-link">Open agent</a>
        </div>`;
    toast.querySelector(".notification-toast-link").addEventListener("click", (e) => {
        e.preventDefault();
        if (onClick) onClick();
        toast.remove();
    });
    toast.querySelector(".notification-toast-close").addEventListener("click", () => toast.remove());
    document.body.appendChild(toast);
    setTimeout(() => toast.remove(), 10000);
}
