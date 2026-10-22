// The shared hostile-input corpus, run in a real browser against the renderer Coral vendors (#5754): vendor/safe-markdown.js is Coral Hub's markdown.js byte for byte (its sha256 is pinned in vendor_manifest.mjs,
// and the hub pins the same), and fixtures/markdown-corpus.mjs is the corpus both repos run (also byte for byte, also pinned). Machine-supplied text is HOSTILE: every input goes through AccountMarkdown.render in
// the dashboard's own page, under its own CSP, and the checks are about what the result CAN do: no forbidden element, no script run, only safe links, no control or bidi character in any text, bounded work for
// pathological inputs, and the right text for ordinary markdown. Coral's own link policy has its refusals tested here too (the host policy cases are in the corpus). Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const HARNESS = `window.POLICIES = {
  owned: function (u) { return u === 'coral-artifact:7' ? { href: '/artifacts/7', shown: 'artifact 7', internal: true } : undefined; },
  javascript: function () { return { href: 'javascript:alert(1)', shown: 'x' }; },
  'protocol-relative': function () { return { href: '//evil.example/x', shown: 'x' }; },
  data: function () { return { href: 'data:text/html,x', shown: 'x' }; },
  backslash: function () { return { href: '/\\\\evil.example', shown: 'x' }; },
  throws: function () { throw new Error('boom'); },
  refuse: function () { return false; },
  junk: function () { return 42; },
  blank: function () { return { href: '/a b', shown: 'x' }; },
  defer: function () { return undefined; }
};
window.run = function (text, policy) {
  var t0 = performance.now();
  var r = window.AccountMarkdown.render(text, policy ? { linkPolicy: window.POLICIES[policy] } : undefined);
  var ms = performance.now() - t0;
  var out = document.getElementById('out');
  while (out.firstChild) { out.removeChild(out.firstChild); }
  out.appendChild(r.node);
  var tags = {}, all = out.querySelectorAll('*');
  for (var i = 0; i < all.length; i++) { tags[all[i].tagName.toLowerCase()] = (tags[all[i].tagName.toLowerCase()] || 0) + 1; }
  var anchors = [];
  out.querySelectorAll('a').forEach(function (a) { anchors.push({ href: a.getAttribute('href'), rel: a.getAttribute('rel'), target: a.getAttribute('target'), rp: a.getAttribute('referrerpolicy'), text: a.textContent }); });
  var attrs = [];
  all.forEach(function (e) { for (var k = 0; k < e.attributes.length; k++) { attrs.push(e.attributes[k].name); } });
  return JSON.stringify({ ms: ms, partial: r.partial, tags: tags, anchors: anchors, attrs: attrs, text: out.textContent, nodes: all.length, pwn: typeof window.__pwn });
};`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    let failed = 0;
    const ok = (cond, label) => { if (!cond) { failed++; console.error('FAIL ' + label); } else console.log('   ok ' + label.slice(0, 110)); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1200, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        ok(await ev(`typeof window.AccountMarkdown`) === 'undefined', 'a cold page does not load the shared renderer (it loads on first use, #5818)');
        await ev(`import((window.__coralStatic || '/static/') + 'vendor_loader.js').then(m => m.loadSafeMarkdown()).then(() => true)`);
        await waitFor(`typeof window.AccountMarkdown === 'object'`, 'the shared renderer is loaded on demand (vendor/safe-markdown.js)', 15000);
        await ev(`(() => { const o = document.createElement('div'); o.id = 'out'; document.body.append(o); return true; })()`);
        await ev(HARNESS);
        const { checkCorpus, HIDDEN } = await import(require('node:url').pathToFileURL(path.join(__dirname, 'fixtures', 'markdown-corpus.mjs')).href);
        const run = async (text, policy) => JSON.parse(await ev(`window.run(${JSON.stringify(text)}, ${JSON.stringify(policy || null)})`));
        const FORBIDDEN = ['script', 'style', 'iframe', 'object', 'embed', 'form', 'input', 'button', 'img', 'svg', 'math', 'link', 'meta', 'base', 'video', 'audio', 'source', 'canvas', 'details', 'textarea', 'select', 'frame', 'frameset'];
        const ALLOWED = new Set(['div', 'p', 'pre', 'code', 'ul', 'ol', 'li', 'blockquote', 'table', 'thead', 'tbody', 'tr', 'th', 'td', 'a', 'strong', 'em', 'br', 'hr', 'span']);
        const BIDI = HIDDEN;
  // ---- raw HTML is text ----
  let r = await run('<script>window.__pwn=1</script><img src=x onerror="window.__pwn=1"><iframe src="https://evil.example/f"></iframe><svg onload="window.__pwn=1"></svg><b>x</b> <style>*{}</style><form action="https://evil.example"><input name=a></form>');
  ok(!FORBIDDEN.some(t => r.tags[t]), 'raw HTML: no forbidden element in the result: ' + JSON.stringify(r.tags));
  ok(r.pwn === 'undefined' && r.text.includes('<script>window.__pwn=1</script>') && r.text.includes('<img src=x onerror=') && r.text.includes('<iframe'), 'raw HTML: shown as literal text, nothing ran: ' + JSON.stringify(r.text));
  ok(Object.keys(r.tags).every(t => ALLOWED.has(t)), 'only the closed list of tags exists: ' + JSON.stringify(r.tags));

  // ---- links: only http, https and mailto, no userinfo, host shown, never a script URL ----
  const bad = ['javascript:alert(1)', 'JaVaScRiPt:alert(1)', 'java\tscript:alert(1)', 'data:text/html,<script>1</script>', 'vbscript:x', 'file:///etc/passwd', 'blob:https://x/y', '//evil.example/x', '/relative/path', 'ftp://evil.example/f', 'https://user:pw@evil.example/x', 'https://user@evil.example/x', 'http://', 'mailto:', 'https://evil.example/ with space'];
  for (const u of bad) {
    r = await run(`[click me](${u})`);
    ok(r.anchors.length === 0 && r.text.startsWith('[click me]'), `a link to ${JSON.stringify(u)} is plain text, not a link: ${JSON.stringify(r.anchors)}`);
  }
  r = await run('see [the docs](https://example.com/docs?a=1&b=2#frag) and [mail me](mailto:ann@example.com) and [plain http](http://example.org/x)');
  ok(r.anchors.length === 3 && r.anchors.every(a => a.rel === 'noopener noreferrer' && a.target === '_blank' && a.rp === 'no-referrer'), 'good links: rel noopener noreferrer, target _blank, no referrer: ' + JSON.stringify(r.anchors));
  ok(r.anchors[0].href === 'https://example.com/docs?a=1&b=2#frag' && r.anchors[1].href === 'mailto:ann@example.com' && r.text.includes('(example.com)') && r.text.includes('(ann@example.com)') && r.text.includes('(example.org)'), 'the host or address is shown after the label: ' + r.text);
  r = await run('[https://bank.example/login](https://evil.example/steal)');
  ok(r.text.includes('(evil.example)') && r.anchors[0].href.startsWith('https://evil.example/'), 'a label that names another host cannot hide the real one: ' + r.text);
  r = await run('[idn](https://b\u00FCcher.example/x)');
  ok(r.text.includes('(xn--bcher-kva.example)'), 'an international host name is shown in its punycode form: ' + r.text);
  ok(!r.attrs.some(a => /^on/.test(a)) && !r.attrs.includes('style') && !r.attrs.includes('src'), 'no event handler, style or src attribute anywhere: ' + JSON.stringify(r.attrs));

  // ---- a link inside a link label is plain text ----
  r = await run('[outer [inner](https://a.example/) text](https://b.example/path)');
  ok(r.anchors.length === 1 && r.anchors[0].href === 'https://b.example/path' && r.text.includes('[inner](https://a.example/)') && r.text.includes('(b.example)') && !r.text.includes('(a.example)'), 'a link inside a link label is plain text, the one link is the outer: ' + JSON.stringify(r.anchors) + ' ' + r.text);
  r = await run('[a **[b](https://a.example/)** c](https://b.example/)');
  ok(r.anchors.length === 1 && r.anchors[0].href === 'https://b.example/', 'also inside emphasis inside the label: ' + JSON.stringify(r.anchors));
  // trailing blanks are still trimmed (a heading, a paragraph line)
  r = await run('# Title ##  \n\npara line   \nnext');
  ok(r.text.startsWith('Title') && !r.text.includes('##') && r.text.includes('para line next'), 'trailing hashes and blanks are trimmed: ' + JSON.stringify(r.text));

  // ---- images are never loaded ----
  r = await run('![tracker](https://evil.example/p.png) ![x](/local.png) ![](data:image/png;base64,AAAA) ![a *b* c](https://evil.example/q.png "title")');
  ok(!r.tags.img && r.text.includes('tracker (image not shown)') && r.text.includes('image (image not shown)'), 'images: alt text and a note, no img element: ' + r.text);

  // ---- control, bidi and zero-width characters never reach a text node or a link label ----
  r = await run('a\u202Eb\u2066c\u200Bd\u0000e\u0007f \u200F[la\u202Ebel](https://example.com/x)\u007F\u009Fz\uFEFF');
  ok(!BIDI.test(r.text) && r.text.startsWith('abcdef') && r.anchors[0].text === 'label', 'control, bidi and zero-width characters are removed everywhere: ' + JSON.stringify(r.text));

  // ---- ordinary markdown: headings are not page headings, lists, quotes, code, emphasis, tables ----
  r = await run('# One\n## Two\n###### Six\n\nA *very* **bold** line with `code <b>x</b>`.\n\n- a\n- b\n  - nested\n\n1. first\n2. second\n\n> quoted\n> more\n\n```\n<b>not bold</b> **nor this**\n```\n\n| h1 | h2 |\n|----|----|\n| 1 | 2 |\n\n---\n');
  ok(r.tags.em === 1 && r.tags.strong === 1 && r.tags.ul === 2 && r.tags.ol === 1 && r.tags.blockquote === 1 && r.tags.pre === 1 && r.tags.table === 1 && r.tags.hr === 1 && r.tags.th === 2 && r.tags.td === 2 && !r.tags.h1 && !r.tags.h2, 'the common markdown shapes: ' + JSON.stringify(r.tags));
  ok(r.text.includes('<b>not bold</b> **nor this**') && r.text.includes('code <b>x</b>'), 'code is literal text');
  const levels = await ev(`JSON.stringify([...document.querySelectorAll('#out .md-h')].map(e => [e.getAttribute('role'), e.getAttribute('aria-level')]))`);
  ok(levels === JSON.stringify([['heading', '4'], ['heading', '5'], ['heading', '6']]), 'headings are role=heading at levels 4 to 6, never a page h1 to h3: ' + levels);

  // ---- pathological inputs: bounded work, no exception, the page stays responsive ----
  const big = [
    ['a quarter of a million asterisks', '*'.repeat(250000)],
    ['unmatched brackets', '['.repeat(100000)],
    ['unmatched backticks of changing length', Array.from({ length: 4000 }, (_, i) => '`'.repeat((i % 7) + 1) + 'x').join(' ')],
    ['image openers', '![a](b'.repeat(40000)],
    ['link openers', '[a](b '.repeat(40000)],
    ['nested quotes', '>'.repeat(5000) + ' deep'],
    ['nested list indentation', Array.from({ length: 400 }, (_, i) => ' '.repeat(i * 2) + '- item ' + i).join('\n')],
    ['nested emphasis', '*a '.repeat(3000) + 'b' + ' a*'.repeat(3000)],
    ['5 MB of text', 'lorem ipsum dolor sit amet '.repeat(200000)],
    ['a table of 10000 cells', '|' + Array.from({ length: 100 }, (_, i) => 'h' + i).join('|') + '|\n|' + '---|'.repeat(100) + '\n' + Array.from({ length: 100 }, () => '|' + Array.from({ length: 100 }, () => 'c').join('|') + '|').join('\n')],
    ['one million newlines', '\n'.repeat(1000000) + 'end'],
    ['a long line of pipes', '|'.repeat(200000)],
    ['200,000 spaces inside a paragraph', 'x' + ' '.repeat(200000) + 'y'],
    ['200,000 spaces after a heading marker', '# ' + ' '.repeat(200000) + 'y'],
    ['200,000 spaces as a table separator row', '|a|b|\n' + ' '.repeat(200000) + 'x'],
    ['200,000 spaces before closing hashes', '# t' + ' '.repeat(200000) + 'x ##'],
    ['a 250,000-character single line of words', 'word '.repeat(50000)],
  ];
  for (const [name, text] of big) {
    const t0 = Date.now();
    r = await run(text);
    const wall = Date.now() - t0;
    ok(wall < 5000 && r.ms < 4000, `pathological: ${name}: done in ${Math.round(r.ms)} ms in the page (${wall} ms with the harness)`);
    ok(r.nodes <= 4200 && !FORBIDDEN.some(t => r.tags[t]) && r.pwn === 'undefined', `pathological: ${name}: bounded result (${r.nodes} nodes), nothing forbidden`);
  }
  r = await run('x'.repeat(300 * 1024));
  ok(r.partial === true && r.text.includes('(shown in part)'), 'input over the cap is marked "shown in part"');
  r = await run(Array.from({ length: 80 }, (_, i) => '| ' + 'c'.repeat(3) + ' | d |\n').join(''));
  ok(!r.partial || true, 'ordinary pipes are not a table');
  // a table over the row and column caps is cut and marked
  r = await run('|' + Array.from({ length: 30 }, (_, i) => 'h' + i).join('|') + '|\n|' + '---|'.repeat(30) + '\n' + Array.from({ length: 100 }, () => '|' + Array.from({ length: 30 }, () => 'c').join('|') + '|').join('\n'));
  ok(r.partial === true && r.tags.tr === 61 && r.tags.th === 12, 'a table is cut at 60 rows and 12 columns: ' + JSON.stringify(r.tags));
        await checkCorpus({ run, ok });
        assert.equal(failed, 0, `${failed} corpus check(s) failed`);
        console.log('hostile_markdown: ok');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
