// A WCAG contrast audit that runs in the page (#4851), for the light and dark visual pass of the shared list bars and the Inbox.
//   const { installAudit, setTheme, auditRegion } = require('./helpers/contrast');
//   await setTheme(ev, 'light');                       // the way the dashboard sets it: clearCustomThemeVars() then applyTheme(); settles a frame
//   const r = await auditRegion(ev, ['#event-search-bar'], { focus: ['#ev-search'] });   // { checked, fails: [{ sel, text, ratio, need, fg, bg }] }
// What it checks, inside each root: every element that draws text of its own (text nodes, a control's value or placeholder) against its
// effective background (the first opaque ancestor, alpha composited), 4.5:1 or 3:1 for large text (>= 24 px, or >= 18.66 px and bold); the
// borders of inputs, selects and buttons against what is behind them, 3:1 (WCAG 1.4.11, as a component's boundary where it has no other cue);
// and, for each selector in `focus`, the focus ring it shows once focused (outline or box-shadow colour against the background, 3:1).
// The borders of controls are reported apart (`borders`): the dashboard's inputs all use one border colour, so they are one finding for the style sheet, not one per panel, and WCAG 1.4.11 asks for it only where the border is what identifies the control.
// Transitions are switched off for the audit (a colour caught mid-fade is not the colour the panel has).
// A colour of any syntax (rgb, hsl, color-mix, oklch...) is read through a canvas pixel, so the audit does not parse CSS colours.
'use strict';
const { framesJs } = require('./wait');

