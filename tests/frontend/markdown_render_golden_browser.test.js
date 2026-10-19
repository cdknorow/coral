// What renderMarkdown produces (#4985): highlighting a fenced block is done after the sanitizer has seen the block's text, not before (the highlighted spans made DOMPurify walk
// several times the nodes: 58% of a chat page's render time), and the HTML that comes out must be the same. Each sample's HTML is compared with fixtures/markdown_render_golden.json
// (recorded before the change; CORAL_UPDATE_GOLDEN=1 rewrites it): plain text, lists, tables, links, inline and fenced code in several languages (and an unknown one, and none),
// code that holds HTML and a closing code tag, raw HTML from an agent (script, handlers, javascript: links, images), a quote and a long line.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const GOLDEN = path.join(__dirname, 'fixtures', 'markdown_render_golden.json');

const SAMPLES = {
    plain: 'Just some **bold** and _italic_ text with a `code span` and a [link](https://example.com/a?b=1&c=2).',
    lists: '# Title\n\n- one\n- two\n  - nested\n\n1. first\n2. second\n\n> a quote\n> over two lines\n',
    table: '| a | b |\n|---|---|\n| 1 | 2 |\n| `x` | **y** |\n',
    go: 'Fix:\n\n```go\nfunc handle(ctx context.Context) error {\n\tmu.Lock()\n\tdefer mu.Unlock()\n\treturn run(ctx, "x") // done\n}\n```\n\nThen test.',
    js: '```js\nconst a = [1, 2, 3].map(x => x * 2);\nasync function f() { return await fetch("/api"); }\n```',
    python: '```python\ndef f(x: int) -> str:\n    """doc"""\n    return f"{x}"  # comment\n```',
    bash: '```bash\ngo test ./... -run "Test.*" -count=1 && echo ok\n```',
    json: '```json\n{"a": [1, 2, {"b": null}], "c": "d"}\n```',
    unknownLang: '```nosuchlang\nsome text <b>not html</b>\n```',
    noLang: '```\nplain block with <tags> & ampersands\n```',
    codeWithHtml: '```html\n<div class="x" onclick="alert(1)">hi</div></code></pre><script>alert(2)</script>\n```',
    inlineHtml: 'Text <b>bold</b> <img src=x onerror=alert(1)> <a href="javascript:alert(1)">bad</a> <script>alert(3)</script> <iframe src="https://evil"></iframe> end',
    twoBlocks: '```go\nx := 1\n```\n\ntext between\n\n```ts\nlet y: number = 2;\n```',
    pathsInCode: 'See `internal/server/handler.go:12` and:\n\n```go\n// internal/server/handler.go:12\nreturn os.ReadFile("coral-go/pkg/frontend/static/utils.js")\n```\n\nand https://example.com/x in prose.',
    rawPre: '<pre><code class="language-go">x := 1</code></pre>\n\n<pre><code class="language-go--hforged">y := 2</code></pre>',
    fenceInList: '- item\n\n  ```go\n  a := 1\n  ```\n\n- next with `code`',
        longLine: 'word '.repeat(400) + '\n\n```go\n' + 'a := b + c\n'.repeat(60) + '```',
};

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        const html = await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const v = await import(st + 'vendor_loader.js'); await v.loadHighlight();
            const u = await import(st + 'utils.js');
            const out = {}; for (const [k, md] of Object.entries(${JSON.stringify(SAMPLES)})) { out[k] = { chat: u.renderMarkdown(md, undefined, 'chat'), board: u.renderMarkdown(md, undefined, 'board') }; }
            return out; })()`);
        assert.ok(html.go.chat.includes('hljs'), 'highlight.js is loaded and colours the Go block');
        if (process.env.CORAL_UPDATE_GOLDEN) { fs.writeFileSync(GOLDEN, JSON.stringify(html, null, 1) + '\n'); console.log('golden written'); }
        else {
            const want = JSON.parse(fs.readFileSync(GOLDEN, 'utf8'));
            for (const k of Object.keys(want)) for (const mode of ['chat', 'board']) assert.equal(html[k][mode], want[k][mode], `${k} (${mode}) renders as it did`);
            assert.deepEqual(Object.keys(html), Object.keys(want));
        }
        console.log('PASS markdown render golden');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
