'use strict';
// The phone touch-target census (#6130): at 390 px with touch, every visible control on a scene is at least 44 x 44 px. measure() is an expression for ev(): the controls (buttons, links, inputs, selects,
// summaries, role=button/tab/menuitem/switch) under 44 px in either direction, and how many controls the scene has at all (so a scene that never rendered cannot pass by being empty). A checkbox or radio
// counts as its label. A link inside running text is inline and exempt (WCAG 2.2 target size, inline exception); everything else under 44 px fails, unless it is on the reviewed ALLOW list below.
const MEASURE = `(() => {
    const vis = e => { if (!e.getClientRects().length) return false; for (let p = e; p; p = p.parentElement) { const cs = getComputedStyle(p); if (cs.visibility === 'hidden' || cs.display === 'none' || p.hidden || p.inert) return false; } return true; };
    const sel = 'button, a[href], input:not([type=hidden]), select, textarea, summary, [role=button], [role=tab], [role=menuitem], [role=option], [role=switch], [role=checkbox], [role=radio]';
    const accName = e => { const lab = e.getAttribute('aria-labelledby'); if (lab) return lab.split(/\\s+/).map(i => (document.getElementById(i) || {}).textContent || '').join(' ').replace(/\\s+/g, ' ').trim();
        if (e.getAttribute('aria-label')) return e.getAttribute('aria-label').replace(/\\s+/g, ' ').trim();
        if (e.labels && e.labels.length) return [...e.labels].map(l => l.textContent).join(' ').replace(/\\s+/g, ' ').trim();
        const c = e.cloneNode(true); c.querySelectorAll('.material-icons, [aria-hidden=true], .sr-only').forEach(x => x.remove()); const t = (c.textContent || '').replace(/\\s+/g, ' ').trim();
        return t || e.getAttribute('alt') || e.getAttribute('title') || e.getAttribute('placeholder') || ''; };
    const groups = new Map(); const unnamed = [];
    const small = []; let n = 0;
    for (const e of document.querySelectorAll(sel)) {
        if (!vis(e) || e.disabled || e.closest('.sr-only, .xterm, [data-census-skip]')) continue;
        if (e.parentElement && e.parentElement.closest(sel) && e.tagName !== 'INPUT') continue;
        let b = e.getBoundingClientRect(); if (!b.width || !b.height || b.bottom < 0 || b.top > innerHeight + 2000) continue; n++;
        { const nm = accName(e); if (!nm && !(e.tagName === 'INPUT' && /hidden|submit/.test(e.type))) unnamed.push(e.tagName.toLowerCase() + (e.type ? '[' + e.type + ']' : '') + (e.name ? '[name=' + e.name + ']' : '') + (e.id ? '#' + e.id : '') + (typeof e.className === 'string' && e.className.trim() ? '.' + e.className.trim().split(/\\s+/)[0] : '') + ' near "' + ((e.closest('label, .settings-row, tr, li, div') || e.parentElement || {}).textContent || '').replace(/\\s+/g, ' ').trim().slice(0, 40) + '"'); if (nm) { const role = e.getAttribute('role') || (e.tagName.toLowerCase() + (e.tagName === 'INPUT' ? ':' + e.type : '')); const k = role + '|' + nm.toLowerCase(); if (!groups.has(k)) groups.set(k, { role, name: nm, count: 0, where: [] }); const g = groups.get(k); g.count++; if (g.where.length < 4) g.where.push(e.tagName.toLowerCase() + (e.id ? '#' + e.id : '') + (typeof e.className === 'string' && e.className.trim() ? '.' + e.className.trim().split(/\\s+/)[0] : '')); } }
        let via = '';
        if (e.tagName === 'INPUT' && /checkbox|radio/.test(e.type)) { const l = e.closest('label') || (e.labels && e.labels[0]); if (l) { const lb = l.getBoundingClientRect(); if (lb.width * lb.height > b.width * b.height) { b = lb; via = ' (label)'; } } }
        const inline = e.tagName === 'A' && !!e.closest('p, li, td, .markdown, .message-text, .chat-bubble, .settings-hint, .settings-info-text, .cs-note');
        if (inline) continue;
        if (b.width < 43.5 || b.height < 43.5) small.push({ d: e.tagName.toLowerCase() + (e.id ? '#' + e.id : '') + (typeof e.className === 'string' && e.className.trim() ? '.' + e.className.trim().split(/\\s+/)[0] : ''), t: (e.getAttribute('aria-label') || e.title || e.textContent || e.value || '').toString().trim().replace(/\\s+/g, ' ').slice(0, 30), w: Math.round(b.width), h: Math.round(b.height), via });
    }
    return { n, small, dups: [...groups.values()].filter(g => g.count > 1), unnamed }; })()`;

