/* Pending (sent, not yet in the transcript) messages, the Working row and the Coral notices (#3837). Moved out of live_chat.js as it
 * was; no behaviour change. They are the chat's trailing chrome rows (.pending-messages, .chat-working): each builds its row with
 * makeBubble (chat_bubbles.js) and places it with lastContent and appendContent (chat_content.js), which keep the rows below the
 * transcript. live_chat.js calls syncPendingBubbles and syncWorkingIndicator after it renders. */
import { appendWork, lastContent } from './chat_content.js';
import { deriveSessionState } from './render.js';
import { escapeHtml, renderMarkdown } from './utils.js';
import { makeBubble } from './chat_bubbles.js';
import { unwrapPastedContent } from './pasted_content.js';
import { splitSharedImages } from './shared_images.js';
import { importOnce } from './import_once.js';
import { state } from './state.js';
import { storeKey } from './local_store.js';
import { browserLocal } from './safe_storage.js';
import { flushReply } from './chat_announce.js';

// ── Pending (sent, not yet in the transcript) messages ───────────────────
// A message typed into the command box only reaches the transcript once the
// agent takes it, which can be a whole turn later. Until then it is shown at
// the bottom of the chat so it does not seem to vanish: "Sent" when the agent
// was free and is taking it now, "Queued" when it was busy and the message
// waits behind the current work. The Working row sits between the two.
// Before either, "Sending…" while the server has not yet said the agent's
// input box took it and it was submitted (#4587): Sent means submitted.
const PENDING_TTL_MS = 15 * 60 * 1000;
const STORAGE_KEY_PENDING = "coral-pending-messages";
const pendingBySession = new Map(); // session_id -> [{ id, text, at, queued, sending }]
let pendingSeq = 0;
const recentUserEntriesBySession = new Map();

// Loaded by app.js once the store knows whose they are (#3681).
export function loadPendingFromStorage() {
    pendingBySession.clear();
    try {
        const raw = browserLocal.get(storeKey(STORAGE_KEY_PENDING));
        if (!raw) return;
        const obj = JSON.parse(raw);
        const now = Date.now();
        for (const [sid, list] of Object.entries(obj)) {
            if (Array.isArray(list)) {
                // A send still unconfirmed when the page went is not known to have been submitted: its draft is kept instead.
                const valid = list.filter(p => p && p.text && !p.sending && (now - (p.at || 0) < PENDING_TTL_MS));
                if (valid.length > 0) {
                    pendingBySession.set(sid, valid);
                    for (const p of valid) {
                        if (p.id > pendingSeq) pendingSeq = p.id;
                    }
                }
            }
        }
    } catch (e) {
        console.warn("[coral] Failed to load pending messages from storage", e);
    }
}

function savePendingToStorage() {
    try {
        const obj = {};
        const now = Date.now();
        for (const [sid, list] of pendingBySession.entries()) {
            const valid = (list || []).filter(p => now - (p.at || 0) < PENDING_TTL_MS);
            if (valid.length > 0) {
                obj[sid] = valid;
            }
        }
        browserLocal.set(storeKey(STORAGE_KEY_PENDING), JSON.stringify(obj));
    } catch (e) {
        console.warn("[coral] Failed to save pending messages to storage", e);
    }
}


const normalizeMsg = (t) => String(t || "").replace(/\s+/g, " ").trim();