const AUDIT_SRC = `(() => {
    const cv = document.createElement('canvas'); cv.width = cv.height = 1; const cx = cv.getContext('2d', { willReadFrequently: true });
    const rgba = c => { cx.clearRect(0, 0, 1, 1); cx.fillStyle = '#000'; cx.fillStyle = c; cx.fillRect(0, 0, 1, 1); const d = cx.getImageData(0, 0, 1, 1).data; return [d[0], d[1], d[2], d[3] / 255]; };
    const over = (f, b) => { const a = f[3]; return [f[0] * a + b[0] * (1 - a), f[1] * a + b[1] * (1 - a), f[2] * a + b[2] * (1 - a), 1]; };
    const lum = c => { const t = c.slice(0, 3).map(v => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }); return 0.2126 * t[0] + 0.7152 * t[1] + 0.0722 * t[2]; };
    const ratio = (a, b) => { const x = lum(a), y = lum(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
    const bgOf = el => { const layers = []; for (let e = el; e; e = e.parentElement) { const c = rgba(getComputedStyle(e).backgroundColor); if (c[3] > 0) layers.push(c); if (c[3] >= 1) break; } let base = rgba(getComputedStyle(document.body).backgroundColor); if (base[3] < 1) base = over(base, [255, 255, 255, 1]); for (let i = layers.length - 1; i >= 0; i--) base = over(layers[i], base); return base; };
    const sel = e => (e.id ? '#' + e.id : e.tagName.toLowerCase()) + (typeof e.className === 'string' && e.className.trim() ? '.' + e.className.trim().split(/\\s+/).slice(0, 2).join('.') : '');
    const shown = e => { if (!e.getClientRects().length) return false; for (let p = e; p; p = p.parentElement) { const s = getComputedStyle(p); if (s.visibility === 'hidden' || s.display === 'none' || Number(s.opacity) === 0) return false; } return true; };
    const hex = c => '#' + c.slice(0, 3).map(v => Math.round(v).toString(16).padStart(2, '0')).join('');
    const fmt = n => Math.round(n * 100) / 100;
    window.__audit = (roots, opts = {}) => {
        const fails = [], borders = []; let checked = 0;
        const note = (el, what, r, need, fg, bg) => fails.push({ sel: sel(el), what, text: (el.value || el.placeholder || el.textContent || '').trim().replace(/\\s+/g, ' ').slice(0, 40), ratio: fmt(r), need, fg: hex(fg), bg: hex(bg) });
        const seen = new Set();
        const text = (el, color, label) => {
            const bg = bgOf(el), fgc = rgba(color); const fg = fgc[3] < 1 ? over(fgc, bg) : fgc; const cs = getComputedStyle(el);
            const px = parseFloat(cs.fontSize), bold = Number(cs.fontWeight) >= 700 || cs.fontWeight === 'bold'; const large = px >= 24 || (px >= 18.66 && bold);
            const glyph = el.classList.contains('material-icons') || /^[\\uE000-\\uF8FF]/.test((el.textContent || '').trim());
            const need = large || glyph ? 3 : 4.5; const r = ratio(fg, bg); checked++; // an icon is a graphic: 3:1 (1.4.11), not the text's 4.5
            if (r < need) note(el, label, r, need, fg, bg);
        };
        for (const rs of roots) for (const root of document.querySelectorAll(rs)) {
            if (!shown(root)) continue;
            const all = [root, ...root.querySelectorAll('*')];
            for (const el of all) {
                if (seen.has(el) || !shown(el)) continue; seen.add(el);
                if (['SCRIPT', 'STYLE', 'SVG', 'PATH'].includes(el.tagName.toUpperCase())) continue;
                const own = [...el.childNodes].some(n => n.nodeType === 3 && n.textContent.trim());
                const cs = getComputedStyle(el);
                if (own && el.tagName !== 'OPTION') text(el, cs.color, 'text');
                if ((el.tagName === 'INPUT' && !['checkbox', 'radio'].includes(el.type) || el.tagName === 'TEXTAREA') && el.value) text(el, cs.color, 'value');
                if ((el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') && el.placeholder && !el.value) text(el, getComputedStyle(el, '::placeholder').color, 'placeholder');
                if (el.tagName === 'SELECT' && el.selectedIndex >= 0) text(el, cs.color, 'select');
                if (['INPUT', 'SELECT', 'BUTTON', 'TEXTAREA'].includes(el.tagName) && !(el.tagName === 'INPUT' && ['checkbox', 'radio', 'hidden'].includes(el.type))) {
                    const bw = parseFloat(cs.borderTopWidth) || 0; if (bw > 0 && rgba(cs.borderTopColor)[3] > 0) {
                        const par = el.parentElement ? bgOf(el.parentElement) : bgOf(el); const b = rgba(cs.borderTopColor); const bc = b[3] < 1 ? over(b, par) : b; const r = ratio(bc, par); checked++;
                        // a control that also has its own fill is bounded by the fill as well: the fill against what is behind it counts too
                        const fill = rgba(cs.backgroundColor); const r2 = fill[3] > 0 ? ratio(over(fill, par), par) : 0;
                        if (r < 3 && r2 < 3) { const n = fails.length; note(el, 'border', Math.max(r, r2), 3, bc, par); borders.push(fails.pop()); }
                    }
                }
            }
        }
        for (const fs of (opts.focus || [])) { const el = document.querySelector(fs); if (!el || !shown(el)) { fails.push({ sel: fs, what: 'focus', text: 'not found or hidden', ratio: 0, need: 3, fg: '', bg: '' }); continue; }
            const bcBefore = getComputedStyle(el).borderTopColor; el.focus({ focusVisible: true }); const cs = getComputedStyle(el); const bg = el.parentElement ? bgOf(el.parentElement) : bgOf(el); let best = 0, fg = [0, 0, 0, 1];
            if (cs.outlineStyle !== 'none' && parseFloat(cs.outlineWidth) > 0) { const c = rgba(cs.outlineColor); const cc = c[3] < 1 ? over(c, bg) : c; const r = ratio(cc, bg); if (r > best) { best = r; fg = cc; } }
            const bs = cs.boxShadow; if (bs && bs !== 'none') { const m = bs.match(/(rgba?\\([^)]*\\)|color\\([^)]*\\)|#[0-9a-f]{3,8})/i); if (m) { const c = rgba(m[1]); const cc = c[3] < 1 ? over(c, bg) : c; const r = ratio(cc, bg); if (r > best) { best = r; fg = cc; } } }
            if (cs.borderTopColor !== bcBefore && parseFloat(cs.borderTopWidth) > 0) { const c = rgba(cs.borderTopColor); const cc = c[3] < 1 ? over(c, bg) : c; const r = ratio(cc, bg); if (r > best) { best = r; fg = cc; } } // a border that changes colour on focus is a focus indicator too
            checked++; if (best < 3) fails.push({ sel: sel(el), what: 'focus ring', text: (el.value || el.placeholder || el.textContent || '').trim().slice(0, 30), ratio: fmt(best), need: 3, fg: hex(fg), bg: hex(bg) });
            el.blur(); }
        return { checked, fails, borders };
    };
    return true; })()`;

async function installAudit(ev) { await ev(AUDIT_SRC); }

/** The theme, the way the dashboard sets it (settings_core.js): the custom colour variables off, then the theme; a frame later. */
async function setTheme(ev, theme) {
    await ev(`(async () => { if (!document.getElementById('__notrans')) { const st = document.createElement('style'); st.id = '__notrans'; st.textContent = '*, *::before, *::after { transition: none !important; animation: none !important; }'; document.head.appendChild(st); }
        const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars && m.clearCustomThemeVars(); m.applyTheme(${JSON.stringify(theme)});
        await ${framesJs(2)}; return document.documentElement.getAttribute('data-theme'); })()`);
}
async function auditRegion(ev, roots, opts = {}) {
    await installAudit(ev);
    return ev(`window.__audit(${JSON.stringify(roots)}, ${JSON.stringify(opts)})`);
}

module.exports = { installAudit, setTheme, auditRegion };
