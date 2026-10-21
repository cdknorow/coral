// The documentation site's search and share tags (#5643): every page of site_docs has its own title and a meta description of
// 70 to 160 characters (none repeated), the head carries Open Graph and Twitter tags from them (docs_theme/main.html, wired in
// mkdocs.yml) with the site's own icon and no image from elsewhere, and robots.txt names the sitemap of site_url. Static: the
// real mkdocs build is the documentation workflow's. Run on its own: node docs_seo.test.mjs
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const docs = path.join(root, 'coral-go/site_docs');
const mk = fs.readFileSync(path.join(root, 'mkdocs.yml'), 'utf8');

/** The front matter of a page as { key: value }: one line each, every value in double quotes (an unquoted value that holds ": " is not YAML, and mkdocs then drops the whole block without a word: the home, privacy and troubleshooting pages once lost theirs that way). */
export function frontMatter(text) {
    const m = /^---\n([\s\S]*?)\n---\n/.exec(text);
    if (!m) return null;
    const out = {};
    for (const line of m[1].split('\n')) {
        const kv = /^([A-Za-z_]+): (.*)$/.exec(line);
        assert.ok(kv, `front matter line is "key: value": ${line}`);
        const v = kv[2];
        assert.ok(/^"(?:[^"\\]|\\.)*"$/.test(v), `${kv[1]} is in double quotes (YAML reads an unquoted value with ": " as an error): ${v}`);
        out[kv[1]] = v.slice(1, -1).replace(/\\(["\\])/g, '$1');
    }
    return out;
}

// self-test
assert.deepEqual(frontMatter('---\ntitle: "A"\ndescription: "b: c"\n---\n\n# x'), { title: 'A', description: 'b: c' });
assert.equal(frontMatter('# no front matter'), null);
assert.throws(() => frontMatter('---\ntitle: A\ndescription: b: c\n---\n'), /double quotes/, 'an unquoted value is refused');
assert.throws(() => frontMatter('---\ntitle: "A"\ndescription: "b" c\n---\n'), /double quotes/);

const pages = fs.readdirSync(docs).filter(f => f.endsWith('.md'));
assert.ok(pages.length >= 10, 'the pages were found');
const seen = new Map();
for (const f of pages) {
    const fm = frontMatter(fs.readFileSync(path.join(docs, f), 'utf8'));
    assert.ok(fm, `${f} has front matter`);
    if (f === 'index.md') assert.equal(fm.title, undefined, 'the home page has no title of its own: the site shows its name, "Coral docs", alone (a title there would read "Coral docs - Coral docs")');
    else assert.ok(fm.title && fm.title.length >= 4 && fm.title.length <= 60, `${f}: a title of 4 to 60 characters (${fm.title})`);
    assert.ok(fm.description && fm.description.length >= 70 && fm.description.length <= 160, `${f}: a description of 70 to 160 characters (${(fm.description || '').length})`);
    for (const [key, v] of [['title', fm.title], ['description', fm.description]]) {
        if (v === undefined) continue;
        assert.ok(!seen.has(key + v), `${f}: its own ${key}, not ${seen.get(key + v)}'s`);
        seen.set(key + v, f);
    }
}

// The three pages whose block was once dropped, by name: their own words, not the site's.
const own = f => frontMatter(fs.readFileSync(path.join(docs, f), 'utf8'));
assert.match(own('index.md').description, /^Coral brings coding agents into one dashboard: /);
assert.equal(own('privacy.md').title, 'Coral privacy: what it sends and stores');
assert.match(own('troubleshooting.md').description, /^Fixes for common problems: /);
assert.match(mk, /^site_name: Coral docs$/m, 'the site is "Coral docs"');
assert.match(mk, /^  custom_dir: coral-go\/docs_theme$/m, 'the theme override is wired in mkdocs.yml');
const siteUrl = /^site_url:\s*(\S+)$/m.exec(mk)[1];
const head = fs.readFileSync(path.join(root, 'coral-go/docs_theme/main.html'), 'utf8');
for (const tag of ['og:type', 'og:site_name', 'og:title', 'og:description', 'og:url', 'og:image', 'twitter:card', 'twitter:title', 'twitter:description', 'twitter:image']) assert.ok(head.includes(`"${tag}"`), `the head has ${tag}`);
assert.ok(!/https?:\/\//.test(head.replace(/\{#[\s\S]*?#\}/g, '')), 'no address in the head template: the image is the site\'s own icon (site_url)');
assert.ok(head.includes('assets/coral-icon.png') && fs.existsSync(path.join(docs, 'assets/coral-icon.png')), 'the share image is the site\'s own icon');
const robots = fs.readFileSync(path.join(docs, 'robots.txt'), 'utf8');
assert.ok(robots.split('\n').includes(`Sitemap: ${siteUrl}sitemap.xml`), 'robots.txt names the sitemap');
console.log(`docs_seo: ok (${pages.length} pages, descriptions 70 to 160 characters, all different)`);