// Codex can record attached images separately or wrap their paths in image
// metadata. Match the accompanying text, not the transport representation.
function pendingMatchText(text) {
    const normalized = normalizeMsg(String(text || "")
        .replace(/<image\b[^>]*\bpath="([^"]+)"[^>]*>[\s\S]*?<\/image>/g, '$1')
        .replace(/\[Image #\d+\]/g, ''));
    const body = splitSharedImages(normalized).rest.replace(/^(?:(?:\/(?:[^\/]+\/)*uploads\/[^\s]+|\/\S*\/uploads\/\S+)\s+)+/, '').trim();
    return body || normalized;
}

export function rememberUserMessage(sessionId, msg) {
    const entries = recentUserEntriesBySession.get(sessionId) || [];
    const entry = { ...msg, receivedAt: Date.now(), remaining: pendingMatchText(msg.content) };
    entries.push(entry);
    recentUserEntriesBySession.set(sessionId, entries.slice(-200));
    settlePending(sessionId, entry);
    dropStaleSentPending(sessionId, msg);
}


// Claude Code records an Esc interrupt as a user entry like this
export const INTERRUPT_RE = /^\[Request interrupted by user[^\]]*\]$/;

// Messages Coral types into the agent's terminal: board and task nudges
// (internal/background/notifier.go, routes/board.go, routes/agent_tasks.go).
// The transcript records them as if the user sent them.
// Not anchored at the end: two nudges typed at the same moment arrive as one
// message ("...to start.You have 1 unread message...").
const CORAL_NUDGE_RES = [
    /^You have \d+ unread messages? on the message board/i,
    /^You have tasks available/i,
    /^You have a new task in Coral \(#\d+:/i,
    /^\[Task #\d+[^\]]*\]/i,
    /^\[New Task #\d+[^\]]*\]/i, // the new-task broadcast, "@notify-all [New Task #N (priority)] ..."
    /^\[Wait resolved\]/i,
	/^\[Coral\b[^\]]*\]/i,
	/^Stop hook feedback:/i, // Claude Code's block from a Stop hook (the board check): not something the user typed (#4171)
];

const CORAL_HEALTH_NUDGE_RE = /^\[Coral health check\]/i;

// Back-to-back notices (nudges that queued up while the agent was busy)
// roll up into one showing the latest text and how many there were.
function coralNoticeSummary(text) {
    return /^\[Coral UI panel request\b/i.test(text) ? text.split(/\r?\n/, 1)[0] : text;
}
export function appendCoralNotice(container, text, health = false) {
    text = coralNoticeSummary(text);
	// A notice is a step of the work it prompts, not a pill of its own (#4171): it goes into the steps group, which it starts when
	// the last thing on the page is not one. Back-to-back notices still roll up into one.
	const tail = lastContent(container);
	const prev = tail && tail.classList.contains("work-group") ? tail.querySelector(":scope > .work-group-body").lastElementChild : tail;
	const noticeClass = health ? "chat-system-health" : "chat-system-general";
	if (prev && prev.classList.contains("chat-system") && prev.classList.contains(noticeClass)) {
        const count = (Number(prev.dataset.count) || 1) + 1;
        prev.dataset.count = String(count);
        const textEl = prev.querySelector(".chat-system-text");
        textEl.textContent = text;
        textEl.title = text;
        let badge = prev.querySelector(".chat-system-count");
        if (!badge) {
            badge = document.createElement("span");
            badge.className = "chat-system-count";
            prev.appendChild(badge);
        }
        badge.textContent = `×${count}`;
        badge.title = `${count} notices from Coral in a row`;
        return;
    }
	// A notice after a folded reply (the previous turn's end) starts a group of its own, so each turn keeps its label.
	const ended = tail && tail.classList.contains("work-group") && tail.querySelector(":scope > .work-group-body").lastElementChild?.classList.contains("folded-reply");
	appendWork(container, makeBubble(`chat-system ${noticeClass}`,
		`<span class="material-icons" aria-hidden="true">${health ? "health_and_safety" : "notifications"}</span><span class="chat-system-label">${health ? "Coral · Health check" : "Coral"}</span><span class="chat-system-text" title="${escapeHtml(text)}">${escapeHtml(text)}</span>`), { newGroup: !!ended });
}

export function normalizeNudgeText(content) {
    let text = String(content || "").trim();
    // In CLI agents (e.g. Antigravity CLI in plan/goal mode), the agent prefixes
    // terminal input with one or more slash commands (e.g. "/plan ").
	text = text.replace(/^(?:\/[a-zA-Z0-9_-]+\s*)+/, "").trim();
	text = text.replace(/^\[Coral health check\]\s*/i, "").trim();
    // If preceded by a notification tag like "@QA Engineer " or "@notify-all "
    return text.replace(/^@[^@\n\r]+?\s+(?=(?:You have|\[Task #|\[New Task #|\[Wait resolved\]|\[Coral\b))/i, "").trim();
}

export function isCoralHealthNudge(content) {
	const text = String(content || "").trim().replace(/^(?:\/[a-zA-Z0-9_-]+\s*)+/, "").trim();
	return CORAL_HEALTH_NUDGE_RE.test(text);
}

export function isCoralNudge(content) {
	const raw = String(content || "").trim().replace(/^(?:\/[a-zA-Z0-9_-]+\s*)+/, "").trim();
	if (CORAL_HEALTH_NUDGE_RE.test(raw)) return true;
	const text = normalizeNudgeText(content);
	return CORAL_NUDGE_RES.some(re => re.test(text));
}

// What a send is doing, said once in one persistent live region (#4680): the bubbles' labels are made in their final words each time (a bubble moves between groups), which some screen readers
// do not announce, so they carry no role; this element stays in the page and its text changes in place. Only for the agent on screen.
function announceSend(sessionId, words) {
    if (!(state.currentSession && state.currentSession.session_id === sessionId)) return;
    const el = document.getElementById("chat-send-status");
    if (!el) return;
    if (el.textContent !== words) { el.textContent = words; return; }
    el.textContent = ""; // the same words again (two sends in a row): cleared first so the change is heard
    requestAnimationFrame(() => { el.textContent = words; });
}

/** Record a message just sent to an agent so the chat can show it right away. sending: it shows "Sending…" until
 *  confirmPendingMessage (the server said it was submitted) or removePendingMessage (it was not). */
export function addPendingMessage(sessionId, text, sentAt = Date.now(), { sending = false } = {}) {
    if (!sessionId || !normalizeMsg(text)) return;
    // Panel retries carry one stable identity even when the first delivery's
    // transcript arrived before the retry began. Never add a second notice.
    const panelMarker = /^\[Coral UI panel request [^\]]+\]/.exec(text)?.[0];
    if (panelMarker && ((pendingBySession.get(sessionId) || []).some(p => p.text.includes(panelMarker))
        || (recentUserEntriesBySession.get(sessionId) || []).some(entry => String(entry.content || '').includes(panelMarker)))) return;
    const session = (state.liveSessions || []).find(s => s.session_id === sessionId)
        || (state.currentSession && state.currentSession.session_id === sessionId ? state.currentSession : { session_id: sessionId });
    const queued = agentIsBusy(session);
    const list = pendingBySession.get(sessionId) || [];
    list.push({ id: ++pendingSeq, text, at: sentAt, queued, sending });
    pendingBySession.set(sessionId, list);
    // A fast transcript poll may beat the send response. Reconcile against
    // unconsumed recent receipts instead of waiting for another transcript entry.
    for (const entry of recentUserEntriesBySession.get(sessionId) || []) {
        if (entry.receivedAt >= sentAt && !Number.isNaN(Date.parse(entry.timestamp || ""))) settlePending(sessionId, entry);
    }
    savePendingToStorage();
    if (!queued) lastSendAt.set(sessionId, Date.now());
    const container = document.getElementById("live-history-messages");
    if (container && state.currentSession && state.currentSession.session_id === sessionId) {
        syncPendingBubbles(container, sessionId);
        syncWorkingIndicator(container, state.currentSession);
        container.scrollTop = container.scrollHeight;
    }
    if (sending) announceSend(sessionId, "Sending message");
}

/** The server said the send at sentAt was submitted: its bubble says Sent (or Queued) from now on (#4587). */
export function confirmPendingMessage(sessionId, sentAt) {
    const p = (pendingBySession.get(sessionId) || []).find(x => x.at === sentAt);
    if (!p || !p.sending) return;
    p.sending = false;
    savePendingToStorage();
    const container = document.getElementById("live-history-messages");
    if (container && state.currentSession && state.currentSession.session_id === sessionId) {
        syncPendingBubbles(container, sessionId);
        syncWorkingIndicator(container, state.currentSession); // the Sent bubble moves to the end: Working goes back below it
    }
    announceSend(sessionId, p.queued ? "Message queued" : "Message sent");
}

/** Withdraw the pending bubble of a send that was refused (its terminal was gone, #3218). */
export function removePendingMessage(sessionId, sentAt) {
    const list = pendingBySession.get(sessionId);
    const i = list ? list.findIndex(p => p.at === sentAt) : -1;
    if (i < 0) return;
    list.splice(i, 1);
    lastSendAt.delete(sessionId);
    savePendingToStorage();
    const container = document.getElementById("live-history-messages");
    if (container && state.currentSession && state.currentSession.session_id === sessionId) {
        syncPendingBubbles(container, sessionId);
        syncWorkingIndicator(container, state.currentSession);
    }
    // The error under the composer (an alert) says why, a moment after this: say it here only when that did not appear (one failure, one announcement, #6213).
    setTimeout(() => { if (document.getElementById("composer-error")?.hidden !== false) announceSend(sessionId, "Message not sent, your text is back in the box"); }, 50);
}

// An interrupt ends the current work; Claude Code then submits whatever was
// queued, so those messages are now being worked on.
export function noteInterrupt(sessionId, msg) {
    const list = pendingBySession.get(sessionId);
    const ts = Date.parse(msg.timestamp || "");
    let released = false;
    for (const p of list || []) {
        if (p.queued && (Number.isNaN(ts) || p.at <= ts + 5000)) {
            p.queued = false;
            released = true;
        }
    }
    if (released) {
        lastSendAt.set(sessionId, Date.now());
        savePendingToStorage();
    } else {
        lastSendAt.delete(sessionId);
    }
}

// Drop the pending messages this transcript entry accounts for. Messages
// queued while the agent was busy can arrive merged into one entry (e.g.
// after an interrupt), so every pending message it contains is settled, each
// occurrence consuming one. Only entries written after the send can match,
// so an earlier identical message ("continue") does not clear a new one.
function settlePending(sessionId, msg) {
    const list = pendingBySession.get(sessionId);
    if (!list || !list.length) return;
    let rest = msg.remaining;
    const ts = Date.parse(msg.timestamp || "");
    if (!rest) return;
    const whole = rest;
    const eligible = (p) => Number.isNaN(ts) || ts >= p.at - 10000;
    let settled = 0;
    let lastSettledId = 0;
    for (let i = 0; i < list.length;) {
        const p = list[i];
        const want = pendingMatchText(p.text);
        const at = eligible(p) && want ? rest.indexOf(want) : -1;
        if (at !== -1) {
            lastSettledId = Math.max(lastSettledId, p.id);
            rest = rest.slice(0, at) + rest.slice(at + want.length);
            list.splice(i, 1);
            settled++;
        } else {
            i++;
        }
    }
    // The transcript may hold a shortened form of a long message
    if (!settled) {
        const i = list.findIndex(p => eligible(p) && pendingMatchText(p.text).includes(whole));
        if (i !== -1) {
            lastSettledId = Math.max(lastSettledId, list[i].id);
            list.splice(i, 1);
            settled++;
            rest = "";
        }
    }
    msg.remaining = rest;
    // The agent took one of our messages: it is working on it from then on
    if (settled) {
        dropEarlierPending(list, lastSettledId);
        lastSendAt.set(sessionId, Number.isNaN(ts) ? Date.now() : ts);
        savePendingToStorage();
    }
}

// Messages reach the agent in the order they were sent, so once a later one
// shows up in the transcript every earlier pending message was taken too, even
// when its text did not match (reworded, merged, image paths). Without this
// such a bubble would sit at the bottom of the chat until it expired.
function dropEarlierPending(list, settledId) {
    for (let i = list.length - 1; i >= 0; i--) {
        if (list[i].id < settledId) list.splice(i, 1);
    }
}

// A "Sent" message (the agent was free) is picked up within moments. When the
// transcript has moved on well past it, it was taken even if never matched.
// Queued messages are exempt: they legitimately wait behind a long turn.
const SENT_STALE_MS = 60 * 1000;
function dropStaleSentPending(sessionId, msg) {
    const list = pendingBySession.get(sessionId);
    if (!list || !list.length) return;
    const ts = Date.parse(msg.timestamp || "");
    if (Number.isNaN(ts)) return;
    const before = list.length;
    for (let i = list.length - 1; i >= 0; i--) {
        if (!list[i].queued && ts > list[i].at + SENT_STALE_MS) list.splice(i, 1);
    }
    if (list.length !== before) savePendingToStorage();
}

function syncPendingGroup(container, cls, items, label) {
    let wrap = container.querySelector(`:scope > .pending-messages.${cls}`);
    if (!items.length) {
        if (wrap) wrap.remove();
        return null;
    }
    if (!wrap) {
        wrap = document.createElement("div");
        wrap.className = `pending-messages ${cls}`;
    }
    if (wrap.dataset.count !== String(items.length)) wrap.dataset.count = String(items.length); // a phone says "2 queued" once instead of a label on every bubble (#6737)
    const want = new Set(items.map(p => p.id));
    for (const el of Array.from(wrap.children)) {
        if (!el.classList.contains("pending-count") && !want.has(Number(el.dataset.pendingId))) el.remove();
    }
    const have = new Set(Array.from(wrap.children).map(el => Number(el.dataset.pendingId)));
    for (const p of items) {
        if (have.has(p.id)) continue;
        const coral = /^\[Coral UI panel request\b/i.test(normalizeNudgeText(p.text));
        const lead = splitSharedImages(unwrapPastedContent(p.text));
        const summary = coralNoticeSummary(normalizeNudgeText(p.text));
        const el = coral
            ? makeBubble("chat-system chat-system-general pending", `<span class="material-icons" aria-hidden="true">notifications</span><span class="chat-system-label">Coral</span><span class="chat-system-text" title="${escapeHtml(summary)}">${escapeHtml(summary)}</span><span class="pending-label">${label}</span>`)
            : makeBubble("chat-bubble human pending", `<div class="message-text">${renderMarkdown(lead.rest, undefined, 'chat')}</div><div class="pending-images"></div><div class="pending-label">${label}</div>`);
        el.dataset.pendingId = String(p.id);
        // Images pasted in the dashboard lead the text as links and a download hint for the agent (#5284): the person sees the same thumbnail chips the sent message will have, never the links (#6199).
        if (!coral && lead.images.length) {
            importOnce('./chat_thumbs.js').then(m => { const slot = el.querySelector(':scope > .pending-images'); if (slot) { slot.innerHTML = m.sharedImageChips(lead.images); m.wireThumbs(el); } }).catch(() => {});
        }
        // Keep send order even when a message moves between groups
        const next = Array.from(wrap.children).find(c => Number(c.dataset.pendingId) > p.id);
        wrap.insertBefore(el, next || null);
    }
    // "2 queued" is a line of text in the list (a phone shows it; the per-message labels go then), not a ::before: assistive technology reads text, not generated content (#6737)
    if (cls === "pending-queued") {
        let c = wrap.querySelector(":scope > .pending-count");
        const said = items.length > 1 ? `${items.length} queued` : "";
        if (said) {
            if (!c) { c = document.createElement("div"); c.className = "pending-count"; wrap.prepend(c); }
            if (c.textContent !== said) c.textContent = said;
        } else if (c) c.remove();
    }
    return wrap;
}

export function syncPendingBubbles(container, sessionId) {
    const now = Date.now();
    const list = (pendingBySession.get(sessionId) || []).filter(p => now - p.at < PENDING_TTL_MS);
    pendingBySession.set(sessionId, list);
    savePendingToStorage();
    const sent = syncPendingGroup(container, "pending-sent", list.filter(p => !p.queued && !p.sending), "Sent");
    const queued = syncPendingGroup(container, "pending-queued", list.filter(p => p.queued && !p.sending), "Queued");
    const sending = syncPendingGroup(container, "pending-sending", list.filter(p => p.sending), "Sending…");
    // Always last, below any newly rendered messages: sent, then queued, then the ones still sending
    for (const wrap of [sent, queued, sending]) {
        if (wrap && container.lastElementChild !== wrap) container.appendChild(wrap);
    }
}

// ── Working indicator ────────────────────────────────────────────────────
// Between sending a message and the first tool call or reply there can be a
// long silence. Show a "Working" row at the bottom of the chat while the
// agent's state is Working, and right after a send until the agent's first
// output lands (its state can take a moment to flip).

const SEND_GRACE_MS = 90 * 1000;
const lastSendAt = new Map(); // session_id -> ms of the last send with no agent output since

state.lastSendAt = lastSendAt; // read by the sleeping view: a message sent and nothing back since (#6058)

export function noteAgentActivity(sessionId, msg) {
    dropStaleSentPending(sessionId, msg);
    const sent = lastSendAt.get(sessionId);
    if (!sent) return;
    const ts = Date.parse(msg.timestamp || "");
    if (Number.isNaN(ts) || ts >= sent - 2000) lastSendAt.delete(sessionId);
}

function agentIsWorking(session) {
    const row = (state.liveSessions || []).find(s => s.session_id === session.session_id) || session;
    const key = deriveSessionState(row);
    if (key === "working") return true;
    // Awaiting the user, stuck, asleep or ended: nothing is in flight
    if (key !== "idle" && key !== "your_turn" && key !== "waiting_on") return false;
    const sent = lastSendAt.get(session.session_id);
    return !!sent && Date.now() - sent < SEND_GRACE_MS;
}

// Busy for queueing purposes: working, or still on a message just sent
const agentIsBusy = agentIsWorking;

/** "12 s", "1 min 05 s": the elapsed time as short digits. */
export function workingElapsedText(secs) {
    if (secs < 60) return `${secs} s`;
    return `${Math.floor(secs / 60)} min ${String(secs % 60).padStart(2, "0")} s`;
}

/** One second of the Working row: the quiet digits from 5 s; at 30 s the "Still working" line with its terminal link and one polite status; then a status once a minute. A row that left the page stops its own timer. */
export function tickWorking(el) {
    if (!el.isConnected) { clearInterval(el._tick); return; }
    const secs = Math.max(0, Math.floor((Date.now() - Number(el.dataset.since)) / 1000));
    el.querySelector(".chat-working-time").textContent = secs >= 5 ? ` · ${workingElapsedText(secs)}` : "";
    if (secs < 30) return;
    el.querySelector(".chat-working-still").hidden = false;
    const live = el.querySelector(".chat-working-live");
    const minutes = Math.floor(secs / 60);
    const said = secs >= 60 ? `Still working, ${minutes} ${minutes === 1 ? "minute" : "minutes"}` : "Still working, 30 seconds";
    if (live.textContent !== said && (secs === 30 || secs % 60 === 0 || !live.textContent)) live.textContent = said;
}

export function syncWorkingIndicator(container, session) {
    let el = container.querySelector(":scope > .chat-working");
    // A card asking for input replaces the Working row
    if (!agentIsWorking(session) || container.querySelector(":scope > .chat-needs-input")) {
        if (el) { clearInterval(el._tick); el.remove(); if (!container.querySelector(":scope > .chat-needs-input")) flushReply(session.session_id); } // the turn is over: the reply is said once
        return;
    }
    if (!el) {
        el = document.createElement("div");
        el.className = "chat-working";
        // One status for a screen reader, in its own element (the visible line next to it is never re-read): "Working" now, "Still working, 30 seconds" at 30 s, then once a minute (#6058).
        // Just the state: the latest step is already the group summary above
        el.innerHTML = `<span class="chat-working-dots" aria-hidden="true"><i></i><i></i><i></i></span><span class="chat-working-label">Working</span>`
            + `<span class="chat-working-time" aria-hidden="true"></span><span class="chat-working-live sr-only" role="status">Working</span>`
            + `<span class="chat-working-still" hidden>Still working. <button type="button" class="chat-working-link" data-on-click="terminal-view-open">Open the terminal</button> to see what it is doing.</span>`;
        // How long it has been (#6058): quiet digits from 5 s, a polite status at 30 s and then once a minute, and a way to look after 30 s. Counted from the send when there was one, else from when the row first showed.
        el.dataset.since = String(lastSendAt.get(session.session_id) || Date.now());
        el._tick = setInterval(() => tickWorking(el), 1000); // interval-ok: one second of the Working row; tickWorking clears it when the row leaves the page, and syncWorkingIndicator clears it when it removes the row
    }
    // Below what the agent is working on (including Sent messages), above
    // anything Queued behind it
    const queued = container.querySelector(":scope > .pending-messages.pending-queued");
    if (queued) {
        if (el.nextElementSibling !== queued) container.insertBefore(el, queued);
    } else if (container.lastElementChild !== el) {
        container.appendChild(el);
    }
}
