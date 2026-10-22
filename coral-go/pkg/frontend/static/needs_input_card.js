/* Moved out of live_chat.js as it was (#3837): no behaviour change. */
import { deriveSessionState } from './render.js';
import { escapeHtml, renderMarkdown } from './utils.js';
import { state } from './state.js';
import { claudeSignedIn, askSigninSoon, isLocalClaude } from './signin_line.js';

// ── Needs input ──────────────────────────────────────────────────────────
// Claude Code writes a tool call to the transcript only once it completes, so
// an open permission prompt, AskUserQuestion or plan approval never shows in
// the chat on its own. The PreToolUse hook reports the call as it starts
// (it arrives with the chat poll's reply as `pending`); the Notification hook sets waiting_for_input. Together
// they drive a card at the bottom of the chat saying what is being asked.
export const pendingToolBySession = new Map(); // session_id -> pending tool or null
// The prompt's options as the terminal shows them right now (numbered, with
// the digit that answers each), read by the server from a pane capture.
export const promptOptionsBySession = new Map(); // session_id -> { question, options: [{ n, label, action }], review }
export const loginBySession = new Map(); // session_id -> { reason } while the CLI waits for a sign-in, null when it doesn't (#3830)

// Tools whose whole purpose is to wait for the user
const BLOCKING_TOOLS = {
    AskUserQuestion: "question",
    ask_question: "question",
    ExitPlanMode: "plan",
};

// Codex has no Notification hook, so nothing reports its sign-in and update
// menus or its approval prompts. For Codex the screen is read on every poll
// and an open numbered prompt raises the card by itself. Antigravity/Agy and
// Gemini also display interactive numbered prompts directly on screen.
function readsPromptsFromScreen(session) {
    return !!session && (
        session.agent_type === "codex" ||
        session.agent_type === "agy" ||
        session.agent_type === "antigravity" ||
        session.agent_type === "gemini"
    );
}

function needsInputInfo(session) {
    const row = (state.liveSessions || []).find(s => s.session_id === session.session_id) || session;
    const key = deriveSessionState(row);
    if (key === "sleeping") return null;
    // Waiting for a sign-in (#3830): the chat reply says so when it has; otherwise the row.
    const login = loginBySession.has(session.session_id) ? loginBySession.get(session.session_id) : (row.needs_login ? { reason: "" } : null);
    if (login) return { kind: "login", reason: login.reason };
    const pt = pendingToolBySession.get(session.session_id) || null;
    const screen = promptOptionsBySession.get(session.session_id) || null;
    if (pt && BLOCKING_TOOLS[pt.tool_name]) return { kind: BLOCKING_TOOLS[pt.tool_name], tool: pt, screen };
    if (row.waiting_for_input && row.waiting_kind !== "login") return { kind: "permission", tool: pt, screen, summary: String(row.waiting_summary || "").replace(/^Notification:\s*/, "") };
    if (readsPromptsFromScreen(session) && screen && screen.options.length) return { kind: pt ? "permission" : "prompt", tool: pt, screen };
    if (key === "check_terminal") {
        // A new Claude agent that has not started: when the server says Claude is not signed in (#5838's check after the first launch), the card says that, not the generic "startup prompt" (#5852).
        // Only for a session on THIS computer: claude_signed_in is this computer's Claude, and an agent on another server (or seen through Coral Hub) must not send the person to the wrong terminal.
        if (session.agent_type === "claude" && isLocalClaude(row)) {
            const signedIn = claudeSignedIn();
            if (signedIn === false) return { kind: "startup", signedOut: true };
            if (signedIn === undefined) askSigninSoon();
        }
        return { kind: "startup" };
    }
    return null;
}

// ── Needs-input card markup ──────────────────────────────────────────────
// The question leads, large; options are compact chips (or small cards when
// they carry descriptions), numbered like the terminal's keys, with the
// terminal's default outlined. A text option becomes an inline field, "Chat
// about this" a quiet link, and the review step a summary with Submit as the
// primary action. Open terminal is a footer link.

