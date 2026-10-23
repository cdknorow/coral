'use strict';
// The keyboard-only census (#6188): on a screen, walk it with real Tab key presses (CDP Input.dispatchKeyEvent, not .focus()) and report what a person who cannot use a mouse would hit:
//   unreachable  a visible, enabled control that no Tab press reaches (a member of a composite widget, a tablist, menu, listbox, radiogroup or toolbar, counts as reached when one of its members is: arrows move inside)
//   noFocus      a control Tab reaches that shows nothing when focused (no outline, shadow, border, fill, colour or underline difference from when it is not)
//   offscreen    a control Tab reaches that is not on the screen even after the browser scrolled to it
//   positive     a tabindex above 0 (it jumps the reading order)
// dialogCheck() does the same for a dialog: focus moves in when it opens, Tab and Shift+Tab stay inside it, Escape closes it and focus goes back to what opened it.
// ALLOW_KEYBOARD below is the reviewed list of exceptions: [scene pattern, control pattern, kind, why]. Empty: a control a keyboard cannot reach or see is fixed, not listed.
const ALLOW_KEYBOARD = [];
const allowedKb = (scene, kind, c) => ALLOW_KEYBOARD.some(([s, p, k]) => new RegExp(s).test(scene) && new RegExp(p).test(c) && new RegExp(k).test(kind));

async function press(Input, key, { shift = false, ctrl = false } = {}) {
    const vk = { Tab: 9, Escape: 27, Enter: 13 }[key];
    const o = { key, code: key, windowsVirtualKeyCode: vk, modifiers: (shift ? 8 : 0) | (ctrl ? 2 : 0) };
    await Input.dispatchKeyEvent({ type: 'rawKeyDown', ...o }); await Input.dispatchKeyEvent({ type: 'keyUp', ...o });
}

const SELECTOR = 'button, a[href], input:not([type=hidden]), select, textarea, summary, [role=button], [role=tab], [role=menuitem], [role=option], [role=switch], [role=checkbox], [role=radio], [tabindex]';
const COMPOSITE = '[role=tablist], [role=menu], [role=menubar], [role=listbox], [role=radiogroup], [role=toolbar], [role=grid], [role=tree], [role=group][aria-label]';

const HELPERS = `(() => {
    const vis = e => { if (!e.getClientRects().length) return false; if (e.checkVisibility && !e.checkVisibility({ checkVisibilityCSS: true, contentVisibilityAuto: true })) return false; for (let p = e; p; p = p.parentElement) { const cs = getComputedStyle(p); if (cs.visibility === 'hidden' || cs.display === 'none' || p.hidden || p.inert) return false; } return true; };
    const name = e => { const t = (e.getAttribute('aria-label') || e.title || e.textContent || e.value || e.placeholder || '').replace(/\\s+/g, ' ').trim().slice(0, 40); return e.tagName.toLowerCase() + (e.id ? '#' + e.id : '') + (typeof e.className === 'string' && e.className.trim() ? '.' + e.className.trim().split(/\\s+/)[0] : '') + (t ? ' "' + t + '"' : ''); };
    const snap = e => { const cs = getComputedStyle(e); return [cs.outlineStyle, cs.outlineWidth, cs.outlineColor, cs.boxShadow, cs.borderTopColor, cs.borderTopWidth, cs.backgroundColor, cs.color, cs.textDecorationLine, cs.opacity, cs.transform].join('|'); };
    const snapAll = e => { const out = [snap(e)]; let p = e.parentElement; for (let i = 0; i < 3 && p && p !== document.body; i++, p = p.parentElement) out.push(snap(p)); return out.join('||'); }; // a ring may be drawn on the control's wrapper (:focus-within)
    window.__kb = { vis, name, snap: snapAll };
    return true; })()`;

/** Put the page's tab order back at its start, so the first Tab press lands on the first control. */
async function reset(ev) {
    await ev(`(() => { document.activeElement && document.activeElement.blur && document.activeElement.blur(); window.scrollTo(0, 0); const b = document.body; b.setAttribute('tabindex', '-1'); b.focus(); b.removeAttribute('tabindex'); document.getSelection().removeAllRanges(); return true; })()`);
}

