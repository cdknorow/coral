// The knowledge page's markdown without marked.js (#7045): the text is escaped before any tag is added.
import assert from 'node:assert/strict';
import { knowledgeFallbackHtml } from '../../coral-go/pkg/frontend/static/knowledge_fallback.js';

const html = knowledgeFallbackHtml('# Title\n\n**bold** and `code`\n- item\n[site](https://example.com/a)\n[other](./other-note.md) [gone](./nobody.md)', { 'other-note': 'Other Note' });
assert.match(html, /<h2 class="knowledge-h2">Title<\/h2>/);
assert.match(html, /<strong>bold<\/strong>/); assert.match(html, /<code class="knowledge-code">code<\/code>/); assert.match(html, /<div class="knowledge-li">&#8226; item<\/div>/);
assert.match(html, /<a href="https:\/\/example.com\/a" class="knowledge-link-ext" target="_blank" rel="noopener">site<\/a>/);
assert.match(html, /<a href="#" class="knowledge-link" data-knowledge-link="Other Note">other<\/a>/);
assert.match(html, /<span class="knowledge-link-dead">gone<\/span>/);
const evil = knowledgeFallbackHtml('<img src=x onerror=alert(1)> <script>alert(2)</script>\n## <b onmouseover=1>h</b>\n[x](javascript:alert(3)) [y](https://a.test/"onclick="alert(4))\n[<i>l</i>](./o.md)', { o: 'a"b<c' });
assert.ok(!/<img|<script|<b |<i>/.test(evil), 'no tag from the text survives: ' + evil);
assert.ok(!/href="javascript:/i.test(evil), 'a javascript: address is not a link');
assert.ok(!/"onclick/.test(evil), 'a quote cannot leave the href');
assert.match(evil, /data-knowledge-link="a&quot;b&lt;c"/, 'the target is escaped as an attribute');
assert.deepEqual(knowledgeFallbackHtml('', {}), '');
assert.ok(!knowledgeFallbackHtml('[x](./constructor.md)', {}).includes('data-knowledge-link'), 'an inherited name is not a note');
// Security's #7045 LOWs: a bounded link rule answers a hostile text at once, and a backtick in a name cannot be read across the attribute.
for (const hostile of ['['.repeat(100000) + 'x', '['.repeat(100000) + 'x](', '[a](https://' + 'a'.repeat(100000), '[a](./' + 'b'.repeat(100000), '[x]('.repeat(30000), '`'.repeat(100000), '**'.repeat(50000)]) {
    const t0 = performance.now(); knowledgeFallbackHtml(hostile, {});
    assert.ok(performance.now() - t0 < 500, `a hostile text returns at once (${hostile.slice(0, 12)}…: ${Math.round(performance.now() - t0)} ms)`);
}
const tick = knowledgeFallbackHtml('see [n](./a-b.md) and `code`', { 'a-b': 'a`b**c' });
assert.match(tick, /data-knowledge-link="a`b\*\*c"/, 'the attribute holds the name as it is: ' + tick);
assert.match(tick, /<code class="knowledge-code">code<\/code>/, 'the code rule still works');
assert.ok(!/data-knowledge-link="[^"]*<code/.test(tick), 'no tag inside the attribute');
assert.match(knowledgeFallbackHtml('[ok](https://example.com/x)', {}), /href="https:\/\/example.com\/x"/, 'a short link still links');
assert.ok(!/<a /.test(knowledgeFallbackHtml('[' + 'l'.repeat(300) + '](https://example.com/x)', {})), 'a label over 200 characters stays text');
console.log('knowledge_fallback: ok');