const KICKERS = {
    question: "Claude is asking",
    plan: "Plan ready for your review",
    permission: "Permission needed",
    startup: "Waiting in the terminal",
    login: "Sign-in needed",
    prompt: "Needs your input",
};

function agentDisplayName(session) {
    const at = (session && session.agent_type) || "claude";
    if (at === "agy" || at === "antigravity" || at === "gemini") return "Antigravity";
    if (at === "codex") return "Codex";
    if (at === "pi") return "Pi";
    return "Claude";
}

// data-label is the on-screen label (what the server checks before
// answering); the button shows the hook's full label when the screen's was
// cut off by wrapping.
function optionButtonHtml(o, desc, fullLabel) {
    return `<button type="button" class="cni-answer${o.selected ? " is-default" : ""}" data-n="${Number(o.n)}" data-label="${escapeHtml(o.label)}" data-action="select">`
        + `<kbd class="cni-key">${o.n}</kbd>`
        + `<span class="cni-answer-text"><span class="cni-answer-label">${escapeHtml(fullLabel || o.label)}</span>`
        + (desc ? `<span class="cni-answer-desc">${escapeHtml(desc)}</span>` : "")
        + `</span></button>`;
}

// A multi-select option (#5232): a box the digit toggles, its mark read from the terminal (never flipped here),
// answering nothing until Submit.
function checkOptionButtonHtml(o, desc, fullLabel) {
    return `<button type="button" class="cni-answer cni-check${o.checked ? " is-checked" : ""}" role="checkbox" aria-checked="${o.checked ? "true" : "false"}" data-n="${Number(o.n)}" data-label="${escapeHtml(o.label)}" data-action="toggle">`
        + `<kbd class="cni-key">${Number(o.n)}</kbd>`
        + `<span class="cni-box" aria-hidden="true">${o.checked ? "\u2714" : ""}</span>`
        + `<span class="cni-answer-text"><span class="cni-answer-label">${escapeHtml(fullLabel || o.label)}</span>`
        + (desc ? `<span class="cni-answer-desc">${escapeHtml(desc)}</span>` : "")
        + `</span></button>`;
}

function answerControlsHtml(info, descriptions = {}, fullLabels = {}) {
    const opts = (info.screen && info.screen.options) || [];
    if (!opts.length) return { body: "", foot: "" };
    const review = ((info.screen && info.screen.review) || []).length > 0;
    const selects = opts.filter(o => (o.action || "select") === "select");
    const texts = opts.filter(o => o.action === "text");
    const chats = opts.filter(o => o.action === "chat");
    let body = "";
    let foot = "";
    if (review) {
        // Submit answers / Cancel: footer actions, Submit primary
        foot += selects.map((o, i) => `<button type="button" class="cni-answer btn btn-sm ${i === 0 ? "btn-primary" : ""}" data-n="${Number(o.n)}" data-label="${escapeHtml(o.label)}" data-action="select">${escapeHtml(o.label)}</button>`).join("");
    } else if (selects.length) {
        const withDesc = selects.some(o => descriptions[fullLabels[o.label] || o.label] || descriptions[o.label]);
        const multi = !!(info.screen && info.screen.multi);
        const button = multi ? checkOptionButtonHtml : optionButtonHtml;
        body += `<div class="cni-options ${withDesc ? "cni-options-cards" : "cni-options-chips"}"${multi ? ` role="group" aria-label="Pick everything that applies"` : ""}>`
            + selects.map(o => button(o, descriptions[fullLabels[o.label] || o.label] || descriptions[o.label], fullLabels[o.label])).join("") + `</div>`;
        const submit = info.screen && info.screen.submit;
        if (multi && submit) foot += `<button type="button" class="cni-answer btn btn-sm btn-primary cni-submit" data-n="0" data-label="${escapeHtml(submit.label)}" data-action="submit">${escapeHtml(submit.label)}</button>`;
    }
    for (const o of texts) {
        const placeholder = /^type something/i.test(o.label) ? "Or type your own answer…" : `${o.label.replace(/\.$/, "")}…`;
        body += `<form class="cni-text-form" data-n="${Number(o.n)}" data-label="${escapeHtml(o.label)}" data-action="text">`
            + `<input type="text" class="cni-text-input" placeholder="${escapeHtml(placeholder)}" aria-label="${escapeHtml(o.label)}">`
            + `<button type="submit" class="btn btn-sm">Send</button></form>`;
    }
    for (const o of chats) {
        foot = `<button type="button" class="cni-link cni-answer" data-n="${Number(o.n)}" data-label="${escapeHtml(o.label)}" data-action="chat">${escapeHtml(o.label)} instead</button>` + foot;
    }
    return { body, foot };
}

