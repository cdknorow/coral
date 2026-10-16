// Dashboard accessibility pass: pins the fixes from the keyboard/labels/contrast audit.
//   1. The top nav is page navigation, not tabs: no tablist/tab roles, no aria-selected, and the open
//      view's button is aria-current="page".
//   2. Muted text (--text-muted) is at least 4.5:1 on the panel backgrounds in both themes.
//   3. Every Settings select and the Analytics time range have an accessible name.
//   4. The Settings dialog is named, takes focus when it opens, keeps Tab inside, and gives focus
//      back when it closes (Escape: to the menu button its opener sat in; Cancel: to the opener).
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
    const { client, Page, ev, waitFor, PRELOAD } = await connect();
    const { Input } = client;
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
    const dialogShown = `getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`;
    const dialogHidden = `getComputedStyle(document.getElementById('settings-modal')).display === 'none'`;

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
        await ev(`(() => { [...document.querySelectorAll('#top-bar-settings-menu .overflow-menu-item')].find(b => /showSettingsModal/.test(b.getAttribute('onclick') || '')).focus(); return true; })()`);
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

        console.log('\nAll dashboard accessibility checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
