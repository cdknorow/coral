// #3734: the file preview's Diff mode (preview_pane.js _mountDiff): a read-only CodeMirror unified merge view of the file's
// current text against the original, and its link to the editor (unsaved changes show up in the diff). Pins, on the
// bundle in vendor/codemirror:
//  - what is shown: the current text, each removed line in a deleted chunk, each changed line marked, the changes also
//    marked in the gutter and at word level, and the file's language highlighted;
//  - edits spread through a long file stay separate chunks (cm_util DIFF_CONFIG; the merge view's own scanLimit would
//    return the whole file as one) and the one scroller keeps a change and its removed lines together;
//  - the view is read-only and has no Accept/Reject buttons (they would do nothing on a read-only view);
//  - an edit made in the editor shows in the diff against the original, and the diff stays read-only;
//  - nothing in the pane carries an inline handler.
// The file endpoints are answered in the page; everything else goes to the isolated server.
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

const ORIGINAL = ['package main', '', 'import "fmt"', '', 'func greet(name string) string {', '\treturn "hello " + name', '}', '', 'func main() {', '\tfmt.Println(greet("world"))', '}', ''].join('\n');
const CURRENT = ['package main', '', 'import "fmt"', '', 'func greet(name string) string {', '\treturn "Hello, " + name + "!"', '}', '', 'func main() {', '\tfmt.Println(greet("world"))', '\tfmt.Println("done")', '}', ''].join('\n');
// 400 lines with three edits far apart (line 20 changed, line 200 removed, line 380 added after).
const LONG_ORIGINAL = Array.from({ length: 400 }, (_, i) => `const v${i} = ${i}; // line ${i}`);
const LONG_CURRENT = LONG_ORIGINAL.slice();
LONG_CURRENT[20] = 'const v20 = 2000; // line 20 changed';
LONG_CURRENT.splice(200, 1);
LONG_CURRENT.splice(379, 0, 'const added = true; // after line 379');

