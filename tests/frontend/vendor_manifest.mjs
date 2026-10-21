// The vendored front-end libraries (coral-go/pkg/frontend/static/vendor), what each file is, and
// where it came from (#3674, dependency audit #3673 finding 8). This list is the source of truth;
// vendor/README.md is generated from it, so a vendored file can't drift unnoticed.
//
//   node vendor_manifest.mjs           check: every file's sha256 matches the list, and the
//                                      README is what this list generates (run by test:cloud)
//   node vendor_manifest.mjs --fetch   also fetch each package's npm tarball, check it against the
//                                      registry's integrity and the sha256 recorded here, and
//                                      check the vendored file against the file in it
//   node vendor_manifest.mjs --rebuild also rebuild each bundle from its recorded input (npm ci, esbuild; needs the npm
//                                      registry or its cache) and check the output equals the vendored file
//   node vendor_manifest.mjs --write   rewrite the README from this list and the files' hashes
//
// "verbatim": the file is the package file byte for byte. "jsdelivr-header": jsDelivr's copy, the
// package file's bytes after a comment header jsDelivr adds. "jsdelivr-minified": jsDelivr's
// minification of the package file (not byte-comparable; the tarball is still checked). "bundle":
// built here from several packages (no single upstream file).
// A tarball is only read in memory (gunzip and the tar headers); nothing in it is run or written.
import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import zlib from 'node:zlib';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
export const VENDOR = path.resolve(here, '../../coral-go/pkg/frontend/static/vendor');
const README = path.join(VENDOR, 'README.md');

