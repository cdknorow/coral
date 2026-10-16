/* The history transcript in pages (#3718). Through Coral Hub a JSON reply is held whole to be
 * checked for secrets, and one over 2 MiB is refused, so a long transcript can't come in one
 * reply: the view loads the newest HISTORY_PAGE messages (a page of 100 measured at most 193 KB)
 * and loads more as asked, "Load earlier messages" above and, when a search hit opened a page in
 * the middle, "Load later messages" below. */
import { loadHistoryMessages } from './api.js';
import { renderHistoryNodes } from './render.js';
import { registerActions } from './actions.js';

export const HISTORY_PAGE = 100;

// The window shown: [lo, hi) of total messages, for one session.
let win = null;

function box() { return document.getElementById('history-messages'); }

/** Record what renderHistoryChat just drew: messages lo..hi of total. */
export function setHistoryWindow(sessionId, agentType, lo, hi, total) {
    win = { sessionId, agentType, lo, hi, total, busy: false };
    syncButtons();
}

function syncButtons() {
    const c = box();
    if (!c || !win) return;
    let earlier = c.querySelector(':scope > .history-load-earlier');
    if (win.lo > 0 && !earlier) c.insertAdjacentHTML('afterbegin', '<button type="button" class="load-more-btn history-load-earlier" data-on-click="history-load-earlier">Load earlier messages</button>');
    else if (win.lo <= 0 && earlier) earlier.remove();
    let later = c.querySelector(':scope > .history-load-later');
    if (win.hi < win.total && !later) c.insertAdjacentHTML('beforeend', '<button type="button" class="load-more-btn history-load-later" data-on-click="history-load-later">Load later messages</button>');
    else if (win.hi >= win.total && later) later.remove();
}

async function loadEarlier() {
    const w = win;
    if (!w || w.busy || w.lo <= 0) return;
    w.busy = true;
    try {
        const limit = Math.min(HISTORY_PAGE, w.lo);
        const data = await loadHistoryMessages(w.sessionId, { limit, offset: w.total - w.lo });
        if (win !== w || !data || !Array.isArray(data.messages) || !data.messages.length) return;
        const c = box();
        const prevHeight = c.scrollHeight, prevTop = c.scrollTop;
        const nodes = renderHistoryNodes(data.messages, w.agentType, w.lo - data.messages.length);
        const earlier = c.querySelector(':scope > .history-load-earlier');
        if (earlier) earlier.after(...nodes); else c.prepend(...nodes);
        w.lo -= data.messages.length;
        c.scrollTop = prevTop + (c.scrollHeight - prevHeight); // the messages read stay in place
    } finally {
        w.busy = false;
        if (win === w) syncButtons();
    }
}

async function loadLater() {
    const w = win;
    if (!w || w.busy || w.hi >= w.total) return;
    w.busy = true;
    try {
        const limit = Math.min(HISTORY_PAGE, w.total - w.hi);
        const data = await loadHistoryMessages(w.sessionId, { limit, offset: w.total - (w.hi + limit) });
        if (win !== w || !data || !Array.isArray(data.messages) || !data.messages.length) return;
        const c = box();
        const nodes = renderHistoryNodes(data.messages, w.agentType, w.hi);
        const later = c.querySelector(':scope > .history-load-later');
        if (later) later.before(...nodes); else c.append(...nodes);
        w.hi += data.messages.length;
    } finally {
        w.busy = false;
        if (win === w) syncButtons();
    }
}

registerActions({
    'history-load-earlier': () => loadEarlier(),
    'history-load-later': () => loadLater(),
});