const FILES = `(() => {
    const files = ${JSON.stringify({
        '/src/main.go': { original: ORIGINAL, current: CURRENT },
        '/src/long.js': { original: LONG_ORIGINAL.join('\n'), current: LONG_CURRENT.join('\n') },
    })};
    const inner = window.fetch;
    const reply = content => Promise.resolve(new Response(JSON.stringify({ content }), { status: 200, headers: { 'content-type': 'application/json' } }));
    window.fetch = (url, opts) => {
        const u = new URL(String(url), location.origin);
        const f = files['/' + String(u.searchParams.get('filepath')).replace(/^[/]+/, '')];
        if (f && /\\/file-original$/.test(u.pathname)) return reply(f.original);
        if (f && /\\/file-content$/.test(u.pathname) && !(opts && opts.method === 'PUT')) return reply(f.current);
        return inner(url, opts);
    };
})();`;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const { waitStable } = require('./helpers/wait').bind(ev);
    const pane = sel => `document.querySelector('#preview-pane ${sel}')`;
    const paneAll = sel => `document.querySelectorAll('#preview-pane ${sel}')`;
    const lines = () => ev(`[...${paneAll('.cm-line')}].map(l => l.textContent)`);
    const open = async (path, mode) => {
        await ev(`(async () => { const pp = await import((window.__coralStatic || '/static/') + 'preview_pane.js'); pp.openPreviewTab(${JSON.stringify(path)}, 1, { mode: ${JSON.stringify(mode)}, diff: true }); return true; })()`);
        await waitFor(`!!${pane('.cm-editor')}`, `the ${mode} view of ${path}`, 8000, "(document.querySelector('#preview-pane') || document.body).innerText.slice(0, 300)");
        await waitStable(`${paneAll('.cm-line')}.length`, 'the view to settle', 200).catch(() => {});
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: FILES });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('ed-a', 'A', 3, { sleeping: false }); true`);
        await ev(`window.selectLiveSession('ed-a', 'claude', 'ed-a-id', 'local'); true`);

        console.log('1. The diff shows the current text, the removed lines, and marks what changed');
        await open('src/main.go', 'diff');
        assert.equal(await ev(`${pane('.cm-editor')}.classList.contains('cm-merge-b')`), true, 'the editor is a merge view');
        assert.deepEqual(await lines(), CURRENT.split('\n').slice(0, -1).concat(['']).map(l => l), 'the lines in the view are the current text, in order');
        assert.deepEqual(await ev(`[...${paneAll('.cm-deletedLine')}].map(l => l.textContent)`), ['\treturn "hello " + name'], 'the removed line is shown in a deleted chunk');
        assert.equal(await ev(`${paneAll('.cm-deletedChunk')}.length`), 2, 'two chunks: the replaced line, and the added line');
        assert.deepEqual(await ev(`[...${paneAll('.cm-changedLine')}].map(l => l.textContent)`), ['\treturn "Hello, " + name + "!"', '\tfmt.Println("done")'], 'the changed lines are the two new ones');
        assert.equal(await ev(`${paneAll('.cm-line:not(.cm-changedLine) ins, .cm-line:not(.cm-changedLine) del')}.length`), 0, 'unchanged lines carry no change mark');
        assert.equal(await ev(`${paneAll('.cm-gutter.cm-changeGutter .cm-changedLineGutter')}.length`), 2, 'the gutter marks the two changed lines');
        assert.equal(await ev(`${paneAll('.cm-gutter.cm-changeGutter .cm-deletedLineGutter')}.length`), 2, 'and the two places where lines were removed');
        const marks = await ev(`(() => {
            const bg = s => getComputedStyle(document.querySelector('#preview-pane ' + s)).backgroundColor;
            const texts = s => [...document.querySelectorAll('#preview-pane ' + s)].map(n => n.textContent);
            return { chunk: bg('.cm-deletedChunk'), changed: bg('.cm-changedLine'), plain: bg('.cm-line:not(.cm-changedLine):not(.cm-activeLine)'), newWords: texts('.cm-changedText'), oldWords: texts('.cm-deletedText') };
        })()`);
        const clear = c => !c || c === 'rgba(0, 0, 0, 0)' || c === 'transparent';
        assert.ok(!clear(marks.chunk), `a removed chunk has a background (${marks.chunk})`);
        assert.ok(!clear(marks.changed), `a changed line has a background (${marks.changed})`);
        assert.notEqual(marks.chunk, marks.changed, 'removed and added lines are told apart by colour');
        assert.ok(clear(marks.plain), `an unchanged line has none (${marks.plain})`);
        assert.ok(marks.newWords.length > 0 && marks.oldWords.length > 0, `the changed words are marked on both sides (${JSON.stringify(marks)})`);
        assert.ok(marks.newWords.join('').includes('Hello') && marks.oldWords.join('').includes('hello'), `the marked words are the ones that changed (${JSON.stringify(marks)})`);
        console.log('   ✓ current text, 1 removed line, 2 changed lines, gutter and word marks');

        console.log('2. The file\'s language is highlighted');
        const hl = await ev(`(() => {
            const content = document.querySelector('#preview-pane .cm-content');
            const line = [...content.querySelectorAll('.cm-line')].find(l => l.textContent.startsWith('func greet'));
            const spans = [...line.querySelectorAll('span')];
            const kw = spans.find(s => s.textContent === 'func');
            const colors = new Set(spans.map(s => getComputedStyle(s).color));
            return { language: content.getAttribute('data-language'), spans: spans.length, kw: !!kw, kwColor: kw && getComputedStyle(kw).color, plain: getComputedStyle(content).color, colors: colors.size };
        })()`);
        assert.equal(hl.language, 'go', 'the Go language is applied');
        assert.ok(hl.kw && hl.spans >= 3, `the line is tokenised (${hl.spans} spans)`);
        assert.notEqual(hl.kwColor, hl.plain, `the keyword is not the plain text colour (${hl.kwColor} vs ${hl.plain})`);
        assert.ok(hl.colors >= 2, `more than one colour on the line (${hl.colors})`);
        console.log(`   ✓ data-language go; "func" is ${hl.kwColor}, plain text ${hl.plain}`);

        console.log('3. It is read-only and has no Accept/Reject buttons');
        assert.equal(await ev(`${pane('.cm-content')}.getAttribute('contenteditable')`), 'false');
        assert.equal(await ev(`${pane('.cm-content')}.getAttribute('aria-readonly')`), 'true');
        assert.equal(await ev(`${paneAll('.cm-chunkButtons, .cm-chunkButtons button')}.length`), 0, 'no chunk buttons');
        assert.equal(await ev(`${paneAll('button[name=accept], button[name=reject]')}.length`), 0, 'no Accept or Reject');
        console.log('   ✓ contenteditable=false, aria-readonly, no chunk buttons');

        console.log('4. Nothing in the pane carries an inline handler');
        const inline = await ev(`[...${paneAll('*')}].flatMap(n => [...n.attributes].filter(a => /^on/i.test(a.name)).map(a => n.tagName + '.' + a.name))`);
        assert.deepEqual(inline, [], 'no on* attribute anywhere in the preview pane');
        console.log('   ✓ no on* attributes in the pane');

        console.log('5. Edits spread through a long file stay separate chunks; the scroller keeps a change and its removed lines together');
        await open('src/long.js', 'diff');
        // The view only renders what is on screen: walk the scroller and collect what carries a mark.
        const seen = { changed: new Set(), removed: new Set() };
        const top = await ev(`${pane('.cm-scroller')}.scrollHeight - ${pane('.cm-scroller')}.clientHeight`);
        for (let y = 0; ; y += 400) {
            await ev(`(() => { ${pane('.cm-scroller')}.scrollTop = ${Math.min(y, top)}; return true; })()`);
            await waitStable(`${pane('.cm-scroller')}.scrollTop + ':' + ${paneAll('.cm-line')}.length`, 'the lines at this scroll position', 100).catch(() => {});
            for (const t of await ev(`[...${paneAll('.cm-changedLine')}].map(l => l.textContent)`)) seen.changed.add(t);
            for (const t of await ev(`[...${paneAll('.cm-deletedLine')}].map(l => l.textContent)`)) seen.removed.add(t);
            if (y >= top) break;
        }
        assert.deepEqual([...seen.changed].sort(), ['const added = true; // after line 379', 'const v20 = 2000; // line 20 changed'], 'only the two new lines are marked, nothing else in 400 lines');
        assert.deepEqual([...seen.removed].sort(), ['const v20 = 20; // line 20', 'const v200 = 200; // line 200'], 'only the two removed lines are shown as removed');
        assert.equal(await ev(`${paneAll('.cm-scroller')}.length`), 1, 'one scroller for the removed and the new lines');
        // Each change sits in place: the removed original right above its replacement, and in view when scrolled to.
        const placed = async (marker, startsWith) => {
            await ev(`(() => { const s = ${pane('.cm-scroller')}; s.scrollTop = ${marker === 'end' ? 's.scrollHeight' : '0'}; return true; })()`);
            await waitFor(`[...${paneAll('.cm-changedLine')}].some(l => l.textContent.startsWith(${JSON.stringify(startsWith)}))`, `${startsWith} to render at the ${marker}`, 5000);
            return ev(`(() => {
                const s = document.querySelector('#preview-pane .cm-scroller'), sr = s.getBoundingClientRect();
                const line = [...document.querySelectorAll('#preview-pane .cm-changedLine')].find(l => l.textContent.startsWith(${JSON.stringify(startsWith)}));
                line.scrollIntoView({ block: 'center' });
                const r = line.getBoundingClientRect(), prev = line.previousElementSibling;
                return { marked: !!line.querySelector('.cm-insertedLine'), inView: r.bottom > sr.top && r.top < sr.bottom, chunkAbove: !!prev && prev.classList.contains('cm-deletedChunk'), removedAbove: prev ? prev.textContent.replace(/Accept|Reject/g, '') : null };
            })()`);
        };
        const first = await placed('start', 'const v20 = 2000');
        assert.ok(first.marked && first.inView && first.chunkAbove && first.removedAbove === 'const v20 = 20; // line 20', `line 20: its removed original is right above it (${JSON.stringify(first)})`);
        const last = await placed('end', 'const added');
        assert.ok(last.marked && last.inView && last.chunkAbove, `the added line near the end: marked, in view, with its chunk above (${JSON.stringify(last)})`);
        console.log('   ✓ 3 chunks, one scroller, the end and the start of the file both show their changes in place');

        console.log('6. An edit made in the editor shows in the diff against the original, and the diff stays read-only');
        await open('src/main.go', 'edit');
        assert.equal(await ev(`${pane('.cm-content')}.getAttribute('contenteditable')`), 'true', 'the editor is editable');
        assert.equal(await ev(`${paneAll('.cm-deletedChunk')}.length`), 0, 'the editor has no diff marks');
        await ev(`(() => {
            const cm = window.CoralCM, view = cm.EditorView.findFromDOM(document.querySelector('#preview-pane .cm-editor'));
            const doc = view.state.doc.toString(), at = doc.indexOf('"done"');
            view.dispatch({ changes: { from: at, to: at + '"done"'.length, insert: '"finished"' } });
            return true;
        })()`);
        await ev(`${pane('button[data-action=mode][data-mode=diff]')}.click(); true`);
        await waitFor(`${paneAll('.cm-deletedChunk')}.length >= 2 && !!${pane('.cm-merge-b')}`, 'the diff of the edited text', 8000);
        const edited = await lines();
        assert.ok(edited.includes('\tfmt.Println("finished")'), `the diff shows the unsaved edit (${JSON.stringify(edited.slice(-4))})`);
        assert.ok(!edited.includes('\tfmt.Println("done")'), 'and not the text from before the edit');
        assert.equal(await ev(`${pane('.cm-content')}.getAttribute('contenteditable')`), 'false', 'the diff is read-only again');
        assert.equal(await ev(`${paneAll('button[name=accept], button[name=reject]')}.length`), 0, 'still no Accept or Reject');
        await ev(`${pane('button[data-action=mode][data-mode=edit]')}.click(); true`);
        await waitFor(`${pane('.cm-content')}.getAttribute('contenteditable') === 'true'`, 'the editor again', 8000);
        assert.ok((await lines()).includes('\tfmt.Println("finished")'), 'back in the editor the edit is still there');
        console.log('   ✓ the unsaved edit appears in the diff; Diff is read-only; back in Edit the text is kept');

        console.log('\nAll editor diff checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