function reviewHtml(info) {
    const items = (info.screen && info.screen.review) || [];
    if (!items.length) return "";
    return `<dl class="cni-review">` + items.map(it =>
        `<div class="cni-review-row"><dt>${escapeHtml(it.question)}</dt><dd>${escapeHtml(it.answer || "—")}</dd></div>`).join("") + `</dl>`;
}

function needsInputHtml(info, session) {
    const inp = (info.tool && info.tool.input) || {};
    const screen = info.screen || {};
    const onScreen = (screen.options || []).length > 0;
    const screenQuestion = onScreen ? (screen.question || "") : "";
    const name = agentDisplayName(session);
    let kicker = (info.kind === "question") ? `${name} is asking` : (KICKERS[info.kind] || "Needs your input");
    let question = "";
    let context = "";
    let descriptions = {};

    let fullLabels = {};
    if (info.kind === "question") {
        const qs = Array.isArray(inp.questions) ? inp.questions : [];
        for (const q of qs) {
            for (const o of Array.isArray(q.options) ? q.options : []) {
                const label = typeof o === "string" ? o : (o && o.label);
                const desc = typeof o === "object" && o ? o.description : "";
                if (label && desc) descriptions[label] = desc;
            }
        }
        // The screen can cut a long question or label short (wrapping); use
        // the hook's full text for whichever one matches what is on screen.
        const norm = (t) => String(t || "").replace(/\s+/g, " ").trim().toLowerCase();
        const onScreenQ = norm(screenQuestion);
        const match = onScreenQ ? qs.find(q => { const h = norm(q.question); return h && (h === onScreenQ || h.endsWith(onScreenQ) || onScreenQ.endsWith(h) || h.includes(onScreenQ)); }) : null;
        question = (match && match.question) || screenQuestion || (qs[0] && qs[0].question) || "";
        const hookLabels = (match ? match.options : qs.flatMap(q => q.options || [])) || [];
        for (const o of (screen.options || [])) {
            const full = hookLabels.find(h => {
                const hLabel = typeof h === "string" ? h : (h && h.label);
                const a = norm(hLabel), b = norm(o.label);
                return a && b && a !== b && a.startsWith(b);
            });
            if (full) fullLabels[o.label] = typeof full === "string" ? full : full.label;
        }
        if (!onScreen && qs.length) {
            // No live options (yet): list the questions from the hook
            context = qs.map(q => `<div class="cni-static-q">${escapeHtml(q.question || "")}`
                + (Array.isArray(q.options) && q.options.length ? `<ul>${q.options.map(o => `<li>${escapeHtml(typeof o === "string" ? o : (o && o.label) || "")}</li>`).join("")}</ul>` : "") + `</div>`).slice(1).join("");
        }
    } else if (info.kind === "plan") {
        question = screenQuestion ? "Ready to proceed with this plan?" : `${name} has a plan ready`;
        if (inp.plan) context = `<div class="cni-plan message-text">${renderMarkdown(inp.plan, undefined, 'chat')}</div>`;
    } else if (info.kind === "permission") {
        const tool = info.tool && info.tool.tool_name;
        if (tool) kicker = `Permission needed · ${escapeHtml(tool)}`;
        question = screenQuestion || (tool ? `Allow ${tool}?` : (info.summary || "The agent is waiting for you"));
        const target = inp.command || inp.file_path || inp.path || inp.url || inp.query || inp.pattern || "";
        if (inp.description && inp.command) context += `<div class="cni-context-text">${escapeHtml(inp.description)}</div>`;
        if (target) context += `<pre class="cni-target"><code>${escapeHtml(target)}</code></pre>`;
    } else if (info.kind === "prompt") {
        question = screenQuestion || "The agent is waiting for you";
    } else {
        if (info.signedOut) {
            kicker = "Sign-in needed";
            question = "Claude is not signed in";
            context = `<div class="cni-context-text">Open the terminal and sign in, then come back and type.</div>`;
        } else {
            question = "The agent may be waiting at a startup prompt";
            context = `<div class="cni-context-text">For example, trusting this folder or signing in. Open the terminal, answer it there, then come back and type your first message.</div>`;
        }
    }

    if (info.kind === "login") return loginCardHtml(info, name, kicker);

    const controls = answerControlsHtml(info, descriptions, fullLabels);
    const hint = !onScreen && info.kind !== "startup" ? `<span class="cni-hint">Answer in the terminal</span>` : "";
    return `<div class="cni-head"><span class="cni-dot" aria-hidden="true"></span><span class="cni-kicker">${kicker}</span></div>`
        + (question ? `<div class="cni-question">${escapeHtml(question)}</div>` : "")
        + context
        + reviewHtml(info)
        + controls.body
        + `<div class="cni-foot"><button type="button" class="cni-link" data-on-click="terminal-view-open">Open terminal</button>${hint}<span class="cni-foot-spacer"></span>${controls.foot}</div>`;
}

