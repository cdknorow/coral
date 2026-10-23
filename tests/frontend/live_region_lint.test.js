// A polite live region that is shown and filled in the same turn says nothing (#6579 Saved, #6611 Queued). A screen reader announces a change of text in a region it already has in the page; a region that is
// hidden (hidden attribute, display none) is not in the page for it, so text set in the same task that un-hides it is just there when the region appears, and nothing is read. (role=alert is the exception:
// browsers announce an alert that appears, which the announcements census says too, so alerts are not checked here.) The way out is to show the region first and set its text an animation frame later (or keep the
// region always rendered and empty).
//
// This scan reads the templates and the static JS. For every region the templates declare as role=status/log or aria-live=polite it looks for two shapes, in one function, with no setTimeout, animation frame,
// microtask or await in between:
//   - shown and filled: the region is un-hidden (hidden = ..., classList.remove('hidden'), display = '' or a box) and its text, or the text of a descendant of it, is set (textContent, innerHTML, append,
//     replaceChildren... on the region, on an element with an id inside it, or on a querySelector of it);
//   - shown with its words: the region starts hidden with its words already in the template and a function un-hides it: the words are there when it appears, which is the same silence.
// Each is named with file and line. Known offenders are listed below, each with why; a new one fails, and so does an entry that no longer matches (a fixed region leaves the list). What it cannot see: a region
// built in JS (setAttribute('role', 'status')), a show and a fill in two functions of the same turn, a region reached by a variable the scan did not bind. The announcements census (helpers/announce_census.js)
// records what the scenes do on a real page.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');

const FRONTEND = path.resolve(__dirname, '../../coral-go/pkg/frontend');
const STATIC = path.join(FRONTEND, 'static');
const TEMPLATES = path.join(FRONTEND, 'templates');

// Reviewed, still in the page (file:region id). All of them are offenders to fix: none is accepted as not needing its words read. (#6637 fixed the five the first version of this lint found.)
const KNOWN = {
  // shown and filled, through a descendant or a wrapper (#6638)
  // shown with its words already in the template (#6638)
  'launch_dialog.js:launch-dir-note': 'the home-folder warning appears with its words already in it',
  'settings_dialog.js:settings-unsaved': '"Unsaved changes" appears with its words already in it',
  'terminal_backend.js:default-notice-banner': 'the tmux notice appears with its words already in it',
};

function walk(dir, ext, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) { if (!['vendor', '_src', 'node_modules'].includes(e.name)) walk(p, ext, out); } else if (e.name.endsWith(ext)) out.push(p);
  }
  return out;
}

const VOID = new Set(['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'param', 'source', 'track', 'wbr']);

/** A small tree of one HTML source: { tag, attrs, children, text } (text is what sits directly in the element). */
function parseHtml(s) {
  const root = { tag: '#root', attrs: '', children: [], text: '' }, stack = [root];
  for (const m of s.matchAll(/<!--[\s\S]*?-->|<(\/?)([a-zA-Z][a-zA-Z0-9-]*)\b([^>]*?)(\/?)>|([^<]+)/g)) {
    if (m[0].startsWith('<!--')) continue;
    if (m[5] !== undefined) { stack[stack.length - 1].text += m[5]; continue; }
    const [, close, tagRaw, attrs, selfClose] = m, tag = tagRaw.toLowerCase();
    if (close) { for (let i = stack.length - 1; i > 0; i--) if (stack[i].tag === tag) { stack.length = i; break; } continue; }
    const n = { tag, attrs, children: [], text: '' };
    stack[stack.length - 1].children.push(n);
    if (!VOID.has(tag) && !selfClose) stack.push(n);
  }
  return root;
}
function* nodes(n) { yield n; for (const c of n.children) yield* nodes(c); }
const textOf = n => (n.text + n.children.map(textOf).join('')).replace(/\s+/g, ' ').trim();

/** The polite regions the templates declare (role=status or log, aria-live=polite; alerts and assertive regions are announced when they appear): id -> { hidden, text, descendants }. */
function politeRegions(htmlFiles) {
  const out = new Map();
  for (const f of htmlFiles) {
    for (const n of nodes(parseHtml(fs.readFileSync(f, 'utf8')))) {
      if (!/role="(status|log)"|aria-live="polite"/.test(n.attrs) || /role="alert"|aria-live="assertive"/.test(n.attrs)) continue;
      const id = /\bid="([^"]+)"/.exec(n.attrs);
      if (!id) continue;
      const descendants = [...nodes(n)].filter(d => d !== n).map(d => /\bid="([^"]+)"/.exec(d.attrs)).filter(Boolean).map(m => m[1]);
      out.set(id[1], { hidden: /(?<![\w-])hidden(?![\w-])/.test(n.attrs), text: textOf(n), descendants });
    }
  }
  return out;
}

