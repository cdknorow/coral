/* Live history view — renders JSONL messages as a read-only conversation log */

import { syncHooksComposer } from './hooks_review.js';
import { state } from './state.js';
import { escapeHtml, showToast, copyText } from './utils.js';
// Load order only (#4996): the imports below with no names are loaded for what the module registers at load, in the order this module imported them before the split (the startup-sequence golden records it).
import './actions.js';
import { platform } from './platform/detect.js';
import { fitTerminal, refreshTerminal } from './xterm_renderer.js';
import { loadXterm, xtermReady } from './vendor_loader.js';
import { fireAgentView } from './agent_view.js';
import './render.js';
import { chatAgentAdapter } from './chat_agents.js';
import { serverFetch, sessionServer, isCloud } from './server_base.js';
import './server_status.js';
import './terminal_gone.js';
import { pollEvery, isPaused, afterPause, isBusyStatus, busyRetryingText } from './overload.js';
import './local_store.js';
import { htmlToElement, rollUpGroups, updateWorkGroupSummary } from './chat_content.js';
import { flashCopied, makeBubble } from './chat_bubbles.js';
import './saved_marks.js';
import './pasted_content.js';
import { CHAT_TRIM, FIRST_PAGE, takeChatPrefetch } from './chat_page.js';
import { loginBySession, ownPromptCard, pendingToolBySession, promptOptionsBySession, syncNeedsInputCard } from './needs_input_card.js';
import { INTERRUPT_RE, noteAgentActivity, noteInterrupt, rememberUserMessage, syncPendingBubbles, syncWorkingIndicator } from './pending_messages.js';
import { browserLocal } from './safe_storage.js';
import './chat_thumbs.js';
import { renderToolCard } from './chat_tool_cards.js';
import { bubbleSource, renderMessage, renderMessagesChunked } from './chat_messages.js';
import { freshKeptPage, keepFirstPage, keepMore, keptPages } from './chat_kept_pages.js';
import { noteUnseenChat, syncJumpToLatest } from './chat_jump_latest.js';
import { sendPromptAnswer, syncEmptyState } from './chat_prompt_empty.js';
import { autosizeCommandInput } from './command_autosize.js';
import { noteReplyText, forgetReplies } from './chat_announce.js';
export { clearKeptChatPages } from './chat_kept_pages.js';
export { renderTranscript } from './chat_messages.js';
export { initCommandInputAutosize } from './command_autosize.js';
export { addPendingMessage, confirmPendingMessage, loadPendingFromStorage, removePendingMessage } from './pending_messages.js';
export { linkifyRefs } from './chat_bubbles.js';

let historyPollInterval = null;
let historyMessageCount = 0;
let historyStart = 0;       // absolute index of the oldest rendered message (keyset cursor)
let historyHasMore = false;  // whether older messages exist
let initialLoadDone = false; // whether the initial full load has completed
let loadingMore = false;     // an older page is being fetched





// Messages per page. Tool calls and results each count as one and are folded
// away in the chat, so a page covers fewer turns than its size suggests. The
// first page is small so a switch paints quickly (the payload crosses the hub
// leg); older pages load as the reader scrolls toward the top. 30 messages fill
// the view and are about a third of the 100 it was (#3723: 22 to 61 KB against
// 68 to 170 KB on 100 MB transcripts).
const OLDER_PAGE = 200;
// Pages carry at most CHAT_TRIM bytes of each long tool field (an output, a
// written file, an edit): they are folded away, and a card loads its message in
// full when opened (loadTrimmedCard).
// Bumped on reset so responses for a previous session are dropped, and used to
// keep a single refresh in flight: overlapping initial loads would otherwise
// both render the full history.
let historyGeneration = 0;
let keptRenderGeneration = -1; // the generation whose kept page is being built (refreshLiveHistory)
let refreshInFlight = -1;
let activeChatAbortController = null;
let activeLoadMoreAbortController = null;
// The query every chat request starts from: which session, and where it is.
function chatParams(session) {
    const params = new URLSearchParams();
    params.set("session_id", session.session_id);
    if (session.agent_type) {
        params.set("agent_type", session.agent_type);
    }
    // Through the hub the machine resolves the directory from session_id (its own stored record), and the hub's route
    // table refuses an absolute path in a file-ref parameter: sending it would turn the chat into a 404.
    if (session.working_directory && !(isCloud() && sessionServer(session) !== 'local')) {
        params.set("working_directory", session.working_directory);
    }
    return params;
}

// A trimmed message (#3723) keeps its absolute index, so its card can load it in full.
function noteIndexes(messages, start) {
    messages.forEach((m, i) => { if (m && m.trimmed) m._index = start + i; });
}