// The sign-in card (#3830): the login happens in the agent's terminal (a browser link and a code
// to paste back, or /login), so the card's one action opens it. reason is the CLI's own message.
function loginCardHtml(info, name, kicker) {
    const reason = info.reason ? `<div class="cni-context-text cni-login-reason">${escapeHtml(info.reason)}</div>` : "";
    return `<div class="cni-head"><span class="cni-dot" aria-hidden="true"></span><span class="cni-kicker">${kicker}</span></div>`
        + `<div class="cni-question">${escapeHtml(name)} needs you to sign in</div>`
        + reason
        + `<div class="cni-context-text">Sign in from the terminal. Nothing runs until you do.</div>`
        + `<div class="cni-foot"><button type="button" class="btn btn-sm btn-primary cni-login-open" data-on-click="terminal-view-open">Open terminal</button></div>`;
}

// The prompt cards Coral itself made (#3622 F1). Only a control inside one of these, and outside
// any agent-written part of it (a plan is the agent's Markdown), can answer a prompt: rendered
// content can never answer for the Operator, whatever it looks like.
const ownPromptCards = new WeakSet();

/** The Coral prompt card that owns this answer control, or null when the control isn't Coral's. */
export function ownPromptCard(control) {
    const card = control.closest(".chat-needs-input");
    if (!card || !ownPromptCards.has(card) || card.parentElement?.id !== "live-history-messages") return null;
    if (control.closest(".message-text, .cni-plan")) return null;
    return card;
}

// What the card does that a screen reader should say (a box ticked, an answer sent): one polite live region for the page, made with the first card. The card itself is an alert, read out
// whole when it appears; a box that is ticked changes in place (below) so that it is not read out whole again, and this says the change.
let _live = null, _liveFlip = false;
function promptLive() {
    if (_live && _live.isConnected) return _live;
    _live = document.createElement("div");
    _live.id = "coral-prompt-live"; _live.className = "sr-only";
    _live.setAttribute("role", "status"); _live.setAttribute("aria-live", "polite"); _live.setAttribute("aria-atomic", "true");
    document.body.appendChild(_live);
    return _live;
}
/** Say `message` to a screen reader, once. */
export function announcePrompt(message) {
    _liveFlip = !_liveFlip;
    promptLive().textContent = message + (_liveFlip ? "" : "\u00a0"); // the same words twice are still a change
}

