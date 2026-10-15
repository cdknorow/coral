// #3542: links in agent-written Markdown (board posts, chat, tasks, notes) never move the
// dashboard tab. A web link opens a new tab; mailto and Coral artifact links stay; a dashboard
// page link stays only when the router accepts it, rebuilt from the parsed route; in chat a
// relative path becomes a file reference with NO href; everything else (/api/..., ../x, a hash
// route, file:, vbscript:, javascript:, another coral: link) is text with its target shown, and
// clicking it leaves the page where it is. agent_text_links.test.js guards that every caller
// passes a mode.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');

const BASE = require('./test_base').BASE;
const HEX = 'd'.repeat(64);

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation, Target } = client;
    const ev = async expression => {
        const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
        return result.result.value;
    };
    const { coralReady } = require('./helpers/wait').bind(ev);
    const S = `(window.__coralStatic||'/static/')`;
    let failures = 0;
    const check = (label, cond, detail) => { console.log(`${cond ? '  ✓' : '  ✗'} ${label}${cond ? '' : ' — ' + detail}`); if (!cond) failures++; };
    const md = [
        '[k](/api/system/api-key) [r](../x.md) [f](#team-view=evil) [t](/team/qa?x=1#evil) [c](/chats?q=hi&page=1&x=2)',
        '[h](https://e.test/p) [m](mailto:a@b.test) [a](coral://artifacts/' + HEX + ') [fs](file:///etc/hosts) [v](vbscript:msgbox) [js](javascript:alert(1)) [cl](coral://agents/x) [src](src/main.go) [abs](/tmp/proj/a.go)',
        // Raw HTML an agent could write, carrying the markers only Coral's own code may set.
        'x <a href="#" data-knowledge-link="Worker">kl</a> <span data-file-ref="/etc/hosts">fr</span> <a data-file-ref-raw="/etc/hosts">raw</a> <a href="/api/x" data-route="">dr</a> <span data-fragment="top">fg</span>',
    ].join('\n\n');
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Emulation.setDeviceMetricsOverride({ width: 1300, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await coralReady();

        const render = mode => ev(`(async () => {
            const u = await import(${S}+'utils.js');
            const host = document.getElementById('live-history-messages') || document.body;
            const box = document.createElement('div');
            box.id = 'qa-links-${mode}';
            box.innerHTML = u.renderMarkdown(${JSON.stringify(md)}, undefined, ${JSON.stringify(mode)});
            host.appendChild(box);
            if (${JSON.stringify(mode)} === 'chat') (await import(${S}+'live_chat.js')).linkifyRefs(box);
            const out = { links: {}, text: [...box.querySelectorAll('.blocked-link')].map(s => s.textContent) };
            for (const a of box.querySelectorAll('a')) out.links[a.textContent] = { href: a.getAttribute('href'), target: a.target, rel: a.rel, fileRef: a.dataset.fileRef || null, route: a.hasAttribute('data-route') };
            return out;
        })()`);

        for (const mode of ['board', 'chat']) {
            console.log(`Mode '${mode}'`);
            const r = await render(mode);
            const L = r.links;
            check('a web link opens a new tab (noopener noreferrer)', L.h && L.h.href === 'https://e.test/p' && L.h.target === '_blank' && L.h.rel === 'noopener noreferrer', JSON.stringify(L.h));
            check('mailto and the Coral artifact link stay', L.m && L.m.href === 'mailto:a@b.test' && L.a && L.a.href === `coral://artifacts/${HEX}`, JSON.stringify([L.m, L.a]));
            check('a dashboard page link is rebuilt from the parsed route (no stray query, no hash)', L.t && L.t.href === '/team/qa' && L.t.route && L.c && L.c.href === '/chats?q=hi', JSON.stringify([L.t, L.c]));
            for (const [label, target] of [['k', '/api/system/api-key'], ['r', '../x.md'], ['f', '#team-view=evil'], ['fs', 'file:///etc/hosts'], ['cl', 'coral://agents/x']]) {
                if (mode === 'chat' && (label === 'r' || label === 'fs' || label === 'k')) continue; // file-reference candidates in chat, below
                check(`[${label}](${target}) is text with its target shown`, !L[label] && r.text.includes(`${label} (${target})`), JSON.stringify({ link: L[label], text: r.text }));
            }
            check('javascript: and vbscript: (dropped by DOMPurify) leave plain text, nothing link-like', !L.js && !L.v && r.text.includes('js') && r.text.includes('v'), JSON.stringify({ js: L.js, v: L.v, text: r.text }));
            if (mode === 'board') {
                check('[src](src/main.go) and [abs](/tmp/proj/a.go) are text on a board', !L.src && !L.abs && r.text.includes('src (src/main.go)') && r.text.includes('abs (/tmp/proj/a.go)'), JSON.stringify({ src: L.src, abs: L.abs }));
            } else {
                check('in chat, a relative file link is a file reference with NO href', L.src && L.src.href === null && L.src.fileRef === 'src/main.go' && L.abs && L.abs.href === null && L.abs.fileRef === '/tmp/proj/a.go', JSON.stringify({ src: L.src, abs: L.abs }));
                check('../x.md and file: links become file references with no href, too', L.r && L.r.href === null && L.r.fileRef && L.fs && L.fs.href === null && L.fs.fileRef, JSON.stringify({ r: L.r, fs: L.fs }));
                check('no anchor in the chat message has a relative href left', Object.values(L).every(x => x.href === null || /^(https?:|mailto:|coral:\/\/artifacts\/|\/(team|chats)\b)/.test(x.href)), JSON.stringify(L));
            }

            const planted = await ev(`(() => { const box = document.getElementById('qa-links-${mode}'); const els = [...box.querySelectorAll('*')].filter(e => ['kl', 'fr', 'raw', 'dr', 'fg'].includes(e.textContent.trim().split(' ')[0])); return { markers: els.filter(e => ['data-knowledge-link', 'data-file-ref-raw', 'data-route', 'data-fragment'].some(a => e.hasAttribute(a)) || (e.hasAttribute('data-file-ref') && e.textContent.trim().startsWith('fr'))).map(e => e.outerHTML), anchors: els.filter(e => e.tagName === 'A' && e.hasAttribute('href')).map(e => e.outerHTML) }; })()`);
            check('raw HTML carrying Coral-only markers is stripped of them (no file ref, knowledge link, route or fragment)', planted.markers.length === 0 && planted.anchors.length === 0, JSON.stringify(planted));
            const menu = await ev(`(async () => { const box = document.getElementById('qa-links-${mode}'); for (const e of box.querySelectorAll('*')) if (['kl', 'fr', 'raw', 'dr', 'fg'].includes(e.textContent.trim().split(' ')[0])) e.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 })); await new Promise(r => setTimeout(r, 300)); /* sleep-ok: proves a negative, a menu would open within this */ const m = !!document.querySelector('.chat-file-menu'); document.querySelector('.chat-file-menu')?.remove(); return m; })()`);
            check('clicking them opens no file menu', !menu, 'a file menu opened');

            console.log('  Clicking every blocked item and file reference leaves the page where it is');
            const before = await ev('location.href');
            const targetsBefore = (await Target.getTargets()).targetInfos.filter(t => t.type === 'page').length;
            const moved = await ev(`(async () => {
                const box = document.getElementById('qa-links-${mode}');
                const click = (el, extra) => el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ...(extra || {}) }));
                for (const el of box.querySelectorAll('.blocked-link, a[data-file-ref]')) { click(el); click(el, { ctrlKey: true }); click(el, { button: 1 }); }
                document.querySelector('.chat-file-menu')?.remove();
                await new Promise(r => setTimeout(r, 500)); // sleep-ok: proves a negative, a navigation or new tab would start within this
                return location.href;
            })()`);
            const targetsAfter = (await Target.getTargets()).targetInfos.filter(t => t.type === 'page').length;
            check('location.href is unchanged', moved === before, `${before} -> ${moved}`);
            check('no new tab opened (plain, ctrl and middle clicks)', targetsAfter === targetsBefore, `${targetsBefore} -> ${targetsAfter}`);
            await ev(`document.getElementById('qa-links-${mode}').remove()`);
        }
    } catch (e) {
        console.error(e);
        failures++;
    } finally {
        await client.close();
    }
    console.log(failures ? `\n${failures} check(s) failed` : '\nall checks passed');
    process.exit(failures ? 1 : 0);
})();