/** Walk the screen with Tab, `max` presses at most (it stops when the order comes round to a control it has visited). Returns { stops, unreachable, noFocus, offscreen, positive }. */
async function walk(ev, Input, { max = 250, within = null } = {}) {
    await ev(HELPERS);
    await ev(`(() => { if (!document.getElementById('__kbnotrans')) { const st = document.createElement('style'); st.id = '__kbnotrans'; st.textContent = '*, *::before, *::after { transition: none !important; animation: none !important; }'; document.head.appendChild(st); } window.__kbSeen = []; return true; })()`); // a ring caught mid-fade is not the ring the control has
    await reset(ev);
    let dateParts = 0;
    const stops = []; let trapped = '', escaped = false, terminalEscape = ''; const extra = [];
    for (let i = 0; i < max; i++) {
        await press(Input, 'Tab');
        const s = await ev(`(() => { const e = document.activeElement; if (!e || e === document.body || e === document.documentElement) return { body: true }; const i = window.__kbSeen.indexOf(e); if (i >= 0) return { again: i }; window.__kbSeen.push(e);
            const b = e.getBoundingClientRect(); return { n: window.__kbSeen.length - 1, d: window.__kb.name(e), on: window.__kb.snap(e), inside: ${within ? `!!e.closest(${JSON.stringify(within)})` : 'true'},
                onScreen: b.width > 0 && b.height > 0 && b.bottom > 0 && b.right > 0 && b.top < innerHeight && b.left < innerWidth, tab: e.tabIndex }; })()`);
        if (s.again !== undefined) {
            // a date or time field is several tab stops in one element (its day, month and year parts): Tab moves between them and then on, so the same element again is not a trap until its parts are used up (#6288)
            if (s.again === stops.length - 1 && stops.length && /^(date|time|datetime-local|month|week)$/.test(await ev(`document.activeElement.type || ''`)) && (dateParts += 1) <= 4) continue;
            // the same control again: Tab goes nowhere. The terminal keeps Tab for the shell by design and says how to leave (its status bar: Ctrl+Shift+Esc to unfocus): that way out must work, and the walk goes on from there.
            if (s.again === stops.length - 1 && stops.length) {
                const last = stops[stops.length - 1];
                if (/xterm-helper-textarea/.test(last.d) && !escaped) {
                    escaped = true;
                    const h = await ev(`(() => { const t = document.activeElement; const el = t.getAttribute('aria-describedby') && document.getElementById(t.getAttribute('aria-describedby')); return { ok: !!el && el.getClientRects().length > 0 && /Ctrl\\+Shift\\+Esc/.test(el.textContent), op: el ? Number(getComputedStyle(el).opacity) : 0 }; })()`);
                    if (!h.ok) extra.push('the terminal input is not described by a visible hint that names Ctrl+Shift+Esc (how a keyboard user leaves it)');
                    else if (h.op < 0.85) extra.push('the terminal hint is dimmed to ' + h.op + ' while the terminal has focus');
                    await press(Input, 'Escape', { shift: true, ctrl: true });
                    const left = await ev(`document.activeElement !== window.__kbSeen[window.__kbSeen.length - 1]`);
                    if (left) { terminalEscape = 'Ctrl+Shift+Esc releases the terminal'; continue; }
                    trapped = last.d + ' (and Ctrl+Shift+Esc does not release it)'; break;
                }
                trapped = last.d;
            }
            break;
        }
        if (s.body) { if (stops.length) break; continue; }
        dateParts = 0; stops.push(s);
    }
    // what each stop looked like unfocused, to tell whether focus shows
    const noFocus = [], offscreen = [], positive = [];
    const off = await ev(`(() => window.__kbSeen.map(e => { e.blur(); return window.__kb.snap(e); }))()`);
    stops.forEach((s, i) => { if (off[i] === s.on && !/^iframe/.test(s.d)) noFocus.push(s.d); // a frame hands focus to its own page, which draws its own ring
         if (!s.onScreen) offscreen.push(s.d); if (s.tab > 0) positive.push(s.d); });
    // every control a person sees that Tab never reached, unless a member of its composite widget was
    const unreachable = await ev(`(() => { const seen = new Set(window.__kbSeen); const out = []; const reachedComposite = new Set(); for (const e of seen) { const c = e.closest(${JSON.stringify(COMPOSITE)}); if (c) reachedComposite.add(c); if (e.type === 'radio' && e.name) document.querySelectorAll('input[type=radio][name="' + CSS.escape(e.name) + '"]').forEach(r => seen.add(r)); }
        const modal = [...document.querySelectorAll('dialog[open], .modal.show, .modal[style*="flex"], [aria-modal=true]')].find(m => window.__kb.vis(m)); // under an open dialog the page behind it is out of reach on purpose
        for (const e of document.querySelectorAll(${JSON.stringify(SELECTOR)})) {
            if (modal && !modal.contains(e)) continue;
            if (seen.has(e) || !window.__kb.vis(e) || e.disabled || e.closest('.sr-only, .xterm, [data-census-skip], [inert]')) continue;
            if (e.parentElement && e.parentElement.closest('button, a[href], summary, [role=button]') && e.tagName !== 'INPUT') continue; // a part of a control
            if (e.tagName === 'A' && !e.getAttribute('href')) continue;
            if (e.tabIndex < 0 && !e.matches('button, a[href], input, select, textarea, summary, [role=button], [role=tab], [role=menuitem], [role=option], [role=switch], [role=checkbox], [role=radio]')) continue; // a script-focusable container, not a control
            const c = e.closest(${JSON.stringify(COMPOSITE)}); if (c && reachedComposite.has(c)) continue;
            const b = e.getBoundingClientRect(); if (!b.width || !b.height) continue;
            out.push(window.__kb.name(e)); }
        return out; })()`);
    return { stops, unreachable: unreachable.filter(Boolean), noFocus, offscreen, positive, trapped, terminalEscape, extra };
}

