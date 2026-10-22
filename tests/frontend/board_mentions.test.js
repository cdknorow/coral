// @mentions in board posts (#5964): a mention of a current board member is wrapped after the Markdown is rendered. jsdom, no browser: node board_mentions.test.js
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');
const { JSDOM } = require('jsdom');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
    const dom = new JSDOM('<!doctype html><body></body>');
    for (const k of ['window', 'document', 'NodeFilter']) Object.defineProperty(globalThis, k, { value: dom.window[k], configurable: true, writable: true });
    const m = await import(pathToFileURL(path.join(dir, 'board_message_text.js')).href);
    const mark = html => m.highlightMentionsHtml(html);
    const spans = html => { const d = dom.window.document.createElement('div'); d.innerHTML = html; return [...d.querySelectorAll('.mb-mention')].map(s => s.textContent + (s.classList.contains('mb-mention-self') ? '*' : '')); };

    assert.strictEqual(m.setBoardMentionNames(['Security Reviewer', 'Security Reviewer Windows', 'QA Engineer', 'coral-go', 'UI/UX']), true);
    assert.strictEqual(m.setBoardMentionNames(['UI/UX', 'coral-go', 'QA Engineer', 'Security Reviewer Windows', 'Security Reviewer']), false, 'the same names in another order are no change');

    // names with spaces match whole, the longest first
    assert.deepStrictEqual(spans(mark('<p>@Security Reviewer Windows please look</p>')), ['@Security Reviewer Windows']);
    assert.deepStrictEqual(spans(mark('<p>@Security Reviewer please look</p>')), ['@Security Reviewer']);
    assert.deepStrictEqual(spans(mark('<p>@Security Reviewer Linux</p>')), ['@Security Reviewer'], 'a longer name that is not a member is not part of it');
    assert.deepStrictEqual(spans(mark('<p>@QA Engineer, @Orchestrator and @all: thanks. @UI/UX.</p>')), ['@QA Engineer', '@Orchestrator', '@all', '@UI/UX']);
    assert.deepStrictEqual(spans(mark('<p>@coral-go builds</p>')), ['@coral-go'], 'a name with a hyphen');
    // the case the author wrote is kept
    assert.deepStrictEqual(spans(mark('<p>@orchestrator and @QA ENGINEER</p>')), ['@orchestrator', '@QA ENGINEER']);
    // not a mention: an unknown name, an email, a word run on, mid-word
    assert.deepStrictEqual(spans(mark('<p>@Nobody and bob@Orchestrator.com and x@all and @Operators and @QA Engineers</p>')), []);
    assert.deepStrictEqual(spans(mark('<p>mail me: a.b@Operator.example, @Operator_x, @alltogether</p>')), []);
    // never inside code, a code block, a link; text beside them is still handled
    assert.deepStrictEqual(spans(mark('<p>use <code>@Orchestrator</code> then @Orchestrator</p><pre><code>@all</code></pre><p><a href="https://x">@Operator</a> @Operator</p>')), ['@Orchestrator', '@Operator*']);
    // the viewer's own name (the Operator) is the stronger style
    assert.deepStrictEqual(spans(mark('<p>@Operator, @operator and @QA Engineer</p>')), ['@Operator*', '@operator*', '@QA Engineer']);
    // only text is touched: markup around it is intact, and a name with markup characters is never a member
    assert.strictEqual(mark('<p><strong>@all</strong> hello</p>'), '<p><strong><span class="mb-mention">@all</span></strong> hello</p>');
    m.setBoardMentionNames(['<img src=x onerror=1>', 'a.b+c (d)']);
    assert.deepStrictEqual(spans(mark('<p>@<img src=x onerror=1> and @a.b+c (d) and @aXbbc d</p>')), ['@a.b+c (d)'], 'a name is literal text in the pattern, and one with < is dropped');
    assert.strictEqual(mark('<p>no mentions here</p>'), '<p>no mentions here</p>');
    assert.strictEqual(mark(''), '');
    // the text inside a span is text, whatever the author wrote
    m.setBoardMentionNames([]);
    assert.ok(!/<img/.test(mark('<p>@all &lt;img src=x onerror=1&gt;</p>')));
    console.log('board_mentions.test.js: ok');
})().catch(e => { console.error(e); process.exit(1); });