// One message, whole: the page of one that ends after it, without trim.
async function fetchChatMessage(session, index) {
    const params = chatParams(session);
    params.set("after", "0");
    params.set("limit", "1");
    params.set("before", String(index + 1));
    const resp = await serverFetch(sessionServer(session), `/api/sessions/live/${encodeURIComponent(session.name)}/chat?${params}`);
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    const data = await resp.json();
    return (data && Array.isArray(data.messages) && data.messages[0]) || null;
}

// Opening a card whose tool or output was shortened loads its message in full and shows that in
// the card. Checked by tool_use_id, so a transcript that changed underneath shows nothing wrong:
// the card then says its text was shortened.
async function loadTrimmedCard(card) {
    const session = state.currentSession;
    if (!session || card.dataset.trimLoading) return;
    card.dataset.trimLoading = "1";
    const generation = historyGeneration;
    try {
        const toolUseId = card.dataset.toolUseId || "";
        if (card.dataset.trimUse !== undefined) {
            const msg = await fetchChatMessage(session, Number(card.dataset.trimUse));
            if (generation !== historyGeneration) return;
            const tool = (msg && Array.isArray(msg.tool_uses) ? msg.tool_uses : []).find(t => (t.tool_use_id || "") === toolUseId);
            if (!tool || tool.trimmed) throw new Error("the message changed");
            const fresh = htmlToElement(renderToolCard(tool, chatAgentAdapter(session.agent_type)));
            const body = card.querySelector(":scope > .tool-call-body");
            const outputs = [...body.querySelectorAll(":scope > .tool-card-output")];
            body.replaceChildren(...fresh.querySelector(".tool-call-body").childNodes, ...outputs);
            delete card.dataset.trimUse;
        }
        if (card.dataset.trimResult !== undefined) {
            const msg = await fetchChatMessage(session, Number(card.dataset.trimResult));
            if (generation !== historyGeneration) return;
            if (!msg || msg.type !== "tool_result" || (msg.tool_use_id || "") !== (card.dataset.trimResultId || "") || msg.trimmed) {
                throw new Error("the message changed");
            }
            const pre = card.querySelector(".tool-card-output > pre");
            if (pre) pre.textContent = msg.content || "";
            delete card.dataset.trimResult;
            delete card.dataset.trimResultId;
        }
    } catch (e) {
        if (generation !== historyGeneration) return;
        const body = card.querySelector(":scope > .tool-call-body");
        if (body && !body.querySelector(":scope > .tool-card-trim-note")) {
            body.insertAdjacentHTML("beforeend", `<div class="tool-card-trim-note">Shortened; the full text couldn't be loaded (${escapeHtml(e.message || "error")}).</div>`);
        }
    } finally {
        delete card.dataset.trimLoading;
    }
}

// toggle doesn't bubble: caught on the way down.
document.addEventListener("toggle", (e) => {
    const card = e.target;
    if (!card || !card.open || !card.classList || !card.classList.contains("tool-call")) return;
    if (card.dataset.trimUse !== undefined || card.dataset.trimResult !== undefined) loadTrimmedCard(card);
}, true);










// Join a prepended older page to the page after it: a run of tool calls split by the page
// boundary is one group again. A reply at the end of the older page stays a reply (#4105).
export function stitchPages(lastOlder) {
    let next = lastOlder && lastOlder.nextElementSibling;
    // The page after may open with a roll-up bar in front of its run: the group the tool run continues into is the bar's first member
    // (rollUpGroups then makes one bar of the run again).
    if (next && next.classList.contains("work-rollup")) next = next.nextElementSibling;
    if (!next || !next.classList.contains("work-group")) return;
    const nextBody = next.querySelector(":scope > .work-group-body");
    if (lastOlder && lastOlder.classList.contains("work-group")) {
        const olderBody = lastOlder.querySelector(":scope > .work-group-body");
        while (nextBody.firstChild) olderBody.appendChild(nextBody.firstChild);
        next.remove();
        updateWorkGroupSummary(lastOlder);
    } else {
        updateWorkGroupSummary(next);
    }
}


document.addEventListener("click", async (e) => {
    const btn = e.target.closest && e.target.closest(".chat-copy-btn");
    if (!btn) return;
    const bubble = btn.closest(".chat-bubble");
    const text = bubbleSource.get(bubble) ?? bubble.querySelector(".message-text")?.innerText ?? "";
    if (!(await copyText(text))) {
        showToast("Could not copy", true);
        return;
    }
    flashCopied(btn);
});