/** A dialog: focus goes in when it opens, Tab and Shift+Tab stay in, Escape closes it and focus returns to the opener. `open` opens it (given the opener focused), `box` is its selector, `opener` the selector of what opens it. */
async function dialogCheck(ev, Input, { opener, box, open, closedWhen }) {
    const bad = [];
    await ev(HELPERS);
    const pick = `[...document.querySelectorAll(${JSON.stringify(opener)})].find(e => window.__kb.vis(e))`;
    if (!(await ev(`(() => { const o = ${pick}; if (!o) return false; o.focus(); return document.activeElement === o; })()`))) return ['the opener is not on the screen or does not take focus (' + opener + ')'];
    await open();
    await new Promise(r => setTimeout(r, 300)); // sleep-ok: the dialog's open transition
    const inside = () => ev(`(() => { const d = document.querySelector(${JSON.stringify(box)}); const a = document.activeElement; return !!d && !!a && a !== document.body && d.contains(a); })()`);
    if (!(await inside())) bad.push('focus did not move into the dialog when it opened');
    let escaped = false;
    for (let i = 0; i < 60 && !escaped; i++) { await press(Input, 'Tab'); if (!(await inside())) escaped = true; }
    if (escaped) bad.push('Tab left the dialog (focus is not trapped)');
    await ev(`(() => { const d = document.querySelector(${JSON.stringify(box)}); const f = [...d.querySelectorAll('a[href], button:not([disabled]), input:not([disabled]):not([type=hidden]), select:not([disabled]), textarea:not([disabled]), summary, [tabindex]:not([tabindex="-1"])')].filter(e => window.__kb.vis(e)); if (f[0]) f[0].focus(); return true; })()`);
    await press(Input, 'Tab', { shift: true });
    if (!(await inside())) bad.push('Shift+Tab left the dialog');
    await press(Input, 'Escape');
    await new Promise(r => setTimeout(r, 300)); // sleep-ok: the dialog's close transition
    const closed = await ev(closedWhen || `(() => { const d = document.querySelector(${JSON.stringify(box)}); return !d || !d.getClientRects().length || getComputedStyle(d).display === 'none' || d.hidden || !d.open && d.tagName === 'DIALOG'; })()`);
    if (!closed) bad.push('Escape did not close the dialog');
    else {
        const back = await ev(`(() => { const o = ${pick}; const a = document.activeElement; const d = document.querySelector(${JSON.stringify(box)}); return { ok: !!o && a === o, now: a ? window.__kb.name(a) : 'nothing', recorded: d && d._focusOpener ? window.__kb.name(d._focusOpener) : (d && d._focusAttached ? 'attached, no opener' : 'not attached'), shown: d ? getComputedStyle(d).display : '' }; })()`);
        if (!back.ok) bad.push('focus did not return to what opened the dialog (it is on ' + back.now + '; the dialog recorded: ' + back.recorded + ')');
    }
    return bad;
}

function problems(name, r) {
    const out = [];
    for (const [kind, list] of [['unreachable by Tab', r.unreachable], ['shows no focus', r.noFocus], ['off the screen when focused', r.offscreen], ['has tabindex above 0', r.positive]]) for (const c of list) if (!allowedKb(name, kind, c)) out.push(`${name}: ${c} ${kind}`);
    for (const x of r.extra || []) out.push(`${name}: ${x}`);
    if (r.trapped) out.push(`${name}: Tab is trapped on ${r.trapped} (it stays there)`);
    return out;
}

module.exports = { walk, dialogCheck, problems, press, ALLOW_KEYBOARD };