export const ENTRIES = [
    { file: 'purify.min.js', pkg: 'dompurify', version: '3.4.17', path: 'dist/purify.min.js', mode: 'verbatim',
        tarball: '056cbdae276897208b88c21d1d779adfa09ad7be488a2f510a28e900d3bbffe7',
        note: 'Sanitizes all agent-written HTML. Also byte-identical to dist/purify.min.js at the GitHub tag 3.4.17.' },
    { file: 'marked.min.js', pkg: 'marked', version: '15.0.12', path: 'marked.min.js', mode: 'verbatim', tarball: '39a847a896af15b2846f927da55d444619ea55a55c236e10333056474b793aaa' },
    { file: 'hljs/highlight.min.js', pkg: '@highlightjs/cdn-assets', version: '11.9.0', path: 'highlight.min.js', mode: 'verbatim', tarball: '62b15dbb4430b9692eb864e99393f4578646da1b25d437fc9e10a79786330dd0' },
    { file: 'hljs/github-dark.min.css', pkg: '@highlightjs/cdn-assets', version: '11.9.0', path: 'styles/github-dark.min.css', mode: 'verbatim', tarball: '62b15dbb4430b9692eb864e99393f4578646da1b25d437fc9e10a79786330dd0' },
    { file: 'xterm/xterm.min.js', pkg: '@xterm/xterm', version: '5.5.0', path: 'lib/xterm.js', mode: 'jsdelivr-header', tarball: 'bd954fa721872170188cc5d7e83e88db3c83c9a18a4e8d24c2783d26491f59d2' },
    { file: 'xterm/xterm.min.css', pkg: '@xterm/xterm', version: '5.5.0', path: 'css/xterm.css', mode: 'jsdelivr-minified', tarball: 'bd954fa721872170188cc5d7e83e88db3c83c9a18a4e8d24c2783d26491f59d2' },
    { file: 'xterm/addon-fit.min.js', pkg: '@xterm/addon-fit', version: '0.10.0', path: 'lib/addon-fit.js', mode: 'jsdelivr-header', tarball: '917ac44972453d5eed52edc1e50260c76398ce48cf2290c2e60671102bba0b33' },
    { file: 'xterm/addon-web-links.min.js', pkg: '@xterm/addon-web-links', version: '0.11.0', path: 'lib/addon-web-links.js', mode: 'jsdelivr-header', tarball: 'cad54687a1447f87cd8dd9ce454d4d657d18cc7179e9179908f391b4512f74a0' },
    { file: 'xterm/addon-unicode-graphemes.min.js', pkg: '@xterm/addon-unicode-graphemes', version: '0.4.0', path: 'lib/addon-unicode-graphemes.js', mode: 'verbatim', tarball: '293ff2ccc3f8fb0297606f2a135e7649fb20bc6d99806f8615d3a19fbcf16fc6' },
    { file: 'xterm/addon-canvas.min.js', pkg: '@xterm/addon-canvas', version: '0.7.0', path: 'lib/addon-canvas.js', mode: 'verbatim', tarball: 'f8004a9c444289c686ac9b58df98505f3e027fcfea411b6845f9279cc281a774',
        note: 'Upstream deprecates it in later xterm lines; pinned, revisit at the next xterm bump.' },
    { file: 'xterm/addon-canvas.LICENSE', pkg: '@xterm/addon-canvas', version: '0.7.0', path: 'LICENSE', mode: 'verbatim', tarball: 'f8004a9c444289c686ac9b58df98505f3e027fcfea411b6845f9279cc281a774' },
    // Shared with Coral Hub (#5754): ONE file, the same bytes in both repos. The hub pins this sha256; a change to it goes through Security on both sides, and fixtures/markdown-corpus.mjs
    // (the shared hostile-input corpus, also pinned) runs against it in a real browser (hostile_markdown_browser).
    { file: 'safe-markdown.js', mode: 'shared', pinned: 'f2e534847b953ab44c4f832e753ff9caf632bb93db9e8b56fbb9be82e79b1fad',
        from: 'Coral Hub, internal/accountui/assets/markdown.js (AccountMarkdown)',
        note: 'A small DOM-built markdown renderer for machine-supplied text (the shared Inbox, #5754): no HTML sink, no images, only http, https and mailto links with the host shown, hidden characters stripped, bounded work. '
            + 'A classic script, loaded on first use by vendor_loader.js (loadSafeMarkdown, #5818: not by the page); safe_markdown.js is its ES wrapper. Byte-identical to the hub\'s copy: the hub pins the same hash.' },
    { file: 'codemirror/codemirror-bundle.js', mode: 'bundle', built: '2026-03-23', input: 'tests/vendor-build/codemirror',
        note: 'An esbuild 0.28.1 IIFE (window.CoralCM) of CodeMirror 6 and its @lezer parsers, built from the input in `tests/vendor-build/codemirror` '
            + '(package.json with exact versions, package-lock.json, entry.js). Byte-verified: `node vendor_manifest.mjs --rebuild` runs '
            + '`npm ci --ignore-scripts` there and `esbuild entry.js --bundle --format=iife --global-name=CoralCM --minify`, and the output must '
            + 'equal this file. The lock was resolved with `npm install --before=2026-03-23T18:06:23Z`, the date of the earlier build, whose output '
            + 'this file replaces: the two have the same size and differ in 8 bytes, all minified variable names (the lang-sql dialect tables and '
            + 'yaml-frontmatter); the earlier build\'s input was never recorded, so that difference is not traced to a package. '
            + '(codemirror by its latest tag: 6.65.7 is an old CodeMirror 5 publish.) esbuild was raised from 0.27.4 to 0.28.1 (#5545, the npm audit finding) '
            + 'with the same output: the rebuild is byte for byte this file under both.',
        packages: [
            { pkg: 'codemirror', version: '6.0.2' }, { pkg: '@codemirror/state', version: '6.6.0' }, { pkg: '@codemirror/view', version: '6.40.0' },
            { pkg: '@codemirror/language', version: '6.12.2' }, { pkg: '@codemirror/commands', version: '6.10.3' },
            { pkg: '@codemirror/autocomplete', version: '6.20.1' }, { pkg: '@codemirror/lint', version: '6.9.5' },
            { pkg: '@codemirror/search', version: '6.6.0' }, { pkg: '@codemirror/merge', version: '6.12.1' },
            { pkg: '@codemirror/theme-one-dark', version: '6.1.3' }, { pkg: '@codemirror/lang-cpp', version: '6.0.3' },
            { pkg: '@codemirror/lang-css', version: '6.3.1' }, { pkg: '@codemirror/lang-go', version: '6.0.1' },
            { pkg: '@codemirror/lang-html', version: '6.4.11' }, { pkg: '@codemirror/lang-java', version: '6.0.2' },
            { pkg: '@codemirror/lang-javascript', version: '6.2.5' }, { pkg: '@codemirror/lang-json', version: '6.0.2' },
            { pkg: '@codemirror/lang-markdown', version: '6.5.0' }, { pkg: '@codemirror/lang-python', version: '6.2.1' },
            { pkg: '@codemirror/lang-rust', version: '6.0.2' }, { pkg: '@codemirror/lang-sql', version: '6.10.0' },
            { pkg: '@codemirror/lang-xml', version: '6.1.0' }, { pkg: '@codemirror/lang-yaml', version: '6.1.2' },
        ] },
];

