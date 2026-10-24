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
 *   and runs onClick. A toast with an action has no timer and does not fade: it
 *   stays until it is used or dismissed (WCAG 2.2.1: a keyboard-only person is
 *   far from it, and nothing runs out under them). A plain toast keeps its timer.
 * opts.expanded: a phone shows the whole message from the start, not its first two lines (a path must be read, not tapped open).
 * opts.action.keepOpen: the button runs onClick but leaves the toast on screen (a Copy button: the
 *   person copies and goes on reading); the toast is still dismissed with its ×.
 *
 * Returns the toast element. Text is set with textContent only.
 */
/** How long a toast with no duration of its own stays (#6727): a short one 4 s; an error at least 8 s, and either longer by the length of its words (40 ms a character past 80, at most 20 s): a person has to be able to read what went wrong. */
export function toastMs(text, error) {
    const extra = Math.max(0, String(text || "").length - 80) * 40;
    return Math.min(20000, (error ? 8000 : 4000) + extra);
}

export function showToast(message, isError = false, duration, opts = {}) {
    const error = !!isError;
    const toast = document.createElement("div");
    toast.className = `toast ${error ? "error" : ""}`;

    let text = String(message ?? "");
    if (opts.blocked && !/^Blocked:/.test(text)) text = `Blocked: ${text}`;
    if (!opts.silent) announceNotice(text, error); // said once by the Notifications column's own regions, not by the toast (a region that appears already filled is not reliably announced); silent: another region already says it (a send's status)
    if (duration === undefined) duration = toastMs(text, error);
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
            if (!action.keepOpen) dismiss();
            try { action.onClick(button); } catch (e) { console.error("toast action failed:", e); }
        });
        toast.appendChild(button);
        toast.classList.add("has-action");
        duration = Infinity;
    }
    const close = document.createElement("button");
    close.type = "button";
    close.className = "toast-close";
    // named by its toast, so two toasts on screen are two buttons to a screen reader (#6152); the toast's own words, cut at a word
    const words = text.replace(/\s+/g, " ").trim();
    const cut = words.lastIndexOf(" ", 40);
    close.setAttribute("aria-label", words.length > 40 ? `Dismiss: ${words.slice(0, cut > 20 ? cut : 40)}…` : `Dismiss: ${words}`);
    close.textContent = "×";
    close.addEventListener("click", dismiss);
    toast.appendChild(close);
    toast.addEventListener("keydown", (e) => { if (e.key === "Escape") dismiss(); });
    // On a phone a long toast shows two lines (css); a tap opens the rest. The full text is in the element either way, so a screen reader reads it all.
    toast.addEventListener("click", (e) => { if (!e.target.closest("button")) { toast.classList.toggle("expanded"); clearTimeout(timer); timer = 0; } }); // a toast that is being read is not timed out (#6727)

    if (opts.expanded) toast.classList.add("expanded");
    toastHost().appendChild(toast);
    if (Number.isFinite(duration)) timer = setTimeout(dismiss, duration);
    return toast;
}

// Everything that appears over the page shares one column, #notices: the warnings and the update notice first (#warning-toasts), the short toasts below them (#toast-host), so none sits on another (the second short
// toast hid the first, and on a phone the toasts hid the notices' titles). Bottom right on a desktop, under the top bar on a phone (css).
export function noticeColumn() {
    let col = document.getElementById("notices");
    if (!col) {
        col = document.createElement("div");
        col.id = "notices";
        const warnings = document.createElement("div");
        warnings.id = "warning-toasts";
        const toasts = document.createElement("div");
        toasts.id = "toast-host";
        // What the notices say is said here, once, in two regions that stay in the page: polite for a toast, a notification or a status, assertive (an alert) for an error (#6213).
        const polite = document.createElement("div");
        polite.id = "notice-live"; polite.className = "sr-only"; polite.setAttribute("role", "status"); polite.setAttribute("aria-live", "polite"); polite.setAttribute("aria-atomic", "true");
        const loud = document.createElement("div");
        loud.id = "notice-alert"; loud.className = "sr-only"; loud.setAttribute("role", "alert");
        col.append(warnings, toasts);
        // Outside the column: the column is not drawn while it holds no notice (no empty landmark), and a region inside a hidden column is not announced by a browser.
        const announcer = document.createElement("div");
        announcer.id = "notice-announcer"; announcer.className = "sr-only";
        announcer.append(polite, loud);
        document.body.appendChild(announcer);
        // A named region, so a screen reader can jump to the notices (they are drawn fixed, wherever they are in the page).
        col.setAttribute("role", "region");
        col.setAttribute("aria-label", "Notifications");
        document.body.appendChild(col);
    }
    placeNotices();
    return col;
}

let _noticeFlip = false;
const _noticeQueue = []; // { text, assertive } waiting to be said, one at a time: two said in one tick would be one replacing the other
let _noticeTimer = 0;
const NOTICE_GAP_MS = 900; // a notice has this long to be spoken before the next replaces it in the same region

function sayNextNotice() {
    const next = _noticeQueue.shift();
    if (!next) { _noticeTimer = 0; return; }
    const el = document.getElementById(next.assertive ? "notice-alert" : "notice-live");
    if (el) {
        _noticeFlip = !_noticeFlip;
        el.textContent = String(next.text || "").replace(/\s+/g, " ").trim() + (_noticeFlip ? "" : "\u00a0"); // the same words twice are still a change
    }
    _noticeTimer = setTimeout(sayNextNotice, NOTICE_GAP_MS);
}

/** Say `text` once to a screen reader through the Notifications column's own regions: polite, or assertive for an error. Several in a row are said one after another. */
export function announceNotice(text, assertive = false) {
    const fresh = !document.getElementById("notices");
    noticeColumn();
    _noticeQueue.push({ text, assertive });
    if (_noticeTimer) return; // one is being spoken: this follows it
    _noticeTimer = setTimeout(sayNextNotice, fresh ? 100 : 0); // the regions of a fresh column get a moment before they speak
}

function toastHost() {
    noticeColumn();
    return document.getElementById("toast-host");
}

// On a phone the notices and the short toasts start below the top bar (the Agents picker, home, bookmarks, panel, settings), never over it: css reads --notice-top, set here from the bar's own bottom edge on the column
// itself (not on <html>, where the theme code treats every inline custom property as a theme colour) and kept current when the window changes.
export function placeNotices() {
    const bar = document.querySelector("header.top-bar");
    const top = `${Math.max(bar ? Math.round(bar.getBoundingClientRect().bottom) : 0, 0) + 8}px`;
    document.getElementById("notices")?.style.setProperty("--notice-top", top);
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
    announceNotice([title || 'Notification', message].filter(Boolean).join(': '));
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
    announceNotice([title || 'Notification', message].filter(Boolean).join(': '), level === 'error' || level === 'warning');
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
    announceNotice(`${toast.querySelector(".notification-toast-title").textContent.replace(/^\S+\s/, "")}${detail ? ": " + toast.querySelector(".notification-toast-detail").textContent : ""}`);
    setTimeout(() => toast.remove(), 10000);
}
