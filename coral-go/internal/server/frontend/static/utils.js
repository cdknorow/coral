/* Utility functions: HTML escaping and toast notifications */

// Debug logging — enabled via ?debug=1 URL param or localStorage coral-debug=1
const _debugEnabled = new URLSearchParams(location.search).has('debug') ||
    localStorage.getItem('coral-debug') === '1';

export function dbg(...args) {
    if (_debugEnabled) console.log('[coral]', ...args);
}

export function escapeHtml(str) {
    if (str == null) return '';
    const div = document.createElement("div");
    div.textContent = String(str);
    return div.innerHTML;
}

export function escapeAttr(str) {
    // Escape for safe use in HTML attributes and JS string literals within onclick handlers.
    // Handles: & < > " ' \ newlines
    return str
        .replace(/&/g, "&amp;")
        .replace(/</g, "&lt;")
        .replace(/>/g, "&gt;")
        .replace(/"/g, "&quot;")
        .replace(/'/g, "&#39;")
        .replace(/\\/g, "\\\\")
        .replace(/\n/g, "\\n")
        .replace(/\r/g, "\\r");
}

export function showToast(message, isError = false, duration = 4000) {
    const toast = document.createElement("div");
    toast.className = `toast ${isError ? "error" : ""}`;
    toast.textContent = message;
    document.body.appendChild(toast);
    setTimeout(() => toast.remove(), duration);
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

export function showAlertNotification(title, message, level = 'info', link = null) {
    const icons = { success: '✅', warning: '⚠️', error: '❌', info: '💬' };
    const icon = icons[level] || icons.info;
    const overlay = document.createElement("div");
    overlay.className = "alert-notification-overlay";
    const linkHtml = link
        ? `<a href="#" class="alert-notification-link">${escapeHtml(link.label)}</a>`
        : '';
    overlay.innerHTML = `<div class="alert-notification">
            <div class="alert-notification-icon">${icon}</div>
            <div class="alert-notification-title">${title ? escapeHtml(title) : 'Notification'}</div>
            ${message ? `<div class="alert-notification-message">${escapeHtml(message)}</div>` : ''}
            ${linkHtml}
            <button class="alert-notification-ok btn btn-primary">OK</button>
        </div>`;
    overlay.querySelector(".alert-notification-ok").addEventListener("click", () => overlay.remove());
    overlay.addEventListener("click", (e) => { if (e.target === overlay) overlay.remove(); });
    if (link) {
        overlay.querySelector(".alert-notification-link").addEventListener("click", (e) => {
            e.preventDefault();
            overlay.remove();
            // docs:// links navigate to the Docs tab (e.g. "docs://workflow-quickstart")
            if (link.url.startsWith('docs://')) {
                const docName = link.url.replace('docs://', '');
                if (window.switchNavTab) window.switchNavTab('docs');
                import('./docs.js').then(m => m.selectDoc(docName));
            } else if (link.url.startsWith('http')) {
                window.open(link.url, '_blank');
            } else {
                window.location.href = link.url;
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
            <a href="#" class="notification-toast-link">Switch to session →</a>
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

const VIEW_IDS = [
    "welcome-screen",
    "live-session-view",
    "history-session-view",
    "scheduler-view",
    "messageboard-view",
    "workflows-view",
    "connected-apps-view",
    "cost-dashboard-view",
    "timeline-view",
    "kanban-view",
    "docs-view",
];

const VIEW_DISPLAY = {
    "welcome-screen": "flex",
    "live-session-view": "flex",
    "history-session-view": "flex",
    "scheduler-view": "block",
    "messageboard-view": "flex",
    "workflows-view": "block",
    "connected-apps-view": "block",
    "cost-dashboard-view": "flex",
    "timeline-view": "flex",
    "kanban-view": "flex",
    "docs-view": "block",
};

const FULL_WIDTH_VIEWS = new Set([
    "cost-dashboard-view",
    "timeline-view",
    "kanban-view",
    "workflows-view",
    "connected-apps-view",
]);

export function showView(activeId) {
    for (const id of VIEW_IDS) {
        const el = document.getElementById(id);
        if (el) el.style.display = id === activeId ? VIEW_DISPLAY[id] : "none";
    }
    const layout = document.querySelector('.layout');
    if (layout) layout.classList.toggle('sidebar-hidden', FULL_WIDTH_VIEWS.has(activeId));
}

// options is passed through to marked.parse for this call only, e.g.
// { breaks: true } to keep single newlines as line breaks.
export function renderMarkdown(content, options) {
    if (!content) return '';
    if (typeof marked !== 'undefined') {
        try {
            if (typeof hljs !== 'undefined' && !marked._hljsConfigured) {
                marked.setOptions({
                    highlight: (code, lang) => {
                        if (lang && hljs.getLanguage(lang)) {
                            return hljs.highlight(code, { language: lang }).value;
                        }
                        return hljs.highlightAuto(code).value;
                    },
                });
                marked._hljsConfigured = true;
            }
            const html = options ? marked.parse(content, options) : marked.parse(content);
            return typeof DOMPurify !== 'undefined' ? DOMPurify.sanitize(html) : html;
        } catch { /* fall through */ }
    }
    return escapeHtml(content);
}

// Shared agent color palette (Nord-inspired muted tones)
const _agentColorPalette = [
    '#81a1c1', '#a3be8c', '#b48ead', '#d08770',
    '#bf616a', '#88c0d0', '#ebcb8b', '#8fbcbb',
];
const _agentColorCache = {};

export function getAgentColor(name) {
    if (!name) return _agentColorPalette[0];
    if (_agentColorCache[name]) return _agentColorCache[name];
    const idx = Object.keys(_agentColorCache).length % _agentColorPalette.length;
    _agentColorCache[name] = _agentColorPalette[idx];
    return _agentColorCache[name];
}

export function hexToRgba(hex, alpha) {
    const r = parseInt(hex.slice(1, 3), 16);
    const g = parseInt(hex.slice(3, 5), 16);
    const b = parseInt(hex.slice(5, 7), 16);
    return `rgba(${r},${g},${b},${alpha})`;
}

export function copyBranchName(btn) {
    const branchText = btn.closest(".branch-chip").querySelector(".branch-text").textContent;
    navigator.clipboard.writeText(branchText).then(() => {
        showToast("Copied branch name");
    }).catch(() => {
        showToast("Failed to copy", true);
    });
}