/** Every {...} block of the source as [start, end], skipping strings, template literals and comments. */
function blocks(s) {
  const out = [], stack = [];
  let instr = null;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (instr) { if (c === '\\') i++; else if (c === instr) instr = null; continue; }
    if (s.startsWith('//', i)) { const j = s.indexOf('\n', i); i = j < 0 ? s.length : j; continue; }
    if (s.startsWith('/*', i)) { const j = s.indexOf('*/', i); i = j < 0 ? s.length : j + 1; continue; }
    if (c === "'" || c === '"' || c === '`') instr = c;
    else if (c === '{') stack.push(i);
    else if (c === '}' && stack.length) out.push([stack.pop(), i]);
  }
  return out;
}

const SHOW = String.raw`\??\.(?:hidden\s*=(?!\s*true\b)[^;]*|style\.display\s*=\s*(?:''|""|['"](?:block|flex|inline[a-z-]*|grid)['"])|classList\.(?:remove|toggle)\(\s*['"]hidden['"]|removeAttribute\(\s*['"]hidden['"]\))`;
const FILL = String.raw`\??\.(?:textContent\s*\+?=|innerHTML\s*\+?=|innerText\s*=|append\(|replaceChildren\(|appendChild\(|replaceWith\()`;
const DEFER = /\b(?:setTimeout|requestAnimationFrame|queueMicrotask|await)\b/; // raf-ok: the scan names the call it accepts, it does not wait on a frame
const esc = s => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/** The ways the source reaches the element with this id inside `body`: a direct lookup, a variable bound to one, a one-line wrapper function (function banner() { return document.getElementById('x'); }), a variable bound to that. */
function refsTo(id, body, wrappers) {
  const look = String.raw`(?:getElementById|\$)\(\s*['"]${esc(id)}['"]\s*\)`;
  const refs = [String.raw`document\.getElementById\(\s*['"]${esc(id)}['"]\s*\)`];
  for (const m of body.matchAll(new RegExp(String.raw`(?:const|let|var|,)\s*(\w+)\s*=\s*(?:document\.)?${look}`, 'g'))) refs.push(String.raw`\b${m[1]}\b`); // `const a = ..., note = document.getElementById('x')` binds note too
  for (const w of wrappers.get(id) || []) {
    refs.push(String.raw`\b${w}\(\)`);
    for (const m of body.matchAll(new RegExp(String.raw`(?:const|let|var)\s+(\w+)\s*=\s*${w}\(\)`, 'g'))) refs.push(String.raw`\b${m[1]}\b`);
  }
  return refs;
}

