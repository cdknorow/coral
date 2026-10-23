'use strict';
// What a screen reader would be told (#6213): every live region in the page (aria-live other than off, role=status, role=alert, role=log) is watched, and each change of its text, and each such element that is
// added or shown with text in it, is recorded with the time, the region's name and whether it is assertive. A scene then asserts what it must announce (missing), that nothing is said twice (duplicate: the
// same words from any two regions within 2 s, or one region twice with no change in between), and that nothing chatters (more than 4 changes of one region in 2 s: a streamed reply announced word by word).
// A region that is hidden (no box) is not announced by a browser, so it is not recorded. The same words twice from one region (the nbsp trick that makes a repeat still a change) are two announcements.
const WATCH = `(() => {
    if (window.__ann) return true;
    const SEL = '[aria-live]:not([aria-live="off"]), [role="status"], [role="alert"], [role="log"]';
    const ann = window.__ann = { log: [], t0: performance.now() };
    const last = new WeakMap();
    const name = r => (r.id ? '#' + r.id : r.tagName.toLowerCase()) + (typeof r.className === 'string' && r.className.trim() ? '.' + r.className.trim().split(/\\s+/)[0] : '');
    const shown = r => r.getClientRects().length > 0 && getComputedStyle(r).visibility !== 'hidden';
    const loud = r => r.getAttribute('role') === 'alert' || r.getAttribute('aria-live') === 'assertive';
    const note = (r, how) => {
        const content = r.textContent.replace(/\\s+/g, c => c === '\\u00a0' ? c : ' '); // change is judged on the content: a region becoming visible with the same words is not a new announcement
        const text = (r.innerText || '').replace(/\\s+/g, ' ').trim(); // what is read is what is visible
        const was = last.get(r);
        last.set(r, content);
        if (!text || (was === content && !(how === 'shown' && loud(r))) || !shown(r)) return; // an alert that appears is announced; a status that merely appears is not
        ann.log.push({ t: Math.round(performance.now() - ann.t0), region: name(r), text: text.slice(0, 140), loud: loud(r), how });
    };
    const regionOf = n => { const e = n.nodeType === 1 ? n : n.parentElement; return e && e.closest ? e.closest(SEL) : null; };
    new MutationObserver(muts => {
        const seen = new Set();
        for (const m of muts) {
            if (m.type === 'attributes') { const r = m.target.matches && m.target.matches(SEL) ? m.target : regionOf(m.target); if (r && !seen.has(r)) { seen.add(r); note(r, 'shown'); } continue; }
            const r = regionOf(m.target);
            if (r && !seen.has(r)) { seen.add(r); note(r, 'changed ' + name(m.target.nodeType === 1 ? m.target : m.target.parentElement || r)); }
            for (const a of m.addedNodes) { if (a.nodeType !== 1) continue; const own = a.matches(SEL) ? [a] : []; for (const x of [...own, ...a.querySelectorAll(SEL)]) if (!seen.has(x)) { seen.add(x); note(x, 'added'); } }
        }
    }).observe(document.body, { childList: true, subtree: true, characterData: true, attributes: true, attributeFilter: ['hidden', 'style', 'class'] });
    for (const r of document.querySelectorAll(SEL)) last.set(r, r.textContent.replace(/\\s+/g, c => c === '\\u00a0' ? c : ' ')); // what is there at the start is not news
    return true; })()`;

async function watch(ev) { await ev(WATCH); }
async function clear(ev) { await ev(`window.__ann.log.length = 0; window.__ann.t0 = performance.now(); true`); }
async function log(ev) { return ev(`window.__ann.log.slice()`); }

/** The problems in a scene's log: words said twice (by any two regions within 2 s), a region chattering, and any announcement of `forbid`. */
function problems(scene, entries, { forbid = [] } = {}) {
    const out = [];
    for (let i = 0; i < entries.length; i++) for (let j = i + 1; j < entries.length; j++) {
        const a = entries[i], b = entries[j];
        if (b.t - a.t > 2000) break;
        if (a.text === b.text) out.push(`${scene}: "${a.text.slice(0, 60)}" is announced twice (${a.region} then ${b.region}, ${b.t - a.t} ms apart)`);
    }
    const by = new Map();
    for (const e of entries) { const l = by.get(e.region) || []; l.push(e.t); by.set(e.region, l); }
    for (const [region, ts] of by) for (let i = 0; i + 4 < ts.length; i++) if (ts[i + 4] - ts[i] <= 2000) { out.push(`${scene}: ${region} changes ${ts.length} times (5 within ${ts[i + 4] - ts[i]} ms): chatter`); break; }
    for (const re of forbid) for (const e of entries) if (re.test(e.text)) out.push(`${scene}: "${e.text.slice(0, 60)}" must not be announced (${e.region})`);
    return out;
}

/** The announcements of `entries` whose text matches `re` (a scene's "must say"). */
const said = (entries, re) => entries.filter(e => re.test(e.text));

module.exports = { watch, clear, log, problems, said };
