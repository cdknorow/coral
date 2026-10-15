// The rendering audit's F1, F2 and F4 (#3622):
//  F1  content an agent, a board post, a chat, a note or a file wrote is never live UI: no form
//      controls or disclosure widgets, no id/name/for/role/data-*, a class only for code
//      highlighting, whatever config the caller passes; a task-list checkbox stays as a disabled
//      one; a file's ids are prefixed (user-content-) and its #fragments still scroll inside it.
//      (Lookalike answer buttons doing nothing when clicked: transcript_tools.test.js, next to a
//      real prompt card.)
//  F2  a notification's link goes only to docs://, an http(s) address or a dashboard route; any
//      other (javascript:, data:, file:, //host, /api/...) shows the label as text and goes nowhere.
//  F4  the Docs tab renders through the same sanitizer and link policy: a link to another doc is a
//      router link (no javascript: href), a remote image doesn't load, a javascript: link is text.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

// Every construct the audit found surviving the old config.
const PAYLOAD = [
    '# Title',
    '',
    '<button class="cni-answer btn btn-primary" data-n="1" data-label="Yes" data-action="select">Yes</button>',
    '<input type="password" name="pw" placeholder="password">',
    '<input type="text" id="command-input" value="x">',
    '<dialog open>a dialog</dialog>',
    '<details open><summary>more</summary>hidden text</details>',
    '<label for="command-input">a label</label>',
    '<div id="command-input" class="chat-needs-input composer-error" data-action="select" data-n="2" role="button" tabindex="0" onclick="alert(1)">fake card</div>',
    '<fieldset><legend>legend</legend>inside</fieldset>',
    '<progress value="1" max="2"></progress><meter value="1"></meter>',
    '<span aria-label="ok" title="t" lang="en">kept text</span>',
    '',
    '- [x] done task',
    '- [ ] open task',
    '',
    '```js',
    'const a = 1;',
    '```',
].join('\n');

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Fetch } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const U = `import((window.__coralStatic || '/static/') + 'utils.js')`;
    try {
        await Page.enable();
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();
        await waitFor(`typeof DOMPurify !== 'undefined' && typeof marked !== 'undefined'`, 'the sanitizer and marked');

        // ── F1: every renderer and caller config ─────────────────────────────
        const audit = await ev(`${U}.then(u => {
            const md = ${JSON.stringify(PAYLOAD)};
            const html = marked.parse(md);
            const outs = {
                'chat (renderMarkdown)': u.renderMarkdown(md, undefined, 'chat'),
                'board (renderMarkdown)': u.renderMarkdown(md, undefined, 'board'),
                'notes (sanitizeMarkdown, no config)': u.sanitizeMarkdown(md, undefined, 'board'),
                'summary (sanitizeHtml, no config)': u.sanitizeHtml(html, undefined, { links: 'board' }),
                'knowledge (its own config)': u.sanitizeHtml(html, { ADD_ATTR: ['data-knowledge-link'] }, { links: 'board', knowledgeLinks: true }),
                'file (renderMarkdownStrict)': u.renderMarkdownStrict(md),
                'file pane (sanitizeHtml raw HTML)': u.sanitizeHtml(html, u.DOMPURIFY_CONFIG, { links: 'file' }),
            };
            const out = {};
            for (const [k, h] of Object.entries(outs)) {
                const box = document.createElement('div');
                box.innerHTML = h;
                const els = [...box.querySelectorAll('*')];
                out[k] = {
                    tags: [...new Set(els.map(e => e.nodeName.toLowerCase()).filter(t => ['button', 'label', 'fieldset', 'legend', 'dialog', 'details', 'summary', 'progress', 'meter', 'select', 'textarea', 'form'].includes(t)))],
                    inputs: [...box.querySelectorAll('input')].map(i => i.type + (i.disabled ? ':disabled' : '') + (i.checked ? ':checked' : '') + ':' + [...i.attributes].map(a => a.name).sort().join(',')),
                    attrs: [...new Set(els.flatMap(e => [...e.attributes].map(a => a.name)).filter(n => /^(name|for|role|tabindex|onclick|data-(?!file-ref-raw|route$|fragment$))/.test(n)))],
                    file: k.startsWith('file'),
                    ids: els.filter(e => e.id).map(e => e.id),
                    classes: [...new Set(els.flatMap(e => [...e.classList]))],
                    text: box.textContent.includes('kept text') && box.textContent.includes('fake card') && box.textContent.includes('hidden text'),
                    shadowed: document.getElementById('command-input') === box.querySelector('#command-input'),
                };
            }
            return out;
        })`);
        for (const [k, r] of Object.entries(audit)) {
            check(`${k}: no form controls or widgets`, r.tags.length === 0, JSON.stringify(r.tags));
            check(`${k}: only the task list's two disabled checkboxes, nothing else on them`,
                JSON.stringify(r.inputs) === JSON.stringify(['checkbox:disabled:checked:checked,disabled,type', 'checkbox:disabled:disabled,type']), JSON.stringify(r.inputs));
            check(`${k}: no name, for, role, tabindex, handler or data-* from the content`, r.attrs.length === 0, JSON.stringify(r.attrs));
            check(`${k}: ${r.file ? "a file's ids (if any) carry the user-content- prefix" : 'no ids'}`, r.file ? r.ids.every(i => i.startsWith('user-content-')) : r.ids.length === 0, JSON.stringify(r.ids));
            check(`${k}: a class only for code highlighting`, r.classes.every(c => /^(language-|hljs|blocked-)/.test(c)), JSON.stringify(r.classes));
            check(`${k}: the text survives`, r.text && !r.shadowed, JSON.stringify(r));
        }
        // ids: only in file mode, prefixed; a #fragment still scrolls inside the file.
        const ids = await ev(`${U}.then(u => {
            const out = {};
            out.board = u.sanitizeHtml('<h2 id="sec">S</h2><a href="#sec">go</a>', undefined, { links: 'board' });
            const box = document.createElement('div');
            box.style.cssText = 'height:80px;overflow:auto';
            const filler = '<p>line</p>'.repeat(40); // the sanitizer drops style, so the height comes from text
            box.innerHTML = u.sanitizeHtml('<a href="#sec">go</a>' + filler + '<h2 id="sec">S</h2>' + filler, u.DOMPURIFY_CONFIG, { links: 'file' });
            document.body.appendChild(box);
            u.keepFragmentsInside(box);
            const before = location.href;
            box.querySelector('a').click();
            out.fileIds = [...box.querySelectorAll('[id]')].map(e => e.id);
            out.scrolled = box.scrollTop > 0;
            out.urlSame = location.href === before;
            box.remove();
            return out;
        })`);
        check('board content keeps no ids', !/ id=/.test(ids.board), ids.board);
        check('the file pane (raw HTML) keeps the id, prefixed', audit['file pane (sanitizeHtml raw HTML)'].ids.includes('user-content-command-input'), JSON.stringify(audit['file pane (sanitizeHtml raw HTML)'].ids));
        check("a file's ids are prefixed, and its #fragment scrolls inside it without changing the URL",
            JSON.stringify(ids.fileIds) === JSON.stringify(['user-content-sec']) && ids.scrolled && ids.urlSame, JSON.stringify(ids));

        // ── F2: notification links ───────────────────────────────────────────
        const targets = await ev(`${U}.then(u => Object.fromEntries([
            'docs://workflow-quickstart', 'https://example.test/x', 'http://localhost:3000/', '/agents', '/board/qa-team',
            'javascript:alert(1)', ' JavaScript:alert(1)', 'data:text/html,x', 'file:///etc/passwd', '//evil.test/x', '/\\\\evil.test',
            '/api/system/api-key', 'https://user:pw@example.test', 'docs://../x', 'vbscript:x', '#/agents',
        ].map(x => [x, u.notificationLinkTarget(x)])))`);
        const good = ['docs://workflow-quickstart', 'https://example.test/x', 'http://localhost:3000/', '/agents', '/board/qa-team'];
        for (const [u, t] of Object.entries(targets)) {
            if (good.includes(u)) check(`notification link ${u} is allowed`, !!t, JSON.stringify(t));
            else check(`notification link ${JSON.stringify(u)} is refused`, t === null, JSON.stringify(t));
        }
        const shown = await ev(`${U}.then(async u => {
            window.__opened = [];
            const realOpen = window.open;
            window.open = (...a) => { window.__opened.push(a); return null; };
            const before = location.href;
            const out = [];
            for (const url of ['javascript:window.__pwned=1', 'data:text/html,x', '//evil.test/x', 'https://example.test/x']) {
                u.showAlertNotification('Title', 'Message', 'info', { label: 'Open it', url });
                const ov = [...document.querySelectorAll('.alert-notification-overlay')].pop();
                const link = ov.querySelector('.alert-notification-link');
                const r = { url, tag: link.nodeName, href: link.getAttribute('href'), text: link.textContent };
                link.click();
                await new Promise(res => setTimeout(res, 50)); // sleep-ok: let a navigation start
                r.overlayGone = !ov.isConnected;
                if (ov.isConnected) ov.remove();
                out.push(r);
            }
            window.open = realOpen;
            return { out, opened: window.__opened, pwned: !!window.__pwned, urlSame: location.href === before };
        })`);
        for (const r of shown.out.slice(0, 3)) {
            check(`a ${r.url.split(':')[0] || r.url} notification link shows its label as text and does nothing`, r.tag === 'SPAN' && r.href === null && r.text === 'Open it' && !r.overlayGone, JSON.stringify(r));
        }
        check('an https notification link opens a new tab with no opener', shown.out[3].tag === 'A' && shown.out[3].overlayGone
            && shown.opened.length === 1 && shown.opened[0][0] === 'https://example.test/x' && shown.opened[0][1] === '_blank' && /noopener/.test(shown.opened[0][2]), JSON.stringify(shown));
        check('no notification link ran script or moved the page', !shown.pwned && shown.urlSame, JSON.stringify(shown));

        // ── F4: the Docs tab ─────────────────────────────────────────────────
        const DOC = '# QA doc\n\nSee [the board](board.md#tasks), [auth](auth.md), [the web](https://example.test/), [bad](javascript:alert(1)).\n\n![remote](https://evil.test/beacon.png)\n\n<button class="cni-answer" data-n="1">Yes</button>\n';
        await Fetch.enable({ patterns: [{ urlPattern: '*/api/agent-docs/qa-3622*' }] });
        Fetch.requestPaused(({ requestId }) => Fetch.fulfillRequest({ requestId, responseCode: 200,
            responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
            body: Buffer.from(JSON.stringify({ name: 'qa-3622', content: DOC })).toString('base64') }).catch(() => {}));
        // The tab opens README first (its list loads, then README); the QA doc after that.
        await ev(`window.switchNavTab('docs'); true`);
        await waitFor(`!!document.querySelector('#docs-content h1')`, 'the Docs tab with its first doc');
        await ev(`(async () => { const l = await import((window.__coralStatic || '/static/') + 'lazy_panels.js'); const m = await l.loadDocs(); await m.selectDoc('qa-3622'); return true; })()`);
        await waitFor(`!!document.querySelector('#docs-content h1') && document.querySelector('#docs-content h1').textContent === 'QA doc'`, 'the QA doc');
        const doc = await ev(`(() => { const c = document.getElementById('docs-content');
            return { links: [...c.querySelectorAll('a')].map(a => [a.textContent, a.getAttribute('href'), a.hasAttribute('data-route')]),
                js: !!c.querySelector('a[href^="javascript:"]'), imgs: [...c.querySelectorAll('img')].map(i => i.getAttribute('src')),
                buttons: c.querySelectorAll('button').length, blocked: [...c.querySelectorAll('.blocked-link')].map(s => s.textContent) }; })()`);
        check('Docs: a link to another doc is a router link to its page', JSON.stringify(doc.links.filter(l => l[2])) === JSON.stringify([['the board', '/docs/board', true], ['auth', '/docs/auth', true]]), JSON.stringify(doc.links));
        check('Docs: a web link stays a link, a javascript: link is text, no javascript: href anywhere',
            doc.links.some(l => l[1] === 'https://example.test/') && !doc.js && doc.blocked.some(t => t.includes('bad')), JSON.stringify(doc));
        check('Docs: a remote image never loads, and no button renders', doc.imgs.every(s => !s || !/^https?:/.test(s)) && doc.buttons === 0, JSON.stringify(doc));
        await ev(`document.querySelector('#docs-content a[href="/docs/auth"]').click(); true`);
        await waitFor(`location.pathname === '/docs/auth'`, 'the doc link opens that doc through the router', { timeout: 8000, soft: true });
        check('Docs: clicking a doc link opens that doc in the dashboard', await ev(`location.pathname`) === '/docs/auth', await ev(`location.pathname`));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