/** Offenders in one source: [{ id, kind: 'filled' | 'words', showLine, fillLine? }]. `regions` is the Map politeRegions gives (a Set of ids is a region with nothing in it). */
function scan(source, regionsIn) {
  const regions = regionsIn instanceof Map ? regionsIn : new Map([...regionsIn].map(id => [id, { hidden: false, text: '', descendants: [] }]));
  const lineStarts = [0];
  for (let i = 0; i < source.length; i++) if (source[i] === '\n') lineStarts.push(i + 1);
  const lineOf = p => { let lo = 0, hi = lineStarts.length - 1; while (lo < hi) { const mid = (lo + hi + 1) >> 1; if (lineStarts[mid] <= p) lo = mid; else hi = mid - 1; } return lo + 1; };
  const wrappers = new Map();
  for (const m of source.matchAll(/function\s+(\w+)\s*\(\s*\)\s*\{\s*return\s+document\.getElementById\(\s*['"]([^'"]+)['"]\s*\)\s*;?\s*\}|(?:const|let)\s+(\w+)\s*=\s*\(\s*\)\s*=>\s*document\.getElementById\(\s*['"]([^'"]+)['"]\s*\)/g)) {
    const name = m[1] || m[3], id = m[2] || m[4];
    wrappers.set(id, [...(wrappers.get(id) || []), name]);
  }
  const found = new Map();
  for (const [a, b] of blocks(source)) {
    const body = source.slice(a, b);
    if (body.length > 6000) continue;
    for (const [id, info] of regions) {
      const names = [id, ...info.descendants];
      if (!names.some(n => body.includes(n)) && !(wrappers.get(id) || []).some(w => body.includes(w + '('))) continue;
      const mine = refsTo(id, body, wrappers);
      const show = mine.flatMap(t => [...body.matchAll(new RegExp(t + SHOW, 'g'))]);
      if (!show.length) continue;
      const fills = [
        ...mine.flatMap(t => [...body.matchAll(new RegExp(t + FILL, 'g')), ...body.matchAll(new RegExp(t + String.raw`\??\.querySelector(?:All)?\([^)]*\)\??` + FILL, 'g'))]),
        ...info.descendants.flatMap(d => refsTo(d, body, wrappers).flatMap(t => [...body.matchAll(new RegExp(t + FILL, 'g'))])),
      ];
      let any = false;
      for (const s of show) for (const f of fills) {
        any = true;
        const [x, y] = s.index < f.index ? [s.index, f.index] : [f.index, s.index];
        if (DEFER.test(body.slice(x, y))) continue;
        const key = id + ':' + lineOf(a + s.index);
        if (!found.has(key)) found.set(key, { id, kind: 'filled', showLine: lineOf(a + s.index), fillLine: lineOf(a + f.index) });
      }
      if (!any && info.hidden && info.text) { // un-hidden with the words the template gave it
        const key = id + ':' + lineOf(a + show[0].index);
        if (!found.has(key)) found.set(key, { id, kind: 'words', showLine: lineOf(a + show[0].index) });
      }
    }
  }
  return [...found.values()];
}

