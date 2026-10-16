// CSP stage 2, slice 8 (#3645): the page shell in index.html and the last generated markup with
// inline handlers (folder tags, custom views, the folder browser, the theme editor, the update
// toast, docs) on delegated data-on-* actions.
//  - Census: every data-on-* element in the loaded page names a registered action, and no element
//    in the page has an inline on* handler.
//  - The page shell: a nav tab passes its tab (an unlisted one does nothing); the settings menu's
//    items close the menu first; the tmux banner's close hides it; the empty state's launch
//    buttons pass their type.
//  - Generated markup gets its values as data, never re-escaped into a script: a folder named
//    with quotes, markup and an entity reaches the folder tag functions unchanged; a folder browser
//    entry and a recent folder pass their names unchanged; an update toast's Dismiss passes its
//    version; a doc link passes its doc.
//  - The theme editor's inputs and buttons reach their functions; Delete still asks first.
//  - The leftover tmux banner's End is on the shared destructive list.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what, ms = 8000) => waitFor(expr, what, { timeout: ms, soft: true });
    const MOD = `(window.__coralStatic||'/static/')`;
    // Run fn with window[name] replaced by a recorder; returns the calls.
    const spy = (names, body) => ev(`(async () => { const calls = []; const keep = {};
        // A name under the test hooks (window.__coral) or on window itself.
        const where = n => (window.__coral && n in window.__coral) ? window.__coral : window;
        for (const n of ${JSON.stringify(names)}) { const o = where(n); keep[n] = [o, o[n]]; o[n] = (...a) => { calls.push([n, ...a.map(x => (x && x.nodeType) ? 'el' : x)]); }; }
        try { ${body} } finally { for (const n of ${JSON.stringify(names)}) keep[n][0][n] = keep[n][1]; }
        return calls; })()`);
    const HOSTILE = `a'b"c<img src=x>&amp;d`;
    try {
        await Page.enable();
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();

        // ── Census ─────────────────────────────────────────────────────────
        const census = await ev(`import(${MOD} + 'actions.js').then(a => {
            const names = new Set(); const inline = [];
            for (const el of document.querySelectorAll('*')) for (const at of el.attributes) {
                if (at.name.startsWith('data-on-')) names.add(at.value);
                if (/^on/i.test(at.name)) inline.push((el.id || el.tagName) + ' ' + at.name);
            }
            return { count: names.size, missing: [...names].filter(n => !a.hasAction(n)), inline: inline.slice(0, 8) };
        })`);
        check('every data-on-* control in the page names a registered action', census.count > 50 && census.missing.length === 0, JSON.stringify({ count: census.count, missing: census.missing }));
        check('no element in the page has an inline handler', census.inline.length === 0, JSON.stringify(census.inline));

        // ── The page shell ─────────────────────────────────────────────────
        const nav = await spy(['switchNavTab'], `
            for (const b of document.querySelectorAll('[data-on-click="m-switch-nav-tab"]')) b.click();
            const bad = document.createElement('button'); bad.setAttribute('data-on-click', 'm-switch-nav-tab'); bad.dataset.arg = 'agents; x'; document.body.appendChild(bad); bad.click(); bad.remove();`);
        check('a nav tab passes its tab; an unlisted one does nothing', nav.length > 2 && nav.every(c => /^[a-z]+$/.test(c[1])), JSON.stringify(nav));
        const menu = await spy(['closeTopBarSettings', 'showSettingsModal'], `
            document.querySelector('[data-on-click="m-close-top-bar-settings-then-show-settings-modal"]').click();`);
        check('a settings menu item closes the menu, then opens its dialog', JSON.stringify(menu) === JSON.stringify([['closeTopBarSettings'], ['showSettingsModal']]), JSON.stringify(menu));
        const banner = await ev(`(() => { const b = document.getElementById('tmux-missing-banner'); if (!b) return null; const keep = b.style.display; b.style.display = 'block';
            b.querySelector('[data-on-click="m-tmux-banner-close"]').click(); const d = b.style.display; b.style.display = keep; return d; })()`);
        check('the tmux banner’s close hides it', banner === 'none', String(banner));
        const launch = await spy(['showLaunchModal', '_selectLaunchType'], `
            for (const b of document.querySelectorAll('[data-on-click="m-show-launch-modal-then-select-launch-type"]')) b.click();
            const bad = document.createElement('button'); bad.setAttribute('data-on-click', 'm-show-launch-modal-then-select-launch-type'); bad.dataset.arg = 'root'; document.body.appendChild(bad); bad.click(); bad.remove();`);
        const types = launch.filter(c => c[0] === '_selectLaunchType').map(c => c[1]);
        check('the empty state’s launch buttons open the dialog with their type; an unlisted type does nothing',
            types.length > 0 && types.every(t => t === 'agent' || t === 'team') && launch.filter(c => c[0] === 'showLaunchModal').length === types.length, JSON.stringify(launch));

        // ── Folder tags: the folder name arrives as written ────────────────
        const folder = await spy(['removeFolderTag', 'addFolderTag', 'createAndAddFolderTag'], `
            const ft = await import(${MOD} + 'folder_tags.js');
            const anchor = document.createElement('div'); document.body.appendChild(anchor);
            await ft.showFolderTagDropdown(${JSON.stringify(HOSTILE)}, anchor); anchor.remove();
            const dd = document.getElementById('folder-tag-dropdown');
            const input = dd.querySelector('[data-on-keydown="folder-tag-new-key"]');
            input.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', bubbles: true }));
            input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
            dd.querySelector('[data-on-click="folder-tag-create"]').click();
            // A chip as the dropdown renders one for a tag on the folder.
            for (const [action, id] of [['folder-tag-remove', '7'], ['folder-tag-add', '8'], ['folder-tag-add', '0']]) {
                const s = document.createElement('span'); s.setAttribute('data-on-click', action); s.dataset.folder = input.dataset.folder; s.dataset.tagId = id; dd.appendChild(s); s.click(); }
            ft.hideFolderTagDropdown();`);
        check('a folder named with quotes, markup and an entity reaches the folder tag functions unchanged (a bad tag id does nothing)',
            JSON.stringify(folder) === JSON.stringify([['createAndAddFolderTag', HOSTILE], ['createAndAddFolderTag', HOSTILE], ['removeFolderTag', HOSTILE, 7], ['addFolderTag', HOSTILE, 8]]), JSON.stringify(folder));

        // ── The folder browser ─────────────────────────────────────────────
        const browse = await spy(['browserNavigateTo', 'browserSelectRecent'], `
            const host = document.createElement('div'); document.body.appendChild(host);
            for (const [action, key] of [['browser-nav', 'name'], ['browser-recent', 'dir']]) {
                const li = document.createElement('li'); li.setAttribute('data-on-click', action); li.dataset[key] = ${JSON.stringify(HOSTILE)}; host.appendChild(li); li.click(); }
            host.remove();`);
        check('a folder browser entry and a recent folder pass their names unchanged',
            JSON.stringify(browse) === JSON.stringify([['browserNavigateTo', HOSTILE], ['browserSelectRecent', HOSTILE]]), JSON.stringify(browse));

        // ── The update toast and docs ──────────────────────────────────────
        const misc = await spy(['dismissUpdateToast', 'selectDoc'], `
            await import(${MOD} + 'docs.js'); // loaded with the Docs tab, before its links exist
            const host = document.createElement('div'); document.body.appendChild(host);
            for (const [action, key, v] of [['update-dismiss', 'version', '9.9.9'], ['doc-open', 'doc', 'getting-started'], ['update-dismiss', 'version', '']]) {
                const b = document.createElement('button'); b.setAttribute('data-on-click', action); b.dataset[key] = v; host.appendChild(b); b.click(); }
            host.remove();`);
        check('the update toast’s Dismiss passes its version, a doc link its doc (an empty value does nothing)',
            JSON.stringify(misc) === JSON.stringify([['dismissUpdateToast', '9.9.9'], ['selectDoc', 'getting-started']]), JSON.stringify(misc));

        // ── The theme editor ───────────────────────────────────────────────
        await ev(`window.showThemeConfigurator(); true`);
        await until(`!!document.querySelector('#theme-configurator-modal [data-on-input="theme-var"]')`, 'the theme editor');
        const theme = await spy(['updateThemeVar', 'updateThemeVarText', 'previewTheme', 'saveAndApplyTheme', 'exportCurrentTheme'], `
            const m = document.getElementById('theme-configurator-modal');
            m.querySelector('[data-on-input="theme-var"]').dispatchEvent(new Event('input', { bubbles: true }));
            m.querySelector('[data-on-input="theme-var-text"]').dispatchEvent(new Event('input', { bubbles: true }));
            for (const a of ['theme-preview', 'theme-save', 'theme-export']) m.querySelector('[data-on-click="' + a + '"]').click();`);
        check('the theme editor’s inputs and buttons reach their functions (an input passes itself)',
            JSON.stringify(theme) === JSON.stringify([['updateThemeVar', 'el'], ['updateThemeVarText', 'el'], ['previewTheme'], ['saveAndApplyTheme'], ['exportCurrentTheme']]), JSON.stringify(theme));
        const del = await spy(['showConfirmModal'], `
            const sel = document.getElementById('theme-load-select'); const o = document.createElement('option'); o.value = 'qa-3645'; sel.appendChild(o); sel.value = 'qa-3645';
            document.querySelector('#theme-configurator-modal [data-on-click="theme-delete"]').click(); o.remove();`);
        check('the theme editor’s Delete still asks first', del.length === 1 && del[0][1] === 'Delete Theme', JSON.stringify(del.map(c => c.slice(0, 2))));
        await ev(`document.querySelector('#theme-configurator-modal [data-on-click="theme-close"]').click(); true`);
        check('the theme editor’s Cancel closes it', await ev(`getComputedStyle(document.getElementById('theme-configurator-modal')).display === 'none'`));

        const listed = await ev(`import(${MOD} + 'actions.js').then(a => a.DESTRUCTIVE_ACTIONS.has('m-end-leftover-tmux'))`);
        check('the leftover tmux banner’s End is on the shared destructive list', listed);
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