function syncUsageLimitNotice(container, notice) {
    const existing = container.querySelector('.chat-usage-limit-notice');
    if (!notice) {
        existing?.remove();
        return;
    }
    if (existing) {
        existing.querySelector('.message-text').textContent = notice;
        return;
    }
    const bubble = makeBubble('chat-note chat-usage-limit-notice', `<span class="material-icons" aria-hidden="true">warning</span><span class="message-text">${escapeHtml(notice)}</span>`);
    container.append(bubble);
}



/** Shows the kept page of an agent seen a moment ago (built off screen in slices). True when the agent was switched while it was built: the refresh ends. */
async function _showKeptPage(session, container, kept) {
    // Built off screen in slices; a poll that comes while it builds returns at once (keptRenderGeneration).
    const generation = historyGeneration;
    keptRenderGeneration = generation;
    const built = document.createElement("div");
    let rebuilt = false;
    try {
        rebuilt = await renderMessagesChunked(kept.messages, built, session.agent_type, () => generation === historyGeneration);
    } catch (e) {
        // A failed build must not leave the chat blank or the guard set: forget the kept page, and the first page is
        // loaded below as for an agent not seen before.
        console.error("[coral] the kept chat page could not be rebuilt", e);
        keptPages.delete(session.session_id);
    } finally {
        if (!rebuilt && keptRenderGeneration === generation) keptRenderGeneration = -1;
    }
    if (generation !== historyGeneration) return true; // the agent was switched while it built
    if (rebuilt) {
        container.innerHTML = "";
        while (built.firstChild) container.appendChild(built.firstChild);
        historyHasMore = kept.start > 0;
        historyStart = kept.start;
        historyMessageCount = kept.total;
        _updateLoadMoreButton(container);
        initialLoadDone = true;
        if (state.autoScroll) container.scrollTop = container.scrollHeight;
    }
    return false;
}

/** What to ask for: the first page, or only what came after the last message held. */
function _historyParams(session) {
    const params = chatParams(session);

    if (!initialLoadDone) {
        // First load: fetch most recent messages with pagination
        params.set("after", "0");
        params.set("limit", String(FIRST_PAGE));
        params.set("offset", "0");
        params.set("trim", String(CHAT_TRIM));
    } else {
        // Subsequent polls: only fetch new messages
        params.set("after", historyMessageCount);
    }
    return params;
}

/** The reply to the request: the first page of a deep-linked chat asked for at page load if it is the same request, else a new one. */
async function _fetchHistoryPage(session, params, server, abortCtrl) {
    // The first page of a deep-linked chat was asked for when the page loaded, before the list named the agent (chat_page.js): when it is the request
    // this would make, take it over; if it failed or was refused, make the request as usual. Its abort is this load's: a switch cancels it too.
    let resp = null;
    const early = initialLoadDone ? null : takeChatPrefetch(session, params, { local: server === 'local', cloud: isCloud() });
    if (early) {
        abortCtrl.signal.addEventListener('abort', early.abort, { once: true });
        try { resp = await early.promise; } catch { resp = null; }
        if (resp && !resp.ok) resp = null; // a refusal (a 401 above all) is the real request's to meet, with the page's own handling
    }
    if (!resp) resp = await serverFetch(sessionServer(session), `/api/sessions/live/${encodeURIComponent(session.name)}/chat?${params}`, {
        signal: abortCtrl.signal,
    });
    return resp;
}

/** What the reply carries beyond the messages: the pending tool call, the options of an open prompt, a sign-in the CLI waits on. */
function _noteReplyState(session, data) {
    // What the server says about an open prompt rides on this reply (#3720): the pending tool call, and the options
    // of a prompt that may be open on the terminal.
    pendingToolBySession.set(session.session_id, data.pending || null);
    const po = data.prompt_options;
    if (po) promptOptionsBySession.set(session.session_id, { question: po.question || "", options: po.options || [], review: po.review || [], multi: !!po.multi, submit: po.submit || null });
    else promptOptionsBySession.delete(session.session_id);
    // A sign-in the CLI waits on (#3830): the reply is fresher than the session row.
    if (typeof data.needs_login === "boolean") loginBySession.set(session.session_id, data.needs_login ? { reason: String(data.login_reason || "") } : null);
    syncHooksComposer(session); // Codex's hooks question is in the box's words while it is open (#6409)
}

