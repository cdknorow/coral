// Which Material Icons the dashboard's own source can show (#3973). The icon font is a ligature font: an element's TEXT is the icon's
// name ("close", "arrow_back"), so a name left out of the font's subset renders as plain text. This scans every dashboard source
// file for anything that could be an icon name or an icon's code point and returns the set of icons the subset must keep. It is a
// SUPERSET on purpose (any whole string or element text that equals a ligature name counts, whatever it is used for), so a word that
// merely looks like an icon costs a few bytes and a real icon is never missed. A name assembled at run time ('arrow_' + dir) is the
// one thing a scan cannot see: icon_subset_browser.test.js checks the rendered dashboard for that.
//   node icon_names.mjs          prints the names, one per line
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
export const FRONTEND = path.resolve(HERE, '../../coral-go/pkg/frontend');
export const ALL_NAMES_FILE = path.resolve(HERE, '../../coral-go/tools/material-icons-all-names.txt');
const SKIP_DIRS = new Set(['vendor', '_src', 'node_modules', 'agent_docs']);
const EXTS = new Set(['.js', '.html', '.css', '.json']);

function walk(dir, out = []) {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
        const p = path.join(dir, e.name);
        if (e.isDirectory()) { if (!SKIP_DIRS.has(e.name)) walk(p, out); } else if (EXTS.has(path.extname(e.name))) out.push(p);
    }
    return out;
}

/** Everything in `src` that is a whole string literal or the whole text of an element: the places an icon name is written. */
export function candidates(src) {
    // An ARIA role is never an icon's text (role="list", setAttribute('role', 'list')): left out, so the roles the pages need do not pull the icons named like them into the font (#5813).
    src = src.replace(/\brole\s*=\s*(["'])[a-z]+\1/g, 'role=').replace(/(['"])role\1\s*,\s*(['"])[a-z]+\2/g, "'role', ''");
    const found = new Set();
    for (const m of src.matchAll(/(['"`])([a-z0-9_]{1,40})\1/g)) found.add(m[2]);                  // 'close'
    for (const m of src.matchAll(/>\s*([a-z0-9_]{1,40})\s*</g)) found.add(m[1]);                   // <span class="material-icons">close</span>
    for (const m of src.matchAll(/=\s*([a-z0-9_]{1,40})\s*[;,)]/g)) found.add(m[1]);               // textContent = close (rare)
    for (const m of src.matchAll(/icon[A-Za-z]*\s*[:=]\s*['"`]([a-z0-9_ ]{1,40})['"`]/g)) for (const w of m[1].split(' ')) found.add(w);
    return found;
}

/** Private-use code points the source writes (CSS "\e5cd", HTML &#xe5cd;, JS "" or the character itself). */
export function codePoints(src) {
    const found = new Set();
    for (const m of src.matchAll(/\\(?:u)?([eEfF][0-9a-fA-F]{3})(?![0-9a-fA-F])/g)) found.add(parseInt(m[1], 16));
    for (const m of src.matchAll(/&#x([eEfF][0-9a-fA-F]{3});/g)) found.add(parseInt(m[1], 16));
    for (const m of src.matchAll(/&#(\d{5});/g)) { const n = Number(m[1]); if (n >= 0xE000 && n <= 0xF8FF) found.add(n); }
    for (const ch of src) { const n = ch.codePointAt(0); if (n >= 0xE000 && n <= 0xF8FF) found.add(n); }
    return found;
}

export function allNames() { return new Set(fs.readFileSync(ALL_NAMES_FILE, 'utf8').split('\n').filter(Boolean)); }

/** {names: sorted icon names the source can show, codePoints: sorted private-use code points the source writes}. */
export function scanIcons(root = FRONTEND) {
    const all = allNames(), names = new Set(), points = new Set();
    for (const f of walk(root)) {
        const src = fs.readFileSync(f, 'utf8');
        for (const c of candidates(src)) if (all.has(c)) names.add(c);
        for (const p of codePoints(src)) points.add(p);
    }
    return { names: [...names].sort(), codePoints: [...points].sort((a, b) => a - b) };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
    const r = scanIcons();
    if (process.argv[2] === '--json') console.log(JSON.stringify(r)); else console.log(r.names.join('\n'));
}
