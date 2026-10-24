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
console.log('knowledge_fallback: ok');
