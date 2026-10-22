'use strict';
// The phone touch-target census (#6130): at 390 px with touch, every visible control on a scene is at least 44 x 44 px. measure() is an expression for ev(): the controls (buttons, links, inputs, selects,
// summaries, role=button/tab/menuitem/switch) under 44 px in either direction, and how many controls the scene has at all (so a scene that never rendered cannot pass by being empty). A checkbox or radio
// counts as its label. A link inside running text is inline and exempt (WCAG 2.2 target size, inline exception); everything else under 44 px fails, unless it is on the reviewed ALLOW list below.
const MEASURE = `(() => {
    const vis = e => { if (!e.getClientRects().length) return false; for (let p = e; p; p = p.parentElement) { const cs = getComputedStyle(p); if (cs.visibility === 'hidden' || cs.display === 'none' || p.hidden || p.inert) return false; } return true; };
    const sel = 'button, a[href], input:not([type=hidden]), select, textarea, summary, [role=button], [role=tab], [role=menuitem], [role=option], [role=switch], [role=checkbox], [role=radio]';
    const small = []; let n = 0;
    for (const e of document.querySelectorAll(sel)) {
        if (!vis(e) || e.disabled || e.closest('.sr-only, .xterm, [data-census-skip]')) continue;
        if (e.parentElement && e.parentElement.closest(sel) && e.tagName !== 'INPUT') continue;
        let b = e.getBoundingClientRect(); if (!b.width || !b.height || b.bottom < 0 || b.top > innerHeight + 2000) continue; n++;
        let via = '';
        if (e.tagName === 'INPUT' && /checkbox|radio/.test(e.type)) { const l = e.closest('label') || (e.labels && e.labels[0]); if (l) { const lb = l.getBoundingClientRect(); if (lb.width * lb.height > b.width * b.height) { b = lb; via = ' (label)'; } } }
        const inline = e.tagName === 'A' && !!e.closest('p, li, td, .markdown, .message-text, .chat-bubble, .settings-hint, .settings-info-text, .cs-note');
        if (inline) continue;
        if (b.width < 43.5 || b.height < 43.5) small.push({ d: e.tagName.toLowerCase() + (e.id ? '#' + e.id : '') + (typeof e.className === 'string' && e.className.trim() ? '.' + e.className.trim().split(/\\s+/)[0] : ''), t: (e.getAttribute('aria-label') || e.title || e.textContent || e.value || '').toString().trim().replace(/\\s+/g, ' ').slice(0, 30), w: Math.round(b.width), h: Math.round(b.height), via });
    }
    return { n, small }; })()`;

// Reviewed exceptions: [scene pattern, control description pattern, why]. Empty on purpose: a control under 44 px is fixed, not listed. Add one here only with the reason and a reviewer.
const ALLOW = [];

const allowed = (scene, c) => ALLOW.some(([s, d]) => new RegExp(s).test(scene) && new RegExp(d).test(`${c.d} ${c.t}`));

/** Record one scene: its controls and the ones under 44 px (minus the allow list). */
async function scene(ev, results, name, setup, { min = 3 } = {}) {
    try { await setup(); } catch (e) { results.push({ name, error: String(e && e.message || e).slice(0, 200) }); return; }
    await new Promise(r => setTimeout(r, 600)); // sleep-ok: the scene's own transitions settle
    const m = await ev(MEASURE);
    results.push({ name, n: m.n, min, small: m.small.filter(c => !allowed(name, c)) });
}

/** The problems in the results: a scene that failed to open, one with too few controls to be the real screen, and every control under 44 px. */
function problems(results) {
    const out = [];
    for (const r of results) {
        if (r.error) out.push(`${r.name}: the scene did not open (${r.error})`);
        else {
            if (r.n < r.min) out.push(`${r.name}: only ${r.n} controls (expected at least ${r.min}): the scene did not render`);
            for (const c of r.small) out.push(`${r.name}: ${c.d} "${c.t}" ${c.w}x${c.h}${c.via}`);
        }
    }
    return out;
}

module.exports = { MEASURE, ALLOW, scene, problems };