// Reviewed exceptions: [scene pattern, control description pattern, why]. Empty on purpose: a control under 44 px is fixed, not listed. Add one here only with the reason and a reviewer.
const ALLOW = [];

// Reviewed exceptions for same-named controls in one scene: [scene pattern, role pattern, name pattern, why]. A list of rows whose buttons all say "Delete" or "Open" is fixed by naming each by its row (aria-label with the row's title), not listed here.
const ALLOW_DUPS = [];
const allowedDup = (scene, d) => ALLOW_DUPS.some(([s, r, n]) => new RegExp(s).test(scene) && new RegExp(r).test(d.role) && new RegExp(n, 'i').test(d.name));

// Reviewed exceptions for a control with no accessible name at all: [scene pattern, control pattern, why]. Empty: an unnamed control is named.
const ALLOW_UNNAMED = [];
const allowedUnnamed = (scene, u) => ALLOW_UNNAMED.some(([s, c]) => new RegExp(s).test(scene) && new RegExp(c).test(u));

const allowed = (scene, c) => ALLOW.some(([s, d]) => new RegExp(s).test(scene) && new RegExp(d).test(`${c.d} ${c.t}`));

/** Click a control a person could tap: it throws when the selector matches nothing or the match is not on screen, so a scene that depends on it fails as "did not open" instead of measuring the screen it was already on. */
async function tap(ev, selector) {
    const r = await ev(`(() => { const e = document.querySelector(${JSON.stringify(selector)}); if (!e) return 'no element'; if (!e.getClientRects().length || e.closest('[hidden]')) return 'not visible'; e.click(); return 'ok'; })()`);
    if (r !== 'ok') throw new Error(`${selector}: ${r}`);
}

/** Record one scene: its controls and the ones under 44 px (minus the allow list). `expect` is an expression that must be true once the scene is open (its own content is showing), checked before anything is measured. */
async function scene(ev, results, name, setup, { min = 3, expect } = {}) {
    try {
        await setup();
        if (expect) { await new Promise(r => setTimeout(r, 400)); if (!(await ev(expect))) throw new Error(`the scene is not showing (${expect.slice(0, 80)})`); } // sleep-ok: the scene's own transition settles
    } catch (e) { results.push({ name, error: String(e && e.message || e).slice(0, 200) }); return; }
    await new Promise(r => setTimeout(r, 600)); // sleep-ok: the scene's own transitions settle
    const m = await ev(MEASURE);
    results.push({ name, n: m.n, min, small: m.small.filter(c => !allowed(name, c)), dups: (m.dups || []).filter(d => !allowedDup(name, d)), unnamed: (m.unnamed || []).filter(u => !allowedUnnamed(name, u)) });
}

/** The problems in the results: a scene that failed to open, one with too few controls to be the real screen, and every control under 44 px. */
function problems(results) {
    const out = [];
    for (const r of results) {
        if (r.error) out.push(`${r.name}: the scene did not open (${r.error})`);
        else {
            if (r.n < r.min) out.push(`${r.name}: only ${r.n} controls (expected at least ${r.min}): the scene did not render`);
            for (const c of r.small) out.push(`${r.name}: ${c.d} "${c.t}" ${c.w}x${c.h}${c.via}`);
            for (const u of r.unnamed || []) out.push(`${r.name}: ${u} has no accessible name`);
            if (!process.env.PHONE_TARGETS_NO_DUPS) for (const d of r.dups || []) out.push(`${r.name}: ${d.count} ${d.role} controls are all named "${d.name}" (${d.where.join(', ')})`);
        }
    }
    return out;
}

/** The scenes a suite must have run: a loop over what the page offers (its tabs, its settings groups) cannot quietly shrink to nothing when the page stops offering them. */
function required(results, names) {
    const have = new Set(results.map(r => r.name));
    return names.filter(n => !have.has(n)).map(n => `${n}: the scene was never visited`);
}

module.exports = { MEASURE, ALLOW, ALLOW_DUPS, ALLOW_UNNAMED, scene, tap, problems, required };