// The shared hostile-input corpus (also in the hub's repo, same bytes): pinned here as the renderer is.
export const SHARED_CORPUS = path.resolve(here, 'fixtures/markdown-corpus.mjs');
export const SHARED_CORPUS_SHA = '4365c0455bd41e4dc013ffda0f5ac5b5fb9d2c21fc9515d02132678d62e0def3';

export function sha256(b) { return crypto.createHash('sha256').update(b).digest('hex'); }

// The bytes of `name` (under package/) in a gzipped npm tarball, or null.
export function tarFile(tgz, name) {
    const tar = zlib.gunzipSync(tgz);
    for (let off = 0; off + 512 <= tar.length;) {
        const h = tar.subarray(off, off + 512);
        if (h.every(b => b === 0)) break;
        const field = (a, n) => h.subarray(a, a + n).toString('latin1').replace(/\0.*$/s, '');
        const size = parseInt(field(124, 12).trim() || '0', 8);
        const full = (field(345, 155) ? field(345, 155) + '/' : '') + field(0, 100);
        const body = off + 512;
        if (full.replace(/^[^/]+\//, '') === name && (h[156] === 0x30 || h[156] === 0)) return tar.subarray(body, body + size);
        off = body + Math.ceil(size / 512) * 512;
    }
    return null;
}

// jsDelivr's copy: a leading /** ... */ header (and the newline after it), then the package file.
export function stripJsdelivrHeader(b) {
    const s = b.toString('utf8');
    const m = /^\/\*\*[\s\S]*?\*\/\r?\n/.exec(s);
    return m ? Buffer.from(s.slice(m[0].length), 'utf8') : b;
}

function readmeText(rows) {
    const out = [
        '# Vendored front-end libraries',
        '',
        'Served from Coral\'s own origin (`/static/vendor/`); the dashboard CSP allows scripts from `\'self\'` only.',
        'Generated by `tests/frontend/vendor_manifest.mjs --write`; `node tests/frontend/vendor_manifest.mjs` (in `npm run test:cloud`)',
        'fails if a file or this table drifts, and `--fetch` re-derives every row from the npm registry.',
        'To update a library: replace the file, set its version and tarball sha256 in the script, run `--fetch`, then `--write`.',
        'The fonts are listed in `fonts/README.md`.',
        '',
        '| File | Package | Version | SHA-256 of the file here | Upstream | npm tarball SHA-256 |',
        '|---|---|---|---|---|---|',
    ];
    for (const r of rows) {
        if (r.mode === 'bundle' || r.mode === 'shared') continue;
        const how = { verbatim: `\`${r.path}\`, byte for byte`, 'jsdelivr-header': `\`${r.path}\` after jsDelivr's comment header`,
            'jsdelivr-minified': `jsDelivr's minification of \`${r.path}\` (not byte-comparable)` }[r.mode];
        out.push(`| \`${r.file}\` | [\`${r.pkg}\`](https://www.npmjs.com/package/${r.pkg}) | ${r.version} | \`${r.sha}\` | ${how} | \`${r.tarball || 'not recorded'}\` |`);
    }
    for (const r of rows.filter(x => x.mode === 'bundle')) {
        out.push('', `## \`${r.file}\``, '', `SHA-256 \`${r.sha}\`. ${r.note}`, '', '| Package | Version |', '|---|---|');
        for (const p of r.packages) out.push(`| \`${p.pkg}\` | ${p.version} |`);
    }
    for (const r of rows.filter(x => x.mode === 'shared')) {
        out.push('', `## \`${r.file}\` (shared with Coral Hub)`, '', `SHA-256 \`${r.sha}\`, pinned in \`tests/frontend/vendor_manifest.mjs\`. From ${r.from}. ${r.note}`,
            '', 'The hub pins the same hash. A change to this file needs Security on both sides. `tests/frontend/fixtures/markdown-corpus.mjs` (pinned by the same test, `' + SHARED_CORPUS_SHA + '`) is the hostile-input corpus both repos run against it in a real browser.');
    }
    out.push('', '## Update rule for DOMPurify and marked', '',
        'They sanitize and render the agent-written HTML and markdown of the chat, the board, the docs and the artifact viewer. A security advisory for either one means an update of that file: within 7 days when it is rated high or critical, within 30 days otherwise. ',
        'Before the new version lands the whole front-end suite runs on it, with `html_interpolation_census` and `fixtures/markdown-corpus.mjs` (the shared hostile corpus) in a real browser. The Inbox does not use them (it uses `safe-markdown.js`).');
    const notes = rows.filter(r => r.note && r.mode !== 'bundle' && r.mode !== 'shared');
    if (notes.length) {
        out.push('', '## Notes', '');
        for (const r of notes) out.push(`- \`${r.file}\`: ${r.note}`);
    }
    return out.join('\n') + '\n';
}

function rowsNow() {
    return ENTRIES.map(e => ({ ...e, sha: sha256(fs.readFileSync(path.join(VENDOR, e.file))) }));
}

async function fetchCheck(e) {
    const meta = await (await fetch(`https://registry.npmjs.org/${e.pkg.replace('/', '%2f')}`)).json();
    const v = meta.versions && meta.versions[e.version];
    if (!v) return [`${e.file}: ${e.pkg}@${e.version} is not on npm`];
    const tgz = Buffer.from(await (await fetch(v.dist.tarball)).arrayBuffer());
    const errs = [];
    const [alg, want] = String(v.dist.integrity || '').split('-');
    if (!alg || crypto.createHash(alg).update(tgz).digest('base64') !== want) errs.push(`${e.file}: the tarball fails the registry's integrity`);
    const tsha = sha256(tgz);
    if (e.tarball && e.tarball !== tsha) errs.push(`${e.file}: tarball sha256 ${tsha}, recorded ${e.tarball}`);
    const inner = tarFile(tgz, e.path);
    if (!inner) errs.push(`${e.file}: ${e.path} is not in the tarball`);
    const mine = fs.readFileSync(path.join(VENDOR, e.file));
    if (inner && e.mode === 'verbatim' && !inner.equals(mine)) errs.push(`${e.file}: differs from ${e.path} in the tarball`);
    if (inner && e.mode === 'jsdelivr-header' && !inner.equals(stripJsdelivrHeader(mine))) errs.push(`${e.file}: after the jsDelivr header, differs from ${e.path}`);
    return { errs, tsha };
}

// A bundle's recorded input must be the packages the README lists: the exact versions in its package.json, and each one in the lock.
function bundleInputErrors(e) {
    const dir = path.resolve(here, '../..', e.input), errs = [];
    const pkg = JSON.parse(fs.readFileSync(path.join(dir, 'package.json'), 'utf8'));
    const lock = JSON.parse(fs.readFileSync(path.join(dir, 'package-lock.json'), 'utf8'));
    for (const p of e.packages) {
        if (pkg.dependencies[p.pkg] !== p.version) errs.push(`${e.file}: package.json has ${p.pkg} ${pkg.dependencies[p.pkg]}, the list says ${p.version}`);
        const l = lock.packages['node_modules/' + p.pkg];
        if (!l || l.version !== p.version || !l.integrity) errs.push(`${e.file}: package-lock.json has ${p.pkg} ${l && l.version}, the list says ${p.version}`);
    }
    for (const n of Object.keys(pkg.dependencies)) if (!e.packages.some(p => p.pkg === n)) errs.push(`${e.file}: package.json has ${n}, which the list does not`);
    if (!pkg.devDependencies || !/^\d+\.\d+\.\d+$/.test(pkg.devDependencies.esbuild || '')) errs.push(`${e.file}: esbuild must be pinned to an exact version in package.json`);
    return errs;
}

// The packages' license texts that the release packages ship (licenses/vendor, listed in licenses/vendor/INDEX.tsv and
// pinned in licenses/SHA256SUMS.vendor; see installers/third-party-notices.sh) must be exactly the vendored packages:
// every ENTRIES package and every non-dev package in a bundle's lock, at the vendored version. Offline.
export const LICENSES = path.resolve(here, '../../licenses');
export function licenseErrors() {
    const errs = [], want = new Set();
    for (const e of ENTRIES) {
        if (e.mode === 'bundle') {
            const lock = JSON.parse(fs.readFileSync(path.resolve(here, '../..', e.input, 'package-lock.json'), 'utf8'));
            for (const [k, v] of Object.entries(lock.packages)) if (k && !v.dev) want.add(`${k.replace(/^node_modules\//, '')}@${v.version}`);
        } else if (e.pkg) want.add(`${e.pkg}@${e.version}`);
    }
    const rows = fs.readFileSync(path.join(LICENSES, 'vendor', 'INDEX.tsv'), 'utf8').trim().split('\n').slice(1).map(l => l.split('\t'));
    const have = new Set();
    for (const [pkg, version, , dir] of rows) {
        if (!dir.startsWith('vendor/')) continue;
        have.add(`${pkg}@${version}`);
        if (dir !== `vendor/${pkg.replace('/', '__')}@${version}`) errs.push(`licenses/vendor/INDEX.tsv: ${pkg}@${version} points at ${dir}`);
        const d = path.join(LICENSES, dir);
        const lic = fs.existsSync(d) ? fs.readdirSync(d).filter(f => /^(licen[cs]e|copying)/i.test(f)) : [];
        if (!lic.length) errs.push(`licenses/${dir}: no license file`);
    }
    for (const id of want) if (!have.has(id)) errs.push(`licenses/vendor: no license text for ${id} (vendored)`);
    for (const id of have) if (!want.has(id)) errs.push(`licenses/vendor: ${id} is listed but not vendored`);
    const sums = new Map(fs.readFileSync(path.join(LICENSES, 'SHA256SUMS.vendor'), 'utf8').trim().split('\n').map(l => [l.slice(66), l.slice(0, 64)]));
    const files = [];
    (function walk(rel) {
        for (const ent of fs.readdirSync(path.join(LICENSES, rel), { withFileTypes: true })) {
            const r = path.posix.join(rel, ent.name);
            if (ent.isDirectory()) walk(r); else if (r !== 'vendor/INDEX.tsv') files.push(r);
        }
    })('vendor'); (function walk(rel) { for (const f of fs.readdirSync(path.join(LICENSES, rel))) files.push(path.posix.join(rel, f)); })('fonts');
    for (const f of files) {
        if (!sums.has(f)) errs.push(`licenses/SHA256SUMS.vendor: ${f} is not listed`);
        else if (sums.get(f) !== sha256(fs.readFileSync(path.join(LICENSES, f)))) errs.push(`licenses/${f}: differs from SHA256SUMS.vendor`);
    }
    for (const f of sums.keys()) if (!files.includes(f)) errs.push(`licenses/SHA256SUMS.vendor: ${f} is listed but missing`);
    return errs;
}

// Rebuild a bundle from its input into a temporary folder (the input folder's node_modules is not touched) and return the bytes.
function rebuildBundle(e) {
    const src = path.resolve(here, '../..', e.input);
    const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-vendor-build-'));
    try {
        for (const f of ['package.json', 'package-lock.json', 'entry.js']) fs.copyFileSync(path.join(src, f), path.join(tmp, f));
        execFileSync('npm', ['ci', '--ignore-scripts', '--no-audit', '--no-fund'], { cwd: tmp, stdio: 'pipe', shell: process.platform === 'win32' });
        execFileSync(path.join(tmp, 'node_modules', '.bin', 'esbuild' + (process.platform === 'win32' ? '.cmd' : '')),
            ['entry.js', '--bundle', '--format=iife', '--global-name=CoralCM', '--minify', '--outfile=out.js', '--log-level=error'], { cwd: tmp, stdio: 'pipe', shell: process.platform === 'win32' });
        return fs.readFileSync(path.join(tmp, 'out.js'));
    } finally { fs.rmSync(tmp, { recursive: true, force: true }); }
}

async function main() {
    const args = new Set(process.argv.slice(2));
    const rows = rowsNow();
    if (args.has('--fetch')) {
        let bad = 0;
        for (const e of ENTRIES.filter(x => x.mode !== 'bundle' && x.mode !== 'shared')) {
            const { errs, tsha } = await fetchCheck(e);
            for (const m of errs) { console.log('FAIL ' + m); bad++; }
            if (!errs.length) console.log(`ok   ${e.file}: ${e.pkg}@${e.version} (${e.mode}), tarball ${tsha}`);
        }
        if (bad) process.exit(1);
    }
    if (args.has('--rebuild')) {
        let bad = 0;
        for (const e of ENTRIES.filter(x => x.mode === 'bundle')) {
            const built = rebuildBundle(e), mine = fs.readFileSync(path.join(VENDOR, e.file));
            if (built.equals(mine)) console.log(`ok   ${e.file}: rebuilt from ${e.input}, byte for byte (${sha256(built)})`);
            else { console.log(`FAIL ${e.file}: the rebuild (${sha256(built)}, ${built.length} bytes) differs from the vendored file (${sha256(mine)}, ${mine.length} bytes)`); bad++; }
        }
        if (bad) process.exit(1);
    }
    if (args.has('--write')) {
        fs.writeFileSync(README, readmeText(rows));
        console.log('wrote ' + path.relative(process.cwd(), README));
        return;
    }
    const want = readmeText(rows);
    const have = fs.existsSync(README) ? fs.readFileSync(README, 'utf8') : '';
    if (have !== want) {
        console.error('vendor/README.md does not match the vendored files: a file changed without its row, or the row without the file.\n'
            + 'Check the file, then run: node tests/frontend/vendor_manifest.mjs --fetch && node tests/frontend/vendor_manifest.mjs --write');
        process.exit(1);
    }
    const sharedErrs = [];
    for (const r of rows.filter(x => x.mode === 'shared')) if (r.sha !== r.pinned) sharedErrs.push(`${r.file}: sha256 ${r.sha}, pinned ${r.pinned}: a change to a file shared with Coral Hub is a change in both repos, with Security on both sides`);
    if (sha256(fs.readFileSync(SHARED_CORPUS)) !== SHARED_CORPUS_SHA) sharedErrs.push(`fixtures/markdown-corpus.mjs differs from its pinned sha256 ${SHARED_CORPUS_SHA}`);
    if (sharedErrs.length) { console.error(sharedErrs.join('\n')); process.exit(1); }
    const inputErrs = ENTRIES.filter(e => e.mode === 'bundle').flatMap(bundleInputErrors);
    if (inputErrs.length) { console.error(inputErrs.join('\n')); process.exit(1); }
    const licErrs = licenseErrors();
    if (licErrs.length) { console.error(licErrs.join('\n')); process.exit(1); }
    const listed = new Set(ENTRIES.map(e => e.file));
    const unlisted = [];
    (function walk(d) {
        for (const ent of fs.readdirSync(d, { withFileTypes: true })) {
            const p = path.join(d, ent.name), rel = path.relative(VENDOR, p);
            if (ent.isDirectory()) { if (ent.name !== 'fonts') walk(p); } else if (rel !== 'README.md' && !listed.has(rel)) unlisted.push(rel);
        }
    })(VENDOR);
    if (unlisted.length) { console.error('vendored but not in the manifest: ' + unlisted.join(', ')); process.exit(1); }
    console.log(`vendor_manifest: ok (${ENTRIES.length} files match vendor/README.md)`);
}

if (import.meta.url === `file://${process.argv[1]}`) await main();