/** The messages of the reply: the first page, or the new ones at the bottom, and what each says about the agent. */
function _renderHistoryReply(container, session, data) {
    if (data.messages && data.messages.length > 0) {
        if (!initialLoadDone) {
            container.innerHTML = "";
            noteIndexes(data.messages, data.start);
            // Initial load — render all and add "Load More" button if needed
            for (const msg of data.messages) {
                renderMessage(msg, container, session.agent_type);
            }
            historyHasMore = data.has_more || false;
            historyStart = data.start;
            _updateLoadMoreButton(container);
            initialLoadDone = true;
            keepFirstPage(session.session_id, data.messages, data.total, historyStart);
        } else {
            // Poll — append new messages at the bottom.
            noteUnseenChat(data.messages); // read higher up: counted on the jump-to-latest button
            for (const msg of data.messages) {
                renderMessage(msg, container, session.agent_type);
                if (msg.type === "assistant") noteReplyText(session, msg); // said once when the turn ends (chat_announce.js)
            }
            keepMore(session.session_id, data.messages, data.total);
        }
        for (const msg of data.messages) {
            if (msg.type === "user" && INTERRUPT_RE.test(String(msg.content || "").trim())) noteInterrupt(session.session_id, msg);
            else if (msg.type === "user") rememberUserMessage(session.session_id, msg);
            else noteAgentActivity(session.session_id, msg);
        }
        historyMessageCount = data.total;
    } else if (!initialLoadDone) {
        container.innerHTML = "";
        historyHasMore = false;
        initialLoadDone = true;
        _updateLoadMoreButton(container);
    }
}

/** After a reply: the usage notice, the kept page's age, the pending bubbles, the working row, the empty state and the scroll. */
function _syncAfterHistory(container, session, data) {
    syncUsageLimitNotice(container, data.limit_notice || '');
    // The agent is still on screen: its kept page is as fresh as this reply, so the time limit counts from when it left.
    const keptNow = keptPages.get(session.session_id);
    if (keptNow) keptNow.at = Date.now();

    // Pending first: it settles its place at the bottom, then the working row goes above it
    syncPendingBubbles(container, session.session_id);
    syncNeedsInputCard(container, session);
    syncWorkingIndicator(container, session);
    syncEmptyState(container);
    if (state.autoScroll) {
        container.scrollTop = container.scrollHeight;
    }
    syncJumpToLatest();
}

/** The first load failed: say so, with Retry. */
function _showHistoryError(container) {
    container.innerHTML = `<div class="chat-load-error" role="alert">
                <span class="chat-error-message">Could not load this conversation. Reload the page to try again.</span>
                <button type="button" class="btn btn-secondary btn-small chat-retry-btn">Retry</button>
            </div>`;
    const retryBtn = container.querySelector('.chat-retry-btn');
    if (retryBtn) {
        retryBtn.onclick = () => {
            container.innerHTML = '<div class="loading-indicator" role="status">Loading conversation…</div>';
            refreshInFlight = -1;
            refreshLiveHistory();
        };
    }
}

export async function refreshLiveHistory() {
    if (!state.currentSession || state.currentSession.type !== "live") return;
    if (!platform.isNative && document.hidden) return;

    const session = state.currentSession;
    if (!session.session_id) return;

    const container = document.getElementById("live-history-messages");
    if (!container) return;

    // An agent seen in the last few minutes: its kept page shows at once, and only what came after is asked for
    // below (a poll from the page's end), not the first page again (#3726).
    if (!initialLoadDone && keptRenderGeneration !== historyGeneration) {
        const kept = freshKeptPage(session.session_id);
        if (kept && await _showKeptPage(session, container, kept)) return;
    }

    // Show loading indicator on first load
    if (!initialLoadDone && !container.querySelector('.loading-indicator') && !container.querySelector('.chat-load-error') && container.children.length === 0) {
        container.innerHTML = '<div class="loading-indicator" role="status">Loading conversation…</div>';
    }

    const params = _historyParams(session);

    if (refreshInFlight === historyGeneration) return;
    const generation = historyGeneration;
    const server = sessionServer(session);
    // The first load during a pause (#3430): the server would refuse it as busy. Say so, and load
    // the moment the pause ends rather than a poll later.
    if (!initialLoadDone && isPaused(server === 'local' ? '' : server)) {
        _showHistoryBusy(container, null, server, generation);
        return;
    }
    refreshInFlight = generation;

    if (activeChatAbortController) {
        activeChatAbortController.abort();
    }
    const abortCtrl = new AbortController();
    activeChatAbortController = abortCtrl;

    try {
        const resp = await _fetchHistoryPage(session, params, server, abortCtrl);
        if (generation !== historyGeneration) return;

        // The first load refused as busy (#3430): not an error. Show that it is retrying, and retry
        // once the pause the answer started has run out.
        if (!resp.ok && !initialLoadDone && isBusyStatus(resp.status)) {
            _showHistoryBusy(container, resp, server, generation);
            return;
        }

        if (!resp.ok) {
            let errorText = `HTTP ${resp.status}`;
            try {
                const errData = await resp.json();
                if (errData && errData.error) errorText = errData.error;
            } catch (_) {}
            throw new Error(errorText);
        }

        let data;
        try {
            data = await resp.json();
        } catch (err) {
            // A reply that isn't JSON says so, rather than only a parser's position (#3694).
            throw new Error(`the reply from the server could not be read: ${err.message}`);
        }
        if (generation !== historyGeneration) return;

        // The transcript is shorter than what this client holds (it was cut or replaced while the agent was away, or a kept
        // page outlived it): forget what is shown and load it again from its start.
        if (initialLoadDone && data.total < historyMessageCount) {
            keptPages.delete(session.session_id);
            container.innerHTML = "";
            historyMessageCount = 0;
            historyStart = 0;
            historyHasMore = false;
            initialLoadDone = false;
            setTimeout(refreshLiveHistory, 0); // after this refresh has ended
            return;
        }

        _noteReplyState(session, data);

        _renderHistoryReply(container, session, data);
        _syncAfterHistory(container, session, data);
    } catch (e) {
        if (e.name === 'AbortError') {
            return;
        }
        console.error("Failed to refresh live history:", e);
        if (generation === historyGeneration && !initialLoadDone) {
            _showHistoryError(container);
        }
    } finally {
        if (activeChatAbortController === abortCtrl) {
            activeChatAbortController = null;
        }
        if (refreshInFlight === generation) refreshInFlight = -1;
    }
}

