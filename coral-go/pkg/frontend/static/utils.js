// Load order only (#4996): the imports below with no names are loaded for what the module registers at load, in the order this module imported them before the split (the startup-sequence golden records it).
import './routes.js';
import './import_once.js';
import './vendor_loader.js';
import './agent_view.js';
import { browserLocal } from './safe_storage.js';
import './dock_page.js';
import { formatDuration } from './format.js';
import { showToast } from './notifications.js';
export { escapeAttr, escapeHtml, tagColor } from './html_escape.js';
export { notificationLinkTarget, showAlertNotification, showNotificationToast, showToast, showWorkflowNotification } from './notifications.js';
export { buildWelcome, showView, viewChanges } from './view_switch.js';
export { DOMPURIFY_CONFIG, artifactRefHex, highlightCodeIn, keepFragmentsInside, renderDocsMarkdown, renderMarkdown, renderMarkdownStrict, sanitizeHtml, sanitizeMarkdown } from './markdown.js';
export { isImagePath, isMediaPath, renderImagePanes, renderMediaPanes } from './media_panes.js';
export { _boardBodyCacheStatsForTests, boardAvatar, boardMessageDisplayText, externalMessageBadge, hubUserBadge, labelCodeBlocks, renderBoardMessageBody, setBoardMentionNames } from './board_message_text.js';
export { AGENT_NAME_PALETTE, agentNameColor, boardMessageColorStyle, boardTextColor, getAgentColor, hexToRgba } from './agent_colors.js';
/* Utility functions: HTML escaping and toast notifications */

// Debug logging — enabled via ?debug=1 URL param or localStorage coral-debug=1
const _debugEnabled = new URLSearchParams(location.search).has('debug') ||
    browserLocal.get('coral-debug') === '1';

export function dbg(...args) {
    if (_debugEnabled) console.log('[coral]', ...args);
}

















 


























/** A duration in seconds as "45s", "12m", "1h" or "69h 59m". Minutes are
 *  rounded on the total, so they carry into the hour: 3599s is "1h", never
 *  "60m", and nothing reads "69h 60m" (#3152). Empty for none or zero. */
export function formatDurationSec(sec) {
    return formatDuration(sec, { shape: 'rounded' });
}

/** The dashboard's board identity: the reserved Operator subscriber that task
 *  actions already use (agent_docs/board.md). Team chat posted as "dashboard"
 *  before #3171; those messages still render as the Operator's. */
export const OPERATOR_ID = 'Operator';
export function isOperatorId(id) { return id === OPERATOR_ID || id === 'dashboard'; }

/** Label fenced code blocks with their language (shown by .chat-prose CSS). */
/** Copy text to the clipboard. navigator.clipboard needs a secure context
 *  (https or localhost), so a dashboard opened over plain http from another
 *  machine falls back to a hidden textarea. Resolves to whether it worked. */
export async function copyText(text) {
    try {
        if (navigator.clipboard && window.isSecureContext) {
            await navigator.clipboard.writeText(text);
            return true;
        }
    } catch { /* fall through to the fallback */ }
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.cssText = 'position:fixed;top:0;left:0;opacity:0;pointer-events:none';
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try { ok = document.execCommand('copy'); } catch { ok = false; }
    ta.remove();
    return ok;
}

































export function copyBranchName(btn) {
    // The live header's chip is .branch-chip; the history header's is .title-branch (its copy
    // button threw before #3645's test found it).
    const chip = btn.closest(".branch-chip, .title-branch");
    const branchText = chip ? chip.querySelector(".branch-text").textContent : "";
    if (!branchText) return;
    navigator.clipboard.writeText(branchText).then(() => {
        showToast("Copied branch name");
    }).catch(() => {
        showToast("Failed to copy", true);
    });
}

// The rule for a NEW board or team name (#3534; the server's board.NewNamePattern): letters,
// digits, '-' and '_', starting with a letter or digit, at most 64. Existing boards keep
// any name. Returns '' when the name is fine, else a sentence saying why not.
const NEW_BOARD_NAME_RE = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;
const RESERVED_BOARD_NAMES = new Set(['projects', 'remotes', 'tasks']);
export function boardNameProblem(name) {
    const n = String(name || '');
    if (!n) return 'A board name is required.';
    if (n.length > 64) return 'A board name is at most 64 characters.';
    if (!NEW_BOARD_NAME_RE.test(n)) return "Use only letters, digits, '-' and '_' (no spaces, dots, '@' or '/'), starting with a letter or digit.";
    if (RESERVED_BOARD_NAMES.has(n.toLowerCase())) return `"${n}" is reserved; choose another name.`;
    return '';
}

/** Whether resp is Coral Hub's refusal of a reply too large to check for secrets (#3718: a JSON
 *  reply over 2 MiB), so a view can say why it is empty instead of showing "nothing". Reads a
 *  clone: resp's own body is left for the caller. */
export async function refusedTooLarge(resp) {
    if (!resp || resp.status !== 502 || !/json/i.test(resp.headers.get('Content-Type') || '')) return false;
    try { return (await resp.clone().json()).code === 'hub_reply_too_large'; } catch { return false; }
}
