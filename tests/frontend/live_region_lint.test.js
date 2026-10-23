// A polite live region that is shown and filled in the same turn says nothing (#6579 Saved, #6611 Queued). A screen reader announces a change of text in a region it already has in the page; a region that is
// hidden (hidden attribute, display none) is not in the page for it, so text set in the same task that un-hides it is just there when the region appears, and nothing is read. (role=alert is the exception:
// browsers announce an alert that appears, which the announcements census says too, so alerts are not checked here.) The way out is to show the region first and set its text a frame later (or keep the region
// always rendered and empty).
//
// This scan reads the static JS: for every region the templates declare as role=status/log or aria-live=polite, a function that both shows it (hidden = ..., classList.remove('hidden'), display = '' or a box) and fills it
// (textContent, innerHTML, append, replaceChildren...) with no setTimeout, animation-frame callback, queueMicrotask or await between the two is an offender, named with file and line. Known offenders are listed below, each
// with why it is still there; a new one fails, and so does an entry that no longer matches (a fixed region leaves the list). What it cannot see: a region built in JS (setAttribute('role', 'status')), a show and a fill
// in two functions of the same turn, and a region reached by a variable the scan did not bind. The announcements census (helpers/announce_census.js) records what the scenes do on a real page.
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');

const FRONTEND = path.resolve(__dirname, '../../coral-go/pkg/frontend');
const STATIC = path.join(FRONTEND, 'static');
const TEMPLATES = path.join(FRONTEND, 'templates');

// Reviewed, still in the page (file:region id). Each is a polite region that starts hidden and is shown and filled in one turn: the first words after it appears are not read.
const KNOWN = {
  'terminal_backend.js:tb-pending': 'the terminal backend note: text, then hidden = false',
};

function walk(dir, ext, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) { if (!['vendor', '_src', 'node_modules'].includes(e.name)) walk(p, ext, out); } else if (e.name.endsWith(ext)) out.push(p);
  }
  return out;
}

/** The ids of the polite regions the templates declare (role=status or log, aria-live=polite); alerts and assertive regions are announced when they appear. */
function politeRegionIds(htmlFiles) {
  const ids = new Set();
  for (const f of htmlFiles) {
    for (const m of fs.readFileSync(f, 'utf8').matchAll(/<[a-z0-9]+\b[^>]*>/gi)) {
      const t = m[0];
      if (!/role="(status|log)"|aria-live="polite"/.test(t) || /role="alert"|aria-live="assertive"/.test(t)) continue;
      const id = /\bid="([^"]+)"/.exec(t);
      if (id) ids.add(id[1]);
    }
  }
  return ids;
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

/** Offenders in one source: [{ id, showLine, fillLine }]. */
function scan(source, ids) {
  const lineStarts = [0];
  for (let i = 0; i < source.length; i++) if (source[i] === '\n') lineStarts.push(i + 1);
  const lineOf = p => { let lo = 0, hi = lineStarts.length - 1; while (lo < hi) { const mid = (lo + hi + 1) >> 1; if (lineStarts[mid] <= p) lo = mid; else hi = mid - 1; } return lo + 1; };
  const found = new Map();
  for (const [a, b] of blocks(source)) {
    const body = source.slice(a, b);
    if (body.length > 6000) continue;
    for (const id of ids) {
      if (!body.includes(id)) continue;
      const esc = id.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
      const direct = String.raw`document\.getElementById\(\s*['"]${esc}['"]\s*\)`;
      const vars = [...body.matchAll(new RegExp(String.raw`(?:const|let|var)\s+(\w+)\s*=\s*[^;\n]*?(?:getElementById|\$)\(\s*['"]${esc}['"]\s*\)`, 'g'))].map(m => String.raw`\b${m[1]}\b`);
      for (const t of [direct, ...vars]) {
        const show = [...body.matchAll(new RegExp(t + SHOW, 'g'))], fill = [...body.matchAll(new RegExp(t + FILL, 'g'))];
        for (const s of show) for (const f of fill) {
          const [x, y] = s.index < f.index ? [s.index, f.index] : [f.index, s.index];
          if (DEFER.test(body.slice(x, y))) continue;
          const key = id + ':' + lineOf(a + s.index);
          if (!found.has(key)) found.set(key, { id, showLine: lineOf(a + s.index), fillLine: lineOf(a + f.index) });
        }
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
  const frameLater = `function a() { const el = document.getElementById('r1'); el.hidden = false; requestAnimationFrame(() => { el.textContent = 'Saved'; }); }`; // raf-ok: the scan names the call it accepts, it does not wait on a frame
  const awaited = `async function a() { const el = document.getElementById('r1'); el.hidden = false; await tick(); el.textContent = 'Saved'; }`;
  const onlyFill = `function a() { const el = document.getElementById('r1'); el.textContent = 'Saved'; }`;
  const otherRegion = `function a() { const el = document.getElementById('r2'); el.hidden = false; el.textContent = 'Saved'; }`;
  const hide = `function a() { const el = document.getElementById('r1'); el.hidden = true; el.textContent = ''; }`;
  const queued = `function render() { const box = document.getElementById('composer-queued'); if (!box) return; if (box.hidden === !!items.length) box.hidden = !items.length; box.replaceChildren(...items.map(i => row(i))); }`; // #6611 as it was before its fix
  assert.equal(scan(queued, new Set(['composer-queued'])).length, 1, 'the Queued box of #6611');
  assert.equal(scan(bad, ids).length, 1, 'shown, then filled in one function');
  assert.equal(scan(fillFirst, ids).length, 1, 'filled, then shown');
  assert.equal(scan(direct, ids).length, 1, 'by getElementById at each use');
  assert.equal(scan(frameLater, ids).length, 0, 'filled a frame later');
  assert.equal(scan(awaited, ids).length, 0, 'filled after an await');
  assert.equal(scan(onlyFill, ids).length, 0, 'only filled');
  assert.equal(scan(otherRegion, ids).length, 0, 'not a live region');
  assert.equal(scan(hide, ids).length, 0, 'hiding is not showing');
  assert.equal(politeRegionIds([]).size, 0);
}

const ids = politeRegionIds(walk(TEMPLATES, '.html'));
assert.ok(ids.size > 30, `the templates declare polite regions (${ids.size})`);
const offenders = [];
for (const f of walk(STATIC, '.js')) for (const o of scan(fs.readFileSync(f, 'utf8'), ids)) offenders.push({ file: path.basename(f), ...o });
const keyOf = o => `${o.file}:${o.id}`;
const fresh = offenders.filter(o => !(keyOf(o) in KNOWN));
const stale = Object.keys(KNOWN).filter(k => !offenders.some(o => keyOf(o) === k));
const problems = [
  ...fresh.map(o => `${o.file}:${o.showLine}: #${o.id} (a polite live region) is shown here and filled at line ${o.fillLine} in the same turn: a screen reader says nothing. Show it, then set its text a frame later (requestAnimationFrame), or keep it rendered and empty.`), // raf-ok: the scan names the call it accepts, it does not wait on a frame
  ...stale.map(k => `${k} is in KNOWN but no longer matches: take it out of the list`),
];
assert.deepEqual(problems, [], 'live regions shown and filled in one turn:\n  ' + problems.join('\n  '));
console.log(`PASS live regions: ${ids.size} polite regions scanned, ${new Set(offenders.map(keyOf)).size} still shown and filled in one turn (all ${Object.keys(KNOWN).length} listed), none new`);