// A first history load refused as busy, or due during a pause: "Loading… (Coral Hub busy,
// retrying)" in place of the loading line, and the load again as soon as the pause is over, for
// the same agent only. One retry is queued at a time; the 1 s poll covers it too.
let _historyBusyRetry = null;
function _showHistoryBusy(container, resp, server, generation) {
    let el = container.querySelector(':scope > .loading-indicator');
    if (!el) {
        container.innerHTML = '<div class="loading-indicator"></div>';
        el = container.querySelector(':scope > .loading-indicator');
    }
    el.classList.add('chat-history-busy');
    el.setAttribute('role', 'status');
    el.textContent = busyRetryingText(resp, { machine: server, hub: isCloud() });
    if (_historyBusyRetry) clearTimeout(_historyBusyRetry);
    _historyBusyRetry = afterPause(() => {
        _historyBusyRetry = null;
        if (generation === historyGeneration && !initialLoadDone) refreshLiveHistory();
    }, server);
}

/** Load older messages (prepend above current messages). */
/** True while older transcript pages remain unloaded (used by find-in-chat). */
export function chatHasOlder() { return historyHasMore; }

export async function loadMoreHistory() {
    if (!state.currentSession || !historyHasMore || loadingMore) return;
    loadingMore = true;
    const generation = historyGeneration;
    let ok = false;
    try {
        ok = await _loadMoreHistory(generation);
    } finally {
        if (generation === historyGeneration) loadingMore = false;
    }
    // A page shorter than the view leaves the sentinel in sight, and the
    // observer only reports changes: keep loading while it is still within
    // reach. Measured, because the observer's entry for the prepend has not
    // been delivered yet.
    if (ok && generation === historyGeneration && historyHasMore && sentinelInReach()) {
        loadMoreHistory();
    }
    // Re-arm the observer: observe() reports the current state, so a reader
    // who left and came back to the top while the page loaded is not stuck.
    if (ok && generation === historyGeneration && sentinelObserver) {
        const btn = document.querySelector("#live-history-messages > .load-more-btn");
        if (btn) {
            sentinelObserver.unobserve(btn);
            sentinelObserver.observe(btn);
        }
    }
}

function sentinelInReach() {
    const container = document.getElementById("live-history-messages");
    const btn = container && container.querySelector(":scope > .load-more-btn");
    if (!btn || !container.clientHeight) return false;
    return btn.getBoundingClientRect().bottom >= container.getBoundingClientRect().top - AUTO_LOAD_PX;
}

/** Save for Later's jump (#4510): page up until the message with this key (whose index in the transcript is index) is on screen, then show it:
 *  open the steps it sits in, scroll it to the middle, mark it and give it the focus. Resolves false when it is not there. */
