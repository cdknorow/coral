// A module that registers actions is loaded (#3795, Security's condition for splitting the big modules). The action census reads
// every module's source, so it still passes when a registerActions({...}) block moves into a file that nothing imports: the
// actions are then registered nowhere at run time and their buttons do nothing. This fails on such a module: every file under
// static/ that calls registerActions must be reachable from a page's entry script through imports (static, dynamic, or the
// loaders' './x.js' paths).
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const fe = path.resolve(here, '../../coral-go/pkg/frontend');
const st = path.join(fe, 'static');
const stripComments = s => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:\\'"`])\/\/.*$/gm, '$1');
const read = f => fs.readFileSync(f, 'utf8');
const modules = new Map(fs.readdirSync(st).filter(f => f.endsWith('.js')).map(f => [f, stripComments(read(path.join(st, f)))]));

// The entry scripts: every module script of every page (templates and static pages).
const pages = [path.join(fe, 'templates'), st].flatMap(d => fs.existsSync(d) ? walkHtml(d) : []);
function walkHtml(d) {
    return fs.readdirSync(d, { withFileTypes: true }).flatMap(e => {
        const p = path.join(d, e.name);
        return e.isDirectory() ? (['vendor', 'agent_docs', 'node_modules'].includes(e.name) ? [] : walkHtml(p)) : p.endsWith('.html') ? [p] : [];
    });
}
const entries = new Set();
for (const p of pages) for (const m of read(p).matchAll(/<script[^>]*type="module"[^>]*src="[^"]*?([\w-]+\.js)"/g)) entries.add(m[1]);
assert.ok(entries.has('app.js'), `app.js is a page entry (found ${[...entries]})`);

// Any './name.js' string in a module is an edge: an import, a dynamic import, importOnce, a loader's path.
const reach = new Set();
const queue = [...entries];
while (queue.length) {
    const f = queue.pop();
    if (reach.has(f) || !modules.has(f)) continue;
    reach.add(f);
    for (const m of modules.get(f).matchAll(/['"`]\.\/([\w-]+\.js)(?:\?[^'"`]*)?['"`]/g)) queue.push(m[1]);
}
const registering = [...modules].filter(([, s]) => /^registerActions\(\{/m.test(s)).map(([f]) => f);
assert.ok(registering.length > 20, `the registering modules were found (${registering.length})`);
const lost = registering.filter(f => !reach.has(f));
assert.deepEqual(lost, [], `these modules register actions but no page's scripts import them, so those actions are registered nowhere: ${lost.join(', ')}`);
console.log(`action_modules_loaded: ok (${registering.length} modules register actions, all reachable from ${[...entries].join(', ')})`);
