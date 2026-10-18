// Dashboard accessibility pass: pins the fixes from the keyboard/labels/contrast audit.
//   1. The top nav is page navigation, not tabs: no tablist/tab roles, no aria-selected, and the open
//      view's button is aria-current="page".
//   2. Muted text (--text-muted) is at least 4.5:1 on the panel backgrounds in both themes.
//   3. Every Settings select and the Analytics time range have an accessible name.
//   4. The Settings dialog is named, takes focus when it opens, keeps Tab inside, and gives focus
//      back when it closes (Escape: to the menu button its opener sat in; Cancel: to the opener).
//   5. Every dialog in the page has an accessible name (aria-labelledby or aria-label) and aria-modal (#4023).
//   6. Contrast on the home page, an agent's Chat view and Settings, light and dark, at 1280 and 390 px: no text below 4.5:1
//      (3:1 for large text), no icon glyph below 3:1 (#4023).
//   7. Every kind of focusable control shows a focus indicator (an outline, a ring, or for the two text boxes that use it,
//      a border colour change) when :focus-visible applies (#4023).
//   8. A live list update does not take keyboard focus away from the row it is on (#4023).
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

// WCAG contrast ratio of two '#rrggbb' colours.
const contrast = (a, b) => {
    const lum = hex => {
        const [r, g, bl] = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255)
            .map(v => v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
        return 0.2126 * r + 0.7152 * g + 0.0722 * bl;
    };
    const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p);
    return (x + 0.05) / (y + 0.05);
};

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const { Input, Emulation, DOM, CSS } = client;
    const key = async (k, shift = false) => {
        const code = { Tab: 9, Escape: 27, Enter: 13 }[k];
        const base = { key: k, code: k, windowsVirtualKeyCode: code, modifiers: shift ? 8 : 0 };
        await Input.dispatchKeyEvent({ type: 'keyDown', ...base, ...(k === 'Enter' ? { text: '\r' } : {}) });
        await Input.dispatchKeyEvent({ type: 'keyUp', ...base });
    };
    const focusInfo = () => ev(`(() => {
        const a = document.activeElement, d = document.getElementById('settings-modal');
        return { inside: !!d && d.contains(a), id: a && a.id, tag: a && a.tagName, text: a && (a.getAttribute('aria-label') || a.textContent || '').trim().slice(0, 40), controls: a && a.getAttribute('aria-controls') };
    })()`);
    const dialogShown = `!!document.getElementById('settings-modal') && getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`; // Settings is built on first open (#4500)
    const dialogHidden = `!document.getElementById('settings-modal') || getComputedStyle(document.getElementById('settings-modal')).display === 'none'`;

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        await waitFor(`typeof window.switchNavTab === 'function' && !!document.getElementById('nav-tab-agents')`, 'the nav', 10000);

        console.log('1. Top nav: page navigation with aria-current');
        const nav = () => ev(`(() => {
            const n = document.querySelector('.top-nav-tabs'), btns = [...n.querySelectorAll('.top-nav-tab')];
            return { navRole: n.getAttribute('role'), label: n.getAttribute('aria-label'),
                roles: btns.map(b => b.getAttribute('role')).filter(Boolean), selected: btns.filter(b => b.hasAttribute('aria-selected')).map(b => b.id),
                current: btns.filter(b => b.getAttribute('aria-current') === 'page').map(b => b.id) };
        })()`);
        let n = await nav();
        assert.equal(n.navRole, null, 'the nav is not a tablist'); assert.equal(n.label, 'Main navigation');
        assert.deepEqual(n.roles, [], 'no tab roles on the nav buttons');
        assert.deepEqual(n.selected, [], 'no aria-selected on the nav buttons');
        assert.deepEqual(n.current, ['nav-tab-agents'], 'Agents is the current page on load');
        await ev(`switchNavTab('history'); true`);
        n = await nav();
        assert.deepEqual(n.current, ['nav-tab-history'], 'aria-current follows the open view'); assert.deepEqual(n.selected, []);
        await ev(`switchNavTab('agents'); true`);
        assert.deepEqual((await nav()).current, ['nav-tab-agents']);
        console.log('   ✓ no tablist/tab/aria-selected; aria-current="page" follows the view');

        console.log('2. Muted text contrast: the applied theme, then the stylesheet in both themes');
        const BGS = ['--bg-primary', '--bg-secondary', '--bg-tertiary', '--bg-elevated'];
        const readVars = `(() => { const cs = getComputedStyle(document.documentElement), g = n => cs.getPropertyValue(n).trim().toLowerCase();
            return { muted: g('--text-muted'), bgs: ${JSON.stringify(BGS)}.map(n => [n, g(n)]), inline: !!document.documentElement.style.getPropertyValue('--text-muted') }; })()`;
        const checkContrast = (label, v) => {
            for (const [name, bg] of v.bgs) {
                const r = contrast(v.muted, bg);
                assert.ok(r >= 4.5, `${label}: --text-muted ${v.muted} on ${name} ${bg} is ${r.toFixed(2)}:1, under 4.5`);
            }
        };
        // What a fresh install shows: the bundled Dark theme, applied as inline variables.
        const applied = await ev(readVars);
        checkContrast('the applied theme', applied);
        // The stylesheet's own values (variables.css), dark and light, without a custom theme on top.
        const inlineVars = await ev(`(() => { const st = document.documentElement.style, saved = [];
            for (let i = st.length - 1; i >= 0; i--) { const n = st[i]; if (n.startsWith('--')) { saved.push([n, st.getPropertyValue(n)]); st.removeProperty(n); } }
            window.__savedThemeVars = saved; return saved.length; })()`);
        for (const theme of ['dark', 'light']) {
            await ev(`document.documentElement.setAttribute('data-theme', ${JSON.stringify(theme)}); true`);
            checkContrast(`variables.css ${theme}`, await ev(readVars));
        }
        await ev(`(() => { document.documentElement.setAttribute('data-theme', 'dark'); for (const [n, v] of window.__savedThemeVars) document.documentElement.style.setProperty(n, v); return true; })()`);
        console.log(`   ✓ --text-muted at least 4.5:1 on primary/secondary/tertiary/elevated: applied theme (${applied.muted}${applied.inline ? ', bundled Dark' : ''}), variables.css dark and light (${inlineVars} inline theme vars set aside)`);

        console.log('3. Select names');
        await ev(`window.__coral.buildAllLazy(); true`); // the dialogs are built on first open (#4500): name every select in them
        const names = await ev(`(() => {
            const ids = ['settings-theme', 'settings-renderer-select', 'settings-agent-type', 'settings-permission-mode',
                'settings-database-encryption', 'settings-terminal-font-size', 'settings-terminal-scrollback', 'cost-time-range'];
            const name = el => {
                if (!el) return null;
                const by = el.getAttribute('aria-labelledby');
                if (by) return by.split(/\\s+/).map(i => (document.getElementById(i) || {}).textContent || '').join(' ').trim();
                return (el.getAttribute('aria-label') || (el.labels && el.labels[0] && el.labels[0].textContent) || '').trim();
            };
            const out = Object.fromEntries(ids.map(i => [i, name(document.getElementById(i))]));
            out.unnamed = [...document.querySelectorAll('#settings-modal select')].filter(s => !name(s)).map(s => s.id);
            return out;
        })()`);
        assert.deepEqual(names.unnamed, [], 'every select in Settings has a name');
        for (const [id, nm] of Object.entries(names)) if (id !== 'unnamed') assert.ok(nm, `${id} has a name`);
        assert.equal(names['cost-time-range'], 'Time range');
        assert.match(names['settings-theme'], /Theme/);
        console.log('   ✓ the 7 Settings selects and the Analytics time range are named');

        console.log('4. Settings dialog: name, focus in, Tab stays in, focus back');
        assert.equal(await ev(`(() => { const d = document.getElementById('settings-modal'); return d.getAttribute('role') + '|' + d.getAttribute('aria-modal') + '|' + (document.getElementById(d.getAttribute('aria-labelledby')) || {}).textContent; })()`),
            'dialog|true|Settings', 'role=dialog, aria-modal, named "Settings"');
        // Open the way a keyboard user does: the menu button, then Enter on the Settings item.
        await ev(`(() => { const b = document.querySelector('[aria-controls="top-bar-settings-menu"]'); b.focus(); b.click(); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('top-bar-settings-menu')).display !== 'none'`, 'the top bar menu', 3000);
        await ev(`(() => { [...document.querySelectorAll('#top-bar-settings-menu .overflow-menu-item')].find(b => b.getAttribute('data-on-click') === 'm-close-top-bar-settings-then-show-settings-modal').focus(); return true; })()`);
        await key('Enter');
        await waitFor(dialogShown, 'the Settings dialog', 5000);
        await waitFor(`document.getElementById('settings-modal').contains(document.activeElement)`, 'focus inside the dialog on open', 3000,
            `({ active: document.activeElement && (document.activeElement.id || document.activeElement.tagName) })`);
        const count = await ev(`[...document.querySelectorAll('#settings-modal button:not([disabled]), #settings-modal input:not([disabled]), #settings-modal select:not([disabled]), #settings-modal textarea:not([disabled]), #settings-modal a[href], #settings-modal [tabindex]:not([tabindex="-1"])')].filter(e => e.getClientRects().length > 0).length`);
        assert.ok(count > 3, `the dialog has controls (${count})`);
        const first = await focusInfo();
        for (let i = 0; i < count + 2; i++) {
            await key('Tab');
            const f = await focusInfo();
            assert.ok(f.inside, `Tab ${i + 1} of ${count + 2} stays in the dialog (${JSON.stringify(f)})`);
        }
        await key('Tab', true);
        assert.ok((await focusInfo()).inside, 'Shift+Tab stays in the dialog');
        console.log(`   ✓ named, focus in on open (${first.tag} ${first.text || first.id}), ${count + 2} Tabs and a Shift+Tab stay inside`);

        await key('Escape');
        await waitFor(dialogHidden, 'Escape closes the dialog', 3000);
        await waitFor(`document.activeElement && document.activeElement.getAttribute('aria-controls') === 'top-bar-settings-menu'`, 'focus back on the menu button after Escape', 3000,
            `({ active: document.activeElement && (document.activeElement.id || document.activeElement.tagName) })`);
        console.log('   ✓ Escape: focus back on the menu button (the opener sat in that menu, now closed)');

        // A visible opener gets focus back itself: open from the nav's Agents button, close with Cancel.
        await ev(`(() => { document.getElementById('nav-tab-agents').focus(); showSettingsModal(); return true; })()`);
        await waitFor(dialogShown, 'the Settings dialog again', 5000);
        await waitFor(`document.getElementById('settings-modal').contains(document.activeElement)`, 'focus inside on the second open', 3000);
        await ev(`(() => { [...document.querySelectorAll('#settings-modal button')].find(b => (/hideSettingsModal\(\)/.test(b.getAttribute('onclick') || '') || b.getAttribute('data-on-click') === 'm-hide-settings-modal') && b.textContent.trim() === 'Cancel').click(); return true; })()`);
        await waitFor(dialogHidden, 'Cancel closes the dialog', 3000);
        await waitFor(`document.activeElement && document.activeElement.id === 'nav-tab-agents'`, 'focus back on the opener after Cancel', 3000,
            `({ active: document.activeElement && (document.activeElement.id || document.activeElement.tagName) })`);
        console.log('   ✓ Cancel: focus back on the opener');

        console.log('5. Dialogs are named and modal');
        const dialogs = await ev(`[...document.querySelectorAll('[role=dialog], dialog')].map(d => ({ id: d.id, name: !!(d.getAttribute('aria-label') || (d.getAttribute('aria-labelledby') && d.getAttribute('aria-labelledby').split(' ').every(i => document.getElementById(i)))), modal: d.getAttribute('aria-modal') === 'true' || d.tagName === 'DIALOG' }))`);
        assert.ok(dialogs.length >= 15, `the page has its dialogs (${dialogs.length})`);
        assert.deepEqual(dialogs.filter(d => !d.name).map(d => d.id), [], 'every dialog has an accessible name that points at something');
        assert.deepEqual(dialogs.filter(d => !d.modal).map(d => d.id), [], 'and says it is modal');
        console.log(`   ✓ ${dialogs.length} dialogs named and modal`);

        console.log('6. Contrast in both themes at 1280 and 390 px');
        // The contrast scan of root's text (an expression's source; root is an expression too).
        const CONTRAST_AT = root => `((root) => {
            const vis = e => { const r = e.getBoundingClientRect(); const c = getComputedStyle(e); return r.width > 0 && r.height > 0 && c.visibility !== 'hidden' && c.display !== 'none' && c.opacity !== '0'; };
            // rgb()/rgba(), and color(srgb r g b / a) with 0-1 channels: how Chrome reports a color-mix() colour (#4218).
            const parse = s => { let m = s.match(/rgba?\\(([^)]+)\\)/); if (m) { const p = m[1].split(/[ ,\\/]+/).filter(Boolean).map(Number); return { r: p[0], g: p[1], b: p[2], a: p.length > 3 ? p[3] : 1 }; }
                m = s.match(/color\\(srgb ([^)]+)\\)/); if (!m) return null; const p = m[1].split(/[ \\/]+/).filter(Boolean).map(Number); return { r: p[0] * 255, g: p[1] * 255, b: p[2] * 255, a: p.length > 3 ? p[3] : 1 }; };
            const blend = (f, b) => ({ r: f.r * f.a + b.r * (1 - f.a), g: f.g * f.a + b.g * (1 - f.a), b: f.b * f.a + b.b * (1 - f.a), a: 1 });
            const lum = c => { const f = v => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b); };
            const bgOf = e => { const layers = []; let n = e; while (n && n.nodeType === 1) { const cs = getComputedStyle(n); if (cs.backgroundImage && cs.backgroundImage !== 'none') return null; const c = parse(cs.backgroundColor); if (c && c.a > 0) { layers.push(c); if (c.a === 1) break; } n = n.parentElement; } let base = { r: 255, g: 255, b: 255, a: 1 }; const root = parse(getComputedStyle(document.body).backgroundColor); if (layers.length && layers[layers.length - 1].a < 1 && root && root.a > 0) base = root; for (let i = layers.length - 1; i >= 0; i--) base = blend(layers[i], base); return base; };
            const bad = []; const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT); const seen = new Set();
            for (let n = w.nextNode(); n; n = w.nextNode()) {
                if (!n.nodeValue.trim()) continue; const e = n.parentElement; if (!e || seen.has(e) || !vis(e) || e.closest('script, style, svg')) continue; seen.add(e);
                const cs = getComputedStyle(e); const fg = parse(cs.color); if (!fg) { bad.push('unreadable colour ' + cs.color); continue; } const bg = bgOf(e); if (!bg) continue; const f = blend(fg, bg);
                const ratio = (Math.max(lum(f), lum(bg)) + 0.05) / (Math.min(lum(f), lum(bg)) + 0.05); const size = parseFloat(cs.fontSize); const large = size >= 24 || (size >= 18.66 && parseInt(cs.fontWeight) >= 700);
                // An icon, or a control drawn as one glyph with a name of its own (the kebab's "⋮"), is a UI part: 3:1 (#4218).
                const glyph = /^[^\\p{L}\\p{N}]{1,2}$/u.test(n.nodeValue.trim()) && !!e.closest('button[aria-label], button[title], [role=button][aria-label], [role=button][title]');
                const need = e.classList.contains('material-icons') || glyph ? 3 : (large ? 3 : 4.5);
                if (ratio < need - 0.01) bad.push((e.id ? '#' + e.id : e.tagName.toLowerCase() + '.' + String(e.className).split(' ').slice(0, 2).join('.')) + ' "' + n.nodeValue.trim().slice(0, 20) + '" ' + Math.round(ratio * 100) / 100 + ' < ' + need);
            }
            return [...new Set(bad)];
        })(${root})`;
        const CONTRAST = CONTRAST_AT('document.body');
        // States a scan of the page as it rests never sees (#4218): each visible control and row is
        // scanned again with :hover, :focus-visible and both forced (CSS.forcePseudoState, so the
        // pointer's position plays no part), and each sidebar row again as the selected row.
        const FORCED = '.session-group-item, button, a[href], select, input, [role=button], [role=menuitem], [role=tab], [role=option], .overflow-menu-item';
        const forcedStates = async () => {
            const n = await ev(`(() => { let i = 0; for (const e of document.querySelectorAll(${JSON.stringify(FORCED)})) { e.removeAttribute('data-a11y-i'); const r = e.getBoundingClientRect(); if (r.width > 0 && r.height > 0 && getComputedStyle(e).visibility !== 'hidden') e.setAttribute('data-a11y-i', String(i++)); } return i; })()`);
            const { root } = await DOM.getDocument({ depth: 0 });
            const bad = [];
            for (let i = 0; i < n; i++) {
                const sel = `[data-a11y-i="${i}"]`;
                const { nodeId } = await DOM.querySelector({ nodeId: root.nodeId, selector: sel });
                if (!nodeId) continue;
                const isRow = await ev(`document.querySelector('${sel}').classList.contains('session-group-item') && !document.querySelector('${sel}').classList.contains('active')`);
                for (const active of isRow ? [false, true] : [false]) {
                    if (active) await ev(`document.querySelector('${sel}').classList.add('active')`);
                    for (const pseudo of active ? [[], ['hover'], ['focus-visible']] : [['hover'], ['focus-visible'], ['hover', 'focus-visible']]) {
                        await CSS.forcePseudoState({ nodeId, forcedPseudoClasses: pseudo });
                        for (const b of await ev(CONTRAST_AT(`document.querySelector('${sel}')`))) bad.push(`${b} [${active ? 'selected' : ''}${pseudo.map(p => ':' + p).join('')}]`);
                    }
                    await CSS.forcePseudoState({ nodeId, forcedPseudoClasses: [] });
                    if (active) await ev(`document.querySelector('${sel}').classList.remove('active')`);
                }
            }
            return { n, bad: [...new Set(bad)] };
        };
        const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a scene settles over several answers; there is no single completion event
        const forcedFound = []; let forcedCount = 0;
        await DOM.enable(); await CSS.enable();
        for (const [dev, w, h, mobile] of [['desktop', 1280, 800, false], ['phone', 390, 844, true]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            for (const theme of ['dark', 'light']) {
                await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
                await ev(`window.__coralReady.then(() => true)`);
                await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); (async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars(); m.applyTheme('${theme}'); })(); window.__m.add('docs-bot', 'A', 6, { sleeping: false }); window.__m.add('api-worker', 'B', 3, { sleeping: false }); true`);
                await ev(`document.documentElement.style.setProperty('--zz', '1'); const st = document.createElement('style'); st.textContent = '* { transition: none !important }'; document.head.appendChild(st); true`);
                await sleep(500);   // sleep-ok: a scene settles over several answers; there is no single completion event
                const scenes = [['home', null], ['chat', `window.selectLiveSession('docs-bot', 'claude', 'docs-bot-id', 'local'); true`], ['settings', `(async () => { const m = await import((window.__coralStatic || '/static/') + 'modals.js'); await m.showSettingsModal(); return true; })()`]];
                for (const [name, act] of scenes) {
                    if (act) { await ev(act); await sleep(name === 'settings' ? 1200 : 1000); }   // sleep-ok: a scene settles over several answers; there is no single completion event
                    // Every count badge of the sidebar's tab strip shows a count while it is measured (an empty one is hidden, so the check only ever met them by chance): #4539's Saved tab was 4.4:1 in the light theme.
                    await ev(`(() => { for (const el of document.querySelectorAll('.agentic-tab-count')) if (!el.textContent) el.textContent = '3'; return true; })()`);
                    const bad = await ev(CONTRAST);
                    assert.deepEqual(bad, [], `${name} / ${theme} / ${dev}: text and icons reach contrast: ${bad.join('; ')}`);
                    const forced = await forcedStates();
                    forcedFound.push(...forced.bad.map(b => `${name} / ${theme} / ${dev}: ${b}`));
                    forcedCount += forced.n;
                }
            }
        }
        if (process.env.A11Y_FORCED_REPORT) require('node:fs').writeFileSync(process.env.A11Y_FORCED_REPORT, forcedFound.join('\n') + '\n');
        assert.deepEqual(forcedFound, [], `hovered, focused and selected states reach contrast: ${forcedFound.join('; ')}`);
        console.log(`   ✓ home, Chat and Settings: no text under 4.5:1, no icon under 3:1, both themes, 1280 and 390 px, at rest and with ${forcedCount} controls and rows hovered, focused and selected`);

        console.log('7. Every kind of focusable control shows a focus indicator');
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('docs-bot', 'A', 6, { sleeping: false }); true`);
        await sleep(500);   // sleep-ok: a scene settles over several answers; there is no single completion event
        await ev(`window.selectLiveSession('docs-bot', 'claude', 'docs-bot-id', 'local'); true`); await sleep(1000);   // sleep-ok: a scene settles over several answers; there is no single completion event
        await DOM.enable(); await CSS.enable();
        const marked = await ev(`(() => { const seen = new Set(); let i = 0; document.querySelectorAll('[data-zz]').forEach(e => e.removeAttribute('data-zz'));
            const vis = e => { const r = e.getBoundingClientRect(); const c = getComputedStyle(e); return r.width > 0 && r.height > 0 && c.visibility !== 'hidden' && c.display !== 'none'; };
            for (const e of document.querySelectorAll('button, a[href], input:not([type=hidden]), select, textarea, summary, [tabindex]:not([tabindex="-1"]), [role=button]')) { if (!vis(e)) continue;
                const sig = e.tagName + '|' + (typeof e.className === 'string' ? e.className.split(/\\s+/).slice(0, 3).join('.') : '') + '|' + (e.type || ''); if (seen.has(sig)) continue; seen.add(sig); e.setAttribute('data-zz', String(i++)); } return i; })()`);
        assert.ok(marked >= 15, `controls to check (${marked})`);
        const { root } = await DOM.getDocument({ depth: 0 });
        const { nodeIds } = await DOM.querySelectorAll({ nodeId: root.nodeId, selector: '[data-zz]' });
        const noRing = [];
        for (const nodeId of nodeIds) {
            const style = async forced => { await CSS.forcePseudoState({ nodeId, forcedPseudoClasses: forced }); const { computedStyle } = await CSS.getComputedStyleForNode({ nodeId }); return k => (computedStyle.find(c => c.name === k) || {}).value; };
            const on = await style(['focus', 'focus-visible']); const onOutline = on('outline-style') !== 'none' && parseFloat(on('outline-width')) > 0; const onShadow = on('box-shadow') && on('box-shadow') !== 'none'; const onBorder = on('border-top-color');
            const off = await style([]); const borderChange = onBorder !== off('border-top-color');
            await CSS.forcePseudoState({ nodeId, forcedPseudoClasses: [] });
            if (!onOutline && !onShadow) {
                const { node } = await DOM.describeNode({ nodeId }); const attrs = node.attributes || [];
                const id = attrs[attrs.indexOf('id') + 1] || '';
                if (!borderChange && id !== 'command-input') noRing.push(node.nodeName.toLowerCase() + (id ? '#' + id : ''));   // the composer is checked below, by real focus
            }
        }
        assert.deepEqual(noRing, [], 'every control kind shows an outline, a ring or a border change on keyboard focus: ' + noRing.join(', '));
        // The composer: on a phone the box's own border turns accent; on desktop the card around it does. Either must be 3:1 against what is next to it.
        for (const theme of ['dark', 'light']) {
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars(); m.applyTheme('${theme}'); return true; })()`);
        await sleep(150);   // sleep-ok: a scene settles over several answers; there is no single completion event
        const comp = await ev(`(() => { const st = document.createElement('style'); st.textContent = '* { transition: none !important }'; document.head.appendChild(st);
            const t = document.getElementById('command-input'), p = document.getElementById('command-pane'); const parse = c => (c.match(/[\\d.]+/g) || []).map(Number);
            const col = c => { const m = c.match(/color\\(srgb ([\\d.]+) ([\\d.]+) ([\\d.]+)/); return m ? [m[1], m[2], m[3]].map(v => v * 255) : parse(c).slice(0, 3); };
            const lum = ([r, g, b]) => { const f = v => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b); };
            const ratio = (x, y) => { const [a, b] = [lum(x), lum(y)].sort((p, q) => q - p); return (a + 0.05) / (b + 0.05); };
            const read = () => { const ta = getComputedStyle(t), pn = getComputedStyle(p); return { ta: col(ta.borderTopColor), tw: parseFloat(ta.borderTopWidth), pn: col(pn.borderTopColor), pbg: col(pn.backgroundColor), tbg: col(ta.backgroundColor) }; };
            t.blur(); const a = read(); t.focus(); const b = read();
            const taOn = b.tw > 0 && JSON.stringify(a.ta) !== JSON.stringify(b.ta) ? ratio(b.ta, b.tbg[0] === 0 && b.tbg[1] === 0 ? b.pbg : b.tbg) : 0;
            const paneOn = JSON.stringify(a.pn) !== JSON.stringify(b.pn) ? ratio(b.pn, b.pbg) : 0;
            return { taOn: Math.round(taOn * 100) / 100, paneOn: Math.round(paneOn * 100) / 100 }; })()`);
        assert.ok(Math.max(comp.taOn, comp.paneOn) >= 3, `${theme}: the composer shows focus with a 3:1 border change (box ${comp.taOn}, card ${comp.paneOn})`);
        console.log(`   ✓ ${theme}: the composer's focus border is ${Math.max(comp.taOn, comp.paneOn)}:1`);
        }
        console.log(`   ✓ ${marked} control kinds checked`);

        console.log('8. A live update does not take focus from the row it is on');
        const kept = await ev(`(() => { const row = document.querySelector('#live-sessions-list li[data-session-id="docs-bot-id"]'); if (!row) return { err: 'no row' }; row.focus(); const before = document.activeElement === row;
            window.__m.add('late-bot', 'B', 1, { sleeping: false }); T.render.renderLiveSessions(T.state.liveSessions);
            const a = document.activeElement; return { before, id: a && a.dataset && a.dataset.sessionId, inRow: !!(a && a.closest && a.closest('li[data-session-id="docs-bot-id"]')), tag: a && a.tagName }; })()`);
        assert.ok(kept.before, 'the row took focus');
        assert.ok(kept.inRow, `after a list redraw focus is still on that row, not lost to the page (focus is on ${kept.tag} ${kept.id})`);
        console.log('   ✓ focus stays on the focused row through a redraw');

        console.log('\nAll dashboard accessibility checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