export async function jumpToLiveMessage(key, index) {
    const container = document.getElementById("live-history-messages");
    if (!container) return false;
    const find = () => container.querySelector(`[data-msg-key="${CSS.escape(key)}"]`);
    const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
    for (let i = 0; i < 80 && !(initialLoadDone && container.querySelector("[data-msg-key]")); i++) await pause(100); // the agent's newest page first
    for (let guard = 0; guard < 400 && !find() && historyHasMore && historyStart > index; guard++) {
        if (loadingMore) { await pause(50); continue; }
        const generation = historyGeneration;
        loadingMore = true;
        try {
            if (!(await _loadMoreHistory(generation))) break;
        } finally {
            if (generation === historyGeneration) loadingMore = false;
        }
    }
    const target = find();
    if (!target) return false;
    for (let d = target.closest("details"); d; d = d.parentElement && d.parentElement.closest("details")) d.open = true;
    for (const old of container.querySelectorAll(".chat-search-target")) old.classList.remove("chat-search-target");
    target.classList.add("chat-search-target");
    target.scrollIntoView({ block: "center" });
    target.setAttribute("tabindex", "-1");
    target.focus({ preventScroll: true });
    return true;
}

async function _loadMoreHistory(generation) {

    const session = state.currentSession;
    const container = document.getElementById("live-history-messages");
    if (!container) return;

    const params = chatParams(session);
    // Pagination is anchored by the cursor; omit an explicit after=0 so
    // providers and test harnesses can distinguish load-more from initial load.
    // before= is the oldest rendered message's absolute index, so messages
    // appended since the first page do not shift the window.
    params.set("limit", String(OLDER_PAGE));
    params.set("trim", String(CHAT_TRIM));
    params.set("before", String(historyStart));
    const server = sessionServer(session);

    if (activeLoadMoreAbortController) {
        activeLoadMoreAbortController.abort();
    }
    const abortCtrl = new AbortController();
    activeLoadMoreAbortController = abortCtrl;

    try {
        const url = () => `/api/sessions/live/${encodeURIComponent(session.name)}/chat?${params}`;
        const resp = await serverFetch(server, url(), { signal: abortCtrl.signal });
        if (generation !== historyGeneration) return; // switched agents meanwhile
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const data = await resp.json();
        if (generation !== historyGeneration) return;

        if (data.messages && data.messages.length > 0) {
            const temp = document.createElement("div");
            noteIndexes(data.messages, data.start);
            if (!await renderMessagesChunked(data.messages, temp, session.agent_type, () => generation === historyGeneration)) return;

            // Preserve scroll position: measure just before inserting
            const prevScrollHeight = container.scrollHeight;
            const prevScrollTop = container.scrollTop;

            // Find the insertion point (after the Load More button)
            const loadMoreBtn = container.querySelector(".load-more-btn");
            const refNode = loadMoreBtn ? loadMoreBtn.nextSibling : container.firstChild;

            const lastOlder = temp.lastElementChild;
            while (temp.firstChild) {
                container.insertBefore(temp.firstChild, refNode);
            }
            stitchPages(lastOlder);
            rollUpGroups(container);

            historyStart = data.start;
            historyHasMore = (data.has_more || false) && historyStart > 0;

            // Restore scroll position so the view doesn't jump
            container.scrollTop = prevScrollTop + (container.scrollHeight - prevScrollHeight);
        } else {
            historyHasMore = false;
        }

        _updateLoadMoreButton(container);
        return true;
    } catch (e) {
        if (e.name === 'AbortError') return false;
        console.error("Failed to load more history:", e);
        return false;
    } finally {
        if (activeLoadMoreAbortController === abortCtrl) {
            activeLoadMoreAbortController = null;
        }
    }
}

// The button at the top of the chat is also the autoload sentinel: when it
// comes within AUTO_LOAD_PX of the visible area, the next older page loads.
// The button stays for find-in-chat and for when a load failed.
const AUTO_LOAD_PX = 200;
let sentinelObserver = null;

function observeSentinel(container, btn) {
    if (typeof IntersectionObserver === "undefined") return;
    if (sentinelObserver && sentinelObserver.root !== container) {
        sentinelObserver.disconnect();
        sentinelObserver = null;
    }
    if (!sentinelObserver) {
        sentinelObserver = new IntersectionObserver((entries) => {
            for (const e of entries) {
                if (e.isIntersecting && historyHasMore && initialLoadDone) loadMoreHistory();
            }
        }, { root: container, rootMargin: `${AUTO_LOAD_PX}px 0px 0px 0px` });
    }
    sentinelObserver.observe(btn);
}

function _updateLoadMoreButton(container) {
    let btn = container.querySelector(".load-more-btn");
    if (historyHasMore) {
        if (!btn) {
            btn = document.createElement("button");
            btn.className = "load-more-btn chat-history-sentinel";
            btn.textContent = "Load older messages";
            btn.onclick = loadMoreHistory;
            container.insertBefore(btn, container.firstChild);
            observeSentinel(container, btn);
        }
    } else if (btn) {
        if (sentinelObserver) sentinelObserver.unobserve(btn);
        btn.remove();
    }
}