// ---- self-test: what it must and must not flag
{
  const ids = new Set(['r1']);
  const bad = `function a() { const el = document.getElementById('r1'); el.hidden = false; el.textContent = 'Saved'; }`;
  const fillFirst = `function a() { const el = document.getElementById('r1'); el.textContent = 'x'; el.hidden = !x; }`;
  const direct = `function a() { document.getElementById('r1').hidden = false; document.getElementById('r1').textContent = 'x'; }`;
  const frameLater = `function a() { const el = document.getElementById('r1'); el.hidden = false; requestAnimationFrame(() => { el.textContent = 'Saved'; }); }`; // raf-ok: a self-test input, not a wait
  const awaited = `async function a() { const el = document.getElementById('r1'); el.hidden = false; await tick(); el.textContent = 'Saved'; }`;
  const onlyFill = `function a() { const el = document.getElementById('r1'); el.textContent = 'Saved'; }`;
  const otherRegion = `function a() { const el = document.getElementById('r2'); el.hidden = false; el.textContent = 'Saved'; }`;
  const hide = `function a() { const el = document.getElementById('r1'); el.hidden = true; el.textContent = ''; }`;
  const queued = `function render() { const box = document.getElementById('composer-queued'); if (!box) return; if (box.hidden === !!items.length) box.hidden = !items.length; box.replaceChildren(...items.map(i => row(i))); }`; // #6611 as it was before its fix
  assert.equal(scan(queued, new Set(['composer-queued'])).length, 1, 'the Queued box of #6611');
  assert.equal(scan(`function a() { const i = document.getElementById('in'), el = document.getElementById('r1'); el.hidden = false; el.textContent = 'Saved'; }`, ids).length, 1, 'bound in a comma list');
  assert.equal(scan(bad, ids).length, 1, 'shown, then filled in one function');
  assert.equal(scan(fillFirst, ids).length, 1, 'filled, then shown');
  assert.equal(scan(direct, ids).length, 1, 'by getElementById at each use');
  assert.equal(scan(frameLater, ids).length, 0, 'filled a frame later');
  assert.equal(scan(awaited, ids).length, 0, 'filled after an await');
  assert.equal(scan(onlyFill, ids).length, 0, 'only filled');
  assert.equal(scan(otherRegion, ids).length, 0, 'not a live region');
  assert.equal(scan(hide, ids).length, 0, 'hiding is not showing');
  // a descendant of the region, and a wrapper function (#6638)
  const parent = new Map([['box', { hidden: true, text: '', descendants: ['box-text'] }]]);
  const viaChild = `function sync() { const el = document.getElementById('box'); el.hidden = !note; const t = document.getElementById('box-text'); t.textContent = note.text; }`;
  const viaChildLater = `function sync() { const el = document.getElementById('box'); el.hidden = !note; const t = document.getElementById('box-text'); setTimeout(() => { t.textContent = note.text; }, 0); }`;
  const viaQuery = `function sync() { const el = document.getElementById('box'); el.hidden = false; el.querySelector('.x').textContent = 'y'; }`;
  const viaWrapper = `function _banner() { return document.getElementById('box'); }\nfunction show() { const b = _banner(); b.hidden = false; document.getElementById('box-text').textContent = 'y'; }`;
  assert.equal(scan(viaChild, parent).length, 1, 'text set on a descendant counts as filling the region');
  assert.equal(scan(viaChildLater, parent).length, 0, 'a descendant filled later');
  assert.equal(scan(viaQuery, parent).length, 1, 'a querySelector of the region');
  assert.equal(scan(viaWrapper, parent).length, 1, 'the region reached through a one-line wrapper');
  // the words are already in the template (#6638)
  const worded = new Map([['note', { hidden: true, text: 'Unsaved changes', descendants: [] }]]);
  const wordless = new Map([['note', { hidden: true, text: '', descendants: [] }]]);
  const showOnly = `function sync(d) { const n = document.getElementById('note'); n.hidden = !d; }`;
  assert.deepEqual(scan(showOnly, worded).map(o => o.kind), ['words'], 'un-hidden with its words in the template');
  assert.equal(scan(showOnly, wordless).length, 0, 'an empty region shown alone says nothing, and nothing is lost');
  assert.equal(scan(`function sync() { const n = document.getElementById('note'); n.hidden = true; }`, worded).length, 0, 'hiding it');
  assert.equal(politeRegions([]).size, 0);
  assert.deepEqual([...parseHtml('<div id="a" role="status" hidden><span id="b">Hi</span><br><input></div>').children[0].children.map(c => c.tag)], ['span', 'br', 'input']);
}

const regions = politeRegions(walk(TEMPLATES, '.html'));
assert.ok(regions.size > 30, `the templates declare polite regions (${regions.size})`);
const offenders = [];
for (const f of walk(STATIC, '.js')) for (const o of scan(fs.readFileSync(f, 'utf8'), regions)) offenders.push({ file: path.basename(f), ...o });
const keyOf = o => `${o.file}:${o.id}`;
const fresh = offenders.filter(o => !(keyOf(o) in KNOWN));
const stale = Object.keys(KNOWN).filter(k => !offenders.some(o => keyOf(o) === k));
const problems = [
  ...fresh.map(o => o.kind === 'words'
    ? `${o.file}:${o.showLine}: #${o.id} (a polite live region) is un-hidden with its words already in the template: a screen reader says nothing. Set its text after it is shown, or make it not a live region.`
    : `${o.file}:${o.showLine}: #${o.id} (a polite live region) is shown here and filled at line ${o.fillLine} in the same turn: a screen reader says nothing. Show it, then set its text an animation frame later, or keep it rendered and empty.`),
  ...stale.map(k => `${k} is in KNOWN but no longer matches: take it out of the list`),
];
assert.deepEqual(problems, [], 'live regions that say nothing when they appear:\n  ' + problems.join('\n  '));
console.log(`PASS live regions: ${regions.size} polite regions scanned, ${new Set(offenders.map(keyOf)).size} still say nothing when they appear (all ${Object.keys(KNOWN).length} listed), none new`);
