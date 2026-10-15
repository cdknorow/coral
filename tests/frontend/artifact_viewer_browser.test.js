// #3540: a Markdown artifact linked from a board post is previewed in the dashboard (rendered,
// sanitized), not downloaded. The strict renderer shows raw HTML as text; a remote image is a
// link and is never requested; the dialog's title is text; Download keeps the original bytes;
// a modified click keeps the link's new tab; an artifact image opens the same viewer; Escape
// closes and focus returns. The artifact bytes come from a stubbed fetch (the server's
// artifact store needs an agent session to upload; board_auto_attach_e2e covers that side).
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');

const BASE = require('./test_base').BASE;
const MD_HEX = 'b'.repeat(64);
const IMG_HEX = 'c'.repeat(64);
const PNG_B64 = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==';
const MD = [
    '# Report title',
    '',
    'Some **bold** text.',
    '',
    '```js',
    'const x = 1;',
    '```',
    '',
    '| a | b |',
    '|---|---|',
    '| 1 | 2 |',
    '',
    '![remote](https://x.test/beacon.png?d=SECRET)',
    '',
    '<script>window.__pwned = 1</script>',
    '',
    '<img src="x" onerror="window.__pwned = 2">',
    '',
    'Inline <b>raw</b> html.',
    '',
    '[k](/api/system/api-key) [r](../x.md) [f](#sec) [m](mailto:a@b.test) [w](https://example.test/p) [img](coral://artifacts/' + 'c'.repeat(64) + ')',
].join('\n');

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network, Emulation, Fetch } = client;
    const ev = async expression => {
        const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
        return result.result.value;
    };
    const { coralReady } = require('./helpers/wait').bind(ev);
    const S = `(window.__coralStatic||'/static/')`;
    const requested = [];
    let failures = 0;
    const check = (label, cond, detail) => { console.log(`${cond ? '  ✓' : '  ✗'} ${label}${cond ? '' : ' — ' + detail}`); if (!cond) failures++; };
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        Network.requestWillBeSent(p => requested.push(p.request.url));
        // The image artifact's bytes, for the <img> the viewer shows (a page fetch stub can't serve an <img>).
        await Fetch.enable({ patterns: [{ urlPattern: `*/api/artifacts/${IMG_HEX}*` }] });
        Fetch.requestPaused(p => Fetch.fulfillRequest({ requestId: p.requestId, responseCode: 200,
            responseHeaders: [{ name: 'Content-Type', value: 'image/png' }], body: PNG_B64 }).catch(() => {}));
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await coralReady();

        // The artifact store, stubbed: the Markdown file and a PNG.
        await ev(`(() => {
            const real = window.fetch;
            const png = Uint8Array.from(atob(${JSON.stringify(PNG_B64)}), c => c.charCodeAt(0));
            window.fetch = (input, init) => {
                const u = String(input && input.url ? input.url : input);
                if (u.endsWith('/api/artifacts/${MD_HEX}?v=2')) return Promise.resolve(new Response(${JSON.stringify(MD)}, { headers: { 'Content-Type': 'text/markdown; charset=utf-8', 'Content-Disposition': 'attachment; filename="report.md"' } }));
                if (u.endsWith('/api/artifacts/${IMG_HEX}?v=2')) return Promise.resolve(new Response(png, { headers: { 'Content-Type': 'image/png' } }));
                return real(input, init);
            };
            return true;
        })()`);

        console.log('A board post with a Markdown artifact link and an artifact image');
        const post = `[<img src=x onerror="window.__pwned=3">report](coral://artifacts/${MD_HEX}) and ![shot](coral://artifacts/${IMG_HEX})`;
        await ev(`(async () => {
            const u = await import(${S}+'utils.js');
            const b = await import(${S}+'board_artifacts.js');
            const box = document.createElement('div');
            box.id = 'qa-3540';
            box.innerHTML = '<div class="mb-message-body">' + u.renderMarkdown(${JSON.stringify(post)}) + '</div>';
            document.body.appendChild(box);
            b.resolveBoardArtifacts(box, 'local');
            return true;
        })()`);
        const link = await ev(`(() => { const a = document.querySelector('#qa-3540 a.board-artifact-link'); return a && { href: a.getAttribute('href'), target: a.target, rel: a.rel, text: a.textContent, imgs: a.querySelectorAll('img').length }; })()`);
        check('the link points at the artifact and keeps its new-tab fallback', link && link.href === `/api/artifacts/${MD_HEX}?v=2` && link.target === '_blank' && link.rel === 'noopener noreferrer', JSON.stringify(link));

        console.log('A modified click keeps the browser behaviour (no viewer)');
        const modified = await ev(`(() => {
            const a = document.querySelector('#qa-3540 a.board-artifact-link');
            const e = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ctrlKey: true });
            a.addEventListener('click', ev => ev.preventDefault(), { once: true }); // keep the test page from opening a tab
            a.dispatchEvent(e);
            return { opened: !!document.querySelector('#artifact-viewer:not([hidden])') };
        })()`);
        await new Promise(r => setTimeout(r, 400)); // sleep-ok: proves a negative, the viewer would open within this
        check('ctrl+click does not open the viewer', !modified.opened && !(await ev(`!!document.querySelector('#artifact-viewer:not([hidden])')`)), JSON.stringify(modified));

        console.log('A plain click opens the viewer with the rendered, sanitized Markdown');
        await ev(`(() => { const a = document.querySelector('#qa-3540 a.board-artifact-link'); a.focus(); a.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 })); return true; })()`);
        let v = null;
        for (let i = 0; i < 50 && !(v && v.h1); i++) {
            await new Promise(r => setTimeout(r, 100)); // sleep-ok: polls for the async render
            v = await ev(`(() => {
                const d = document.querySelector('#artifact-viewer');
                if (!d || d.hidden) return null;
                const body = d.querySelector('.artifact-viewer-body');
                return {
                    title: d.querySelector('.artifact-viewer-title').textContent,
                    titleElements: d.querySelector('.artifact-viewer-title').children.length,
                    h1: body.querySelector('h1')?.textContent || null,
                    bold: !!body.querySelector('strong'),
                    code: body.querySelector('pre code')?.textContent || null,
                    table: body.querySelectorAll('table td').length,
                    scripts: body.querySelectorAll('script').length,
                    imgsWithHandlers: [...body.querySelectorAll('img')].filter(i => [...i.attributes].some(a => /^on/i.test(a.name))).length,
                    imgs: body.querySelectorAll('img').length,
                    rawScriptAsText: body.textContent.includes('<script>window.__pwned = 1</script>'),
                    rawImgAsText: body.textContent.includes('onerror'),
                    inlineRawAsText: body.textContent.includes('<b>raw</b>'),
                    remote: [...body.querySelectorAll('.blocked-remote-image')].map(e => ({ tag: e.tagName, href: e.getAttribute('href') })),
                    dlHref: d.querySelector('.artifact-viewer-download').getAttribute('href'),
                    dlName: d.querySelector('.artifact-viewer-download').getAttribute('download'),
                    focus: document.activeElement === d.querySelector('.artifact-viewer-close'),
                    role: d.getAttribute('role'), modal: d.getAttribute('aria-modal'),
                };
            })()`);
        }
        check('the dialog opens (role=dialog, aria-modal) with focus on Close', v && v.role === 'dialog' && v.modal === 'true' && v.focus, JSON.stringify(v));
        check('the heading, bold text, code block and table render', v && v.h1 === 'Report title' && v.bold && v.code === 'const x = 1;\n' && v.table === 2, JSON.stringify(v));
        check('raw HTML is shown as text: no script, no img, no handler', v && v.scripts === 0 && v.imgs === 0 && v.imgsWithHandlers === 0 && v.rawScriptAsText && v.rawImgAsText && v.inlineRawAsText, JSON.stringify(v));
        check('a remote image is a link, never an image', v && v.remote.length === 1 && v.remote[0].tag === 'A' && v.remote[0].href === 'https://x.test/beacon.png?d=SECRET', JSON.stringify(v && v.remote));
        check('the title is text, even with markup in the link name', v && v.titleElements === 0 && !/onerror/.test(v.title), JSON.stringify(v && v.title));
        check('Download keeps the stored file (its own name)', v && v.dlHref === `/api/artifacts/${MD_HEX}?v=2` && v.dlName === '', JSON.stringify(v));
        check('no script ran', (await ev('window.__pwned === undefined')), String(await ev('window.__pwned')));
        check('the remote image was never requested', !requested.some(u => u.includes('x.test')), JSON.stringify(requested.filter(u => u.includes('x.test'))));

        console.log('Links in the file: only web, mailto and artifact links stay links, and none moves the page');
        const links = await ev(`(() => {
            const b = document.querySelector('#artifact-viewer .artifact-viewer-body');
            return {
                anchors: [...b.querySelectorAll('a')].map(a => ({ href: a.getAttribute('href'), target: a.target, frag: a.getAttribute('data-fragment') })).filter(x => !String(x.href).startsWith('https://x.test')),
                blocked: [...b.querySelectorAll('.blocked-link')].map(s => s.textContent),
            };
        })()`);
        check('root-relative and relative links are text with their target shown', links.blocked.includes('k (/api/system/api-key)') && links.blocked.includes('r (../x.md)'), JSON.stringify(links));
        check('mailto, web (new tab), #fragment and artifact links stay links', ['mailto:a@b.test', 'https://example.test/p', '#sec', 'coral://artifacts/' + 'c'.repeat(64)].every(h => links.anchors.some(a => a.href === h)) && links.anchors.find(a => a.href === 'https://example.test/p').target === '_blank', JSON.stringify(links.anchors));
        const before = await ev('location.href');
        const moved = await ev(`(async () => {
            const b = document.querySelector('#artifact-viewer .artifact-viewer-body');
            const click = el => el && el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
            for (const s of b.querySelectorAll('.blocked-link')) click(s);
            click(b.querySelector('a[data-fragment]'));
            await new Promise(r => setTimeout(r, 300)); // sleep-ok: proves a negative, a navigation would start within this
            return location.href;
        })()`);
        check('clicking them leaves the page URL unchanged', moved === before, before + ' -> ' + moved);

        console.log('Escape over another dialog closes only the viewer');
        await ev(`(() => { const m = document.createElement('div'); m.id = 'qa-under'; m.className = 'modal'; m.style.display = 'flex'; m.innerHTML = '<div class="modal-content">under</div>'; document.body.insertBefore(m, document.getElementById('artifact-viewer')); return true; })()`);

        console.log('Escape closes the viewer and focus returns to the link');
        await ev(`document.querySelector('#artifact-viewer').dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))`);
        const closed = await ev(`({ hidden: document.querySelector('#artifact-viewer').hidden, empty: !document.querySelector('#artifact-viewer .artifact-viewer-body').childElementCount, back: document.activeElement === document.querySelector('#qa-3540 a.board-artifact-link') })`);
        check('closed, emptied, focus back on the link', closed.hidden && closed.empty && closed.back, JSON.stringify(closed));
        const under = await ev(`(() => { const m = document.getElementById('qa-under'); const open = !!m && m.style.display !== 'none' && !m.hidden && m.isConnected; m?.remove(); return open; })()`);
        check('the dialog under it is still open (Escape stopped at the viewer)', under, 'it was closed too');

        console.log('An artifact link inside the file opens that artifact in the viewer');
        await ev(`(() => { const a = [...document.querySelectorAll('#qa-3540 a.board-artifact-link')][0]; a.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 })); return true; })()`);
        for (let i = 0; i < 50 && !(await ev(`!!document.querySelector('#artifact-viewer:not([hidden]) .artifact-viewer-body a[href^="coral://artifacts/"]')`)); i++) await new Promise(r => setTimeout(r, 100)); // sleep-ok: polls for the render
        await ev(`document.querySelector('#artifact-viewer .artifact-viewer-body a[href^="coral://artifacts/"]').dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }))`);
        let inner = null;
        for (let i = 0; i < 50 && !(inner && inner.img); i++) { await new Promise(r => setTimeout(r, 100)); inner = await ev(`(() => { const i = document.querySelector('#artifact-viewer:not([hidden]) .artifact-viewer-body img'); return { img: !!(i && i.naturalWidth), title: document.querySelector('#artifact-viewer .artifact-viewer-title').textContent, url: location.href }; })()`); } // sleep-ok: polls for the render
        check('it shows the linked image, the page URL unchanged', inner && inner.img && inner.title === 'img' && inner.url === before, JSON.stringify(inner));
        await ev(`document.querySelector('#artifact-viewer .artifact-viewer-close').click()`);

        console.log('An artifact image in a post opens the same viewer, as an image');
        await ev(`document.querySelector('#qa-3540 img.board-artifact-image').dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }))`);
        let im = null;
        for (let i = 0; i < 50 && !(im && im.img); i++) {
            await new Promise(r => setTimeout(r, 100)); // sleep-ok: polls for the async render
            im = await ev(`(() => { const d = document.querySelector('#artifact-viewer'); if (!d || d.hidden) return null; const i = d.querySelector('.artifact-viewer-body img'); return { img: !!(i && i.naturalWidth), src: i && i.getAttribute('src'), title: d.querySelector('.artifact-viewer-title').textContent }; })()`);
        }
        check('the image shows in the viewer', im && im.img && /\/api\/artifacts\/c{64}\?v=2$/.test(im.src || ''), JSON.stringify(im));
        await ev(`document.querySelector('#artifact-viewer .artifact-viewer-close').click()`);
        check('Close hides it', await ev(`document.querySelector('#artifact-viewer').hidden`), 'still open');

        console.log('The strict renderer on its own');
        const strict = await ev(`(async () => { const u = await import(${S}+'utils.js'); const box = document.createElement('div'); box.innerHTML = u.renderMarkdownStrict('<script>x()</script>\\n\\n<iframe src="https://x.test"></iframe>\\n\\n[js](javascript:alert(1))'); return { scripts: box.querySelectorAll('script, iframe').length, text: box.textContent, js: /javascript:/i.test(box.innerHTML) }; })()`);
        check('script and iframe are text; javascript: links are dropped', strict.scripts === 0 && strict.text.includes('<script>x()</script>') && strict.text.includes('<iframe') && !strict.js, JSON.stringify(strict));
    } catch (e) {
        console.error(e);
        failures++;
    } finally {
        await client.close();
    }
    console.log(failures ? `\n${failures} check(s) failed` : '\nall checks passed');
    process.exit(failures ? 1 : 0);
})();