// Returns the first load, so a caller can hold slower work (the git/file
// queries) until the transcript is in. The browser allows only a handful of
// connections per origin, and on a big repo those queries are seconds long.
 function startLiveHistoryPoll() {
    stopLiveHistoryPoll();
    const firstLoad = refreshLiveHistory();
    historyPollInterval = pollEvery(refreshLiveHistory, 1000, { machine: () => state.currentSession && sessionServer(state.currentSession), agentView: true });
    return firstLoad;
}

export function stopLiveHistoryPoll() {
    if (historyPollInterval) {
        clearInterval(historyPollInterval);
        historyPollInterval = null;
    }
    if (activeChatAbortController) {
        activeChatAbortController.abort();
        activeChatAbortController = null;
    }
}

// Reset transcript state when the selected live session changes. A restart
// keeps the same pane but allocates a new Coral session ID; callers can ask us
// to resume an already-running poll so the new transcript is loaded from zero.
export function resetLiveHistory({ resume = false } = {}) {
    if (!resume) forgetReplies(); // another agent: what was waiting to be said belongs to the one before
    const wasPolling = !!historyPollInterval;
    stopLiveHistoryPoll();
    // Free the connections of the previous chat's fetches right away: through
    // the hub, a switch otherwise queues behind them.
    if (activeLoadMoreAbortController) {
        activeLoadMoreAbortController.abort();
        activeLoadMoreAbortController = null;
    }
    if (activeChatAbortController) {
        activeChatAbortController.abort();
        activeChatAbortController = null;
    }
    if (sentinelObserver) {
        sentinelObserver.disconnect();
        sentinelObserver = null;
    }
    const container = document.getElementById("live-history-messages");
    if (container) container.innerHTML = "";
    historyGeneration++;
    refreshInFlight = -1;
    // A new chat opens at the bottom. Only scroll events set autoScroll, and
    // the one emptying the container fires after the first page renders.
    state.autoScroll = true;
    syncJumpToLatest(); // the emptied chat is at its bottom: no button, no count from the agent before
    historyMessageCount = 0;
    historyStart = 0;
    historyHasMore = false;
    initialLoadDone = false;
    loadingMore = false;
    if (resume) {
        if (wasPolling) startLiveHistoryPoll();
    }
}

// ── Center view mode: terminal vs. chat ─────────────────────────────────
// The transcript renders in the center pane in place of the terminal. The
// terminal keeps running underneath (hidden) so switching back is instant.

const VIEW_MODE_KEY = "coral-live-view-mode";

/** The chosen center view; Chat unless the user picked Terminal. */
export function getLiveViewMode() {
    try {
        return browserLocal.get(VIEW_MODE_KEY) === "terminal" ? "terminal" : "chat";
    } catch {
        return "chat";
    }
}

// Plain terminals have no transcript, so they always show the terminal.
function hasTranscript(session) {
    return !!session && session.agent_type !== "terminal";
}

/** The view the selected agent shows in the centre: "chat" or "terminal". A plain terminal has no transcript,
 *  so it is always the terminal; otherwise the persisted mode, or a one-off override. */
// "Open terminal" on the needs-input card shows the terminal for this agent without changing the saved choice (#4711). The terminal
// attaches only for a view that wants it (sessions.js asks effectiveLiveViewMode), so the view that is showing has to be what is asked:
// until the next agent is selected, or a view is chosen with the toggle, the terminal is the view.
let _terminalOverride = false;
export function clearViewOverride() { _terminalOverride = false; }
export function effectiveLiveViewMode(override) {
    return hasTranscript(state.currentSession) ? (override || (_terminalOverride ? "terminal" : getLiveViewMode())) : "terminal";
}