// The waiting pill over the chat says what the card in the chat says, so it is not drawn beside it while the card is on screen; scrolled away from the card (up in a long chat) the pill is the
// cue again (#5895 review). The capture wrapper carries "card-in-view" while at least a tenth of the card is on screen; the stylesheet hides the pill on that class. A card taken out of the page
// is not on screen, so the class goes with it.
let _cardWatch = null; // { el, observer }
function watchCardInView(el) {
    const wrap = document.getElementById("capture-wrapper");
    if (!wrap) return;
    if (_cardWatch && _cardWatch.el === el) return;
    unwatchCardInView();
    if (typeof IntersectionObserver === "undefined") { wrap.classList.add("card-in-view"); _cardWatch = { el, observer: null }; return; } // cannot tell: keep one message, the card
    const observer = new IntersectionObserver(entries => {
        const last = entries[entries.length - 1];
        if (last) wrap.classList.toggle("card-in-view", last.intersectionRatio >= 0.1);
    }, { threshold: [0, 0.1] });
    observer.observe(el);
    _cardWatch = { el, observer };
}
function unwatchCardInView() {
    if (!_cardWatch) return;
    if (_cardWatch.observer) _cardWatch.observer.disconnect();
    _cardWatch = null;
    const wrap = document.getElementById("capture-wrapper");
    if (wrap) wrap.classList.remove("card-in-view");
}

export function syncNeedsInputCard(container, session) {
    const info = needsInputInfo(session);
    let el = container.querySelector(":scope > .chat-needs-input");
    if (!info) {
        if (el) el.remove();
        unwatchCardInView();
        return;
    }
    const key = JSON.stringify(info);
    const shape = JSON.stringify(info, (k, v) => (k === "checked" ? undefined : v)); // the card but for its boxes' marks
    promptLive();
    if (!el) {
        el = document.createElement("div");
        el.className = "chat-needs-input";
        el.setAttribute("role", "alert");
        ownPromptCards.add(el);
    }
    if (el.dataset.key !== key && el._shape === shape && info.screen && info.screen.multi) {
        // Only a box's mark changed (a multi-select toggle): change it in place, so the control that has focus keeps it and the alert is not read out again.
        for (const o of info.screen.options || []) {
            const b = el.querySelector(`.cni-check[data-n="${Number(o.n)}"]`);
            if (!b) continue;
            b.classList.toggle("is-checked", !!o.checked);
            b.setAttribute("aria-checked", o.checked ? "true" : "false");
            const box = b.querySelector(".cni-box");
            if (box) box.textContent = o.checked ? "\u2714" : "";
        }
        el.dataset.key = key;
    } else if (el.dataset.key !== key) {
        // Keep an answer being typed if the card re-renders
        const open = Array.from(el.querySelectorAll(".cni-text-form")).find(f => f.querySelector("input").value || document.activeElement === f.querySelector("input"));
        const typing = open ? { n: open.dataset.n, value: open.querySelector("input").value, focused: document.activeElement === open.querySelector("input") } : null;
        el.dataset.key = key; el._shape = shape;
        el.innerHTML = needsInputHtml(info, session);
        const form = typing && el.querySelector(`.cni-text-form[data-n="${typing.n}"]`);
        if (form) {
            const input = form.querySelector("input");
            input.value = typing.value;
            if (typing.focused) input.focus();
        }
    }
    // Same slot as the Working row: below Sent messages, above Queued ones
    const queued = container.querySelector(":scope > .pending-messages.pending-queued");
    if (queued) {
        if (el.nextElementSibling !== queued) container.insertBefore(el, queued);
    } else if (container.lastElementChild !== el) {
        container.appendChild(el);
    }
    watchCardInView(el);
}