/** Apply the persisted mode (or a one-off override) to the DOM and start/stop the transcript poll. */
export function applyLiveViewMode(override) {
    const mode = effectiveLiveViewMode(override);
    const canChat = hasTranscript(state.currentSession);
    const toggle = document.querySelector(".live-view-toggle");
    if (toggle) toggle.hidden = !canChat;
    const findBtn = document.getElementById("chat-find-btn");
    if (findBtn) findBtn.hidden = mode !== "chat";
    if (mode !== "chat") window.closeChatFind?.();
    const wrapper = document.getElementById("capture-wrapper");
    if (wrapper) wrapper.classList.toggle("chat-mode", mode === "chat");
    // The command pane drops terminal-only controls in chat mode
    const pane = document.getElementById("command-pane");
    if (pane) pane.classList.toggle("chat-mode", mode === "chat");
    autosizeCommandInput();
    for (const m of ["terminal", "chat"]) {
        const btn = document.getElementById(`live-view-btn-${m}`);
        if (!btn) continue;
        btn.classList.toggle("active", m === mode);
        btn.setAttribute("aria-pressed", String(m === mode));
    }
    if (mode === "chat") {
        // The terminal is not shown: its socket closes (sessions.js), and opens again when it is.
        fireAgentView("coral:terminal-unwanted");
        scheduleTerminalPrefetch();
        return startLiveHistoryPoll();
    } else {
        stopLiveHistoryPoll();
        // The terminal is shown: it attaches now if it has not (sessions.js; no socket or xterm.js in Chat view).
        fireAgentView("coral:terminal-wanted");
        // The xterm canvas was hidden; refit now that it has a size again, and repaint it: a renderer that was hidden may not
        // draw again until something is written, and an idle agent writes nothing (#4518).
        setTimeout(fitTerminal, 0);
        requestAnimationFrame(() => requestAnimationFrame(() => { fitTerminal(); refreshTerminal(); }));
    }
    return Promise.resolve();
}

// xterm.js (456 KB with its add-ons) is only downloaded when the Terminal view is first shown (#3083), and that wait, on a slow link
// or through Coral Hub, was the blank pane after the switch (#4518: about a second at 1.5 Mbit and 150 ms; measured). So it is fetched
// ahead: on the first sign that the Terminal button is about to be used (pointer over it, touch, focus), and when the Chat view has been
// showing for terminalPrefetch.idleMs on a link that is not metered or slow. It loads once; every later switch finds it ready.
export const terminalPrefetch = { idleMs: 2000 };
let _prefetchTimer = null;
function prefetchTerminal() {
    if (xtermReady()) return;
    loadXterm().catch(() => { /* a failed prefetch is retried by the switch itself, which shows its own Retry */ });
}
function slowOrMetered() {
    const c = navigator.connection;
    return !!c && (c.saveData === true || /(^|-)2g$/.test(c.effectiveType || ""));
}
function scheduleTerminalPrefetch() {
    if (xtermReady() || _prefetchTimer || slowOrMetered() || !hasTranscript(state.currentSession)) return;
    _prefetchTimer = setTimeout(() => {
        _prefetchTimer = null;
        if (effectiveLiveViewMode() === "chat" && !slowOrMetered()) prefetchTerminal();
    }, terminalPrefetch.idleMs);
}
// The intent: pointer over, touch or keyboard focus on the Terminal button (it exists in the page from the start).
for (const type of ["pointerover", "touchstart", "focusin"]) {
    document.addEventListener(type, (e) => {
        if (e.target instanceof Element && e.target.closest("#live-view-btn-terminal, [data-on-click='terminal-view-open']")) prefetchTerminal();
    }, { capture: true, passive: true });
}







export function setLiveViewMode(mode) {
    _terminalOverride = false;
    try {
        browserLocal.set(VIEW_MODE_KEY, mode === "chat" ? "chat" : "terminal");
    } catch { /* storage unavailable — mode lasts for this page only */ }
    applyLiveViewMode();
    if (mode === "chat") {
        const container = document.getElementById("live-history-messages");
        if (container) container.scrollTop = container.scrollHeight;
        state.autoScroll = true;
        syncJumpToLatest();
    }
}

/** Show the terminal for this agent without changing the saved Chat/Terminal choice. */
export function showTerminalView() {
    _terminalOverride = true;
    applyLiveViewMode("terminal");
}





document.addEventListener("click", (e) => {
    const btn = e.target.closest && e.target.closest("#live-history-messages .cni-answer");
    if (!btn || btn.disabled || btn.nodeName !== "BUTTON") return;
    const card = ownPromptCard(btn);
    if (!card) return;
    btn.classList.add("sending");
    sendPromptAnswer(card, Number(btn.dataset.n), btn.dataset.label, btn.dataset.action).then(ok => {
        if (!ok) btn.classList.remove("sending");
    });
});

document.addEventListener("submit", (e) => {
    const form = e.target.closest && e.target.closest("#live-history-messages .cni-text-form");
    if (!form) return;
    e.preventDefault();
    if (!ownPromptCard(form)) return;
    const input = form.querySelector("input");
    const text = input.value.trim();
    if (!text) {
        input.focus();
        return;
    }
    sendPromptAnswer(ownPromptCard(form), Number(form.dataset.n), form.dataset.label, "text", text);
});







