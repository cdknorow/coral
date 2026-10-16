// The composer's two autocomplete dropdowns (#3794: file_mention.js and command_mention.js share one dropdown list now, and had
// no test of their own). Pins what a person sees and does, so the shared code cannot change it:
//  - @ opens the file list (directory mode: directories first, a typed filter narrows and highlights, Enter on a file inserts its
//    path and a space, Enter on a directory goes into it; fuzzy mode: ranked paths, Tab inserts);
//  - / opens the command list (fuzzy, best match first, Enter inserts the command and a space); a / inside a word opens nothing;
//  - ArrowUp/ArrowDown move the selection (kept in view), Escape closes, a blur closes, clicking an item picks it.
// The file routes are answered in the page; the agent is the harness's mock. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

const STUB = `(() => {
    const inner = window.fetch;
    window.fetch = (url, opts) => {
        const u = new URL(String(url && url.url || url), location.origin);
        if (/\\/search-files$/.test(u.pathname)) {
            const body = u.searchParams.has('dir')
                ? { entries: (u.searchParams.get('dir') === 'src'
                    ? [{ name: 'main.go', type: 'file', path: 'src/main.go' }, { name: 'util.go', type: 'file', path: 'src/util.go' }]
                    : [{ name: 'src/', type: 'dir', path: 'src' }, { name: 'README.md', type: 'file', path: 'README.md' }, { name: 'main.go', type: 'file', path: 'main.go' }]) }
                : { files: ['src/main.go', 'src/util.go', 'README.md', 'docs/guide.md'] };
            return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } }));
        }
        return inner(url, opts);
    };
})();`;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const MOD = `(window.__coralStatic || '/static/')`;
    const type = async text => { await ev(`document.getElementById('command-input').focus(); true`); await Input.insertText({ text }); };
    const clear = async () => {
        await ev(`document.getElementById('command-input').focus(); true`);
        await Input.dispatchKeyEvent({ type: 'rawKeyDown', key: 'a', commands: ['selectAll'] });
        await Input.dispatchKeyEvent({ type: 'rawKeyDown', key: 'Backspace', commands: ['deleteBackward'] });
    };
    const key = async k => {
        const code = { ArrowDown: 40, ArrowUp: 38, Enter: 13, Tab: 9, Escape: 27 }[k];
        await Input.dispatchKeyEvent({ type: 'rawKeyDown', key: k, code: k, windowsVirtualKeyCode: code });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code: k, windowsVirtualKeyCode: code });
    };
    const list = cls => ev(`(() => { const d = document.querySelector('.${cls}-dropdown'); if (!d || getComputedStyle(d).display === 'none') return null; return { items: [...d.querySelectorAll('.${cls}-item')].map(n => ({ text: n.textContent.trim().replace(/\\s+/g, ' '), selected: n.classList.contains('selected'), strong: [...n.querySelectorAll('strong')].map(s => s.textContent) })), crumb: (d.querySelector('.file-mention-breadcrumb') || {}).textContent || '' }; })()`);
    // A list that is showing (the lists keep their items in the page while hidden).
    const shownWith = (cls, extra = 'true') => `(() => { const d = document.querySelector('.${cls}-dropdown'); return !!d && getComputedStyle(d).display !== 'none' && d.querySelectorAll('.${cls}-item').length >= 1 && (${extra}); })()`;
    const value = () => ev(`document.getElementById('command-input').value`);
    const closed = async cls => waitFor(`(() => { const d = document.querySelector('.${cls}-dropdown'); return !d || getComputedStyle(d).display === 'none'; })()`, `the ${cls} list to close`, 3000);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: STUB });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('mn-a', 'A', 3, { sleeping: false }); true`);
        await ev(`window.selectLiveSession('mn-a', 'claude', 'mn-a-id', 'local'); true`);
        await waitFor(`!!document.getElementById('command-input') && !document.getElementById('command-input').disabled && !!document.querySelector('.file-mention-dropdown') && !!document.querySelector('.command-mention-dropdown')`, 'the composer and both lists', 8000);

        console.log('1. @ opens the file list in directory mode: directories first, a filter narrows and highlights');
        await type('@');
        await waitFor(`!!document.querySelector('.file-mention-dropdown') && getComputedStyle(document.querySelector('.file-mention-dropdown')).display !== 'none'`, 'the file list', 5000);
        let l = await list('file-mention');
        assert.deepEqual(l.items.map(i => i.text), ['\u{1F4C1}src/', 'main.go', 'README.md'], 'directories first, then files by name (the folder icon on the directory)');
        assert.deepEqual(l.items.map(i => i.selected), [true, false, false], 'the first is selected');
        await key('ArrowDown');
        await waitFor(`document.querySelectorAll('.file-mention-item')[1].classList.contains('selected')`, 'ArrowDown to move the selection', 3000);
        await key('ArrowDown'); await key('ArrowDown'); // stops at the last
        await waitFor(`document.querySelectorAll('.file-mention-item')[2].classList.contains('selected')`, 'the last item', 3000);
        await key('ArrowUp');
        await waitFor(`document.querySelectorAll('.file-mention-item')[1].classList.contains('selected')`, 'ArrowUp', 3000);
        await type('ma');
        await waitFor(`document.querySelectorAll('.file-mention-item').length === 1`, 'the filter to narrow the list', 4000);
        l = await list('file-mention');
        assert.deepEqual(l.items, [{ text: 'main.go', selected: true, strong: ['ma'] }], 'one match, "ma" highlighted');
        console.log('   ✓ src/, main.go, README.md; selection moves and stops at the ends; "ma" leaves main.go with "ma" highlighted');

        console.log('2. Enter on a file inserts its path and a space; Enter on a directory goes into it');
        await key('Enter');
        await waitFor(`document.getElementById('command-input').value === 'main.go '`, 'the path inserted', 3000);
        await closed('file-mention');
        await clear();
        await type('@');
        await waitFor(`document.querySelectorAll('.file-mention-item').length === 3`, 'the root list', 4000);
        await key('Enter'); // src/ is first
        await waitFor(`document.getElementById('command-input').value === '@src/' && document.querySelectorAll('.file-mention-item').length === 2`, 'the src listing', 4000);
        l = await list('file-mention');
        assert.deepEqual(l.items.map(i => i.text), ['main.go', 'util.go']);
        assert.equal(l.crumb, 'src/', 'a breadcrumb names the directory');
        await key('Escape');
        await closed('file-mention');
        assert.equal(await value(), '@src/', 'Escape closes the list and keeps the text');
        console.log('   ✓ "main.go " inserted; @src/ lists src with its breadcrumb; Escape closes and keeps the text');

        console.log('3. Fuzzy mode ranks paths; Tab inserts; a click picks');
        await ev(`window.T.state.settings = { ...(window.T.state.settings || {}), file_search_mode: 'fuzzy' }; true`);
        await clear();
        await type('@mgo');
        await waitFor(shownWith('file-mention'), 'fuzzy results', 5000);
        l = await list('file-mention');
        assert.equal(l.items[0].text, 'src/main.go', 'main.go ranks first for "mgo"');
        assert.deepEqual(l.items[0].strong.join(''), 'mgo', 'the matched letters are highlighted');
        await key('Tab');
        await waitFor(`document.getElementById('command-input').value === 'src/main.go '`, 'Tab to insert the path', 3000);
        await clear();
        await type('@u');
        await waitFor(shownWith('file-mention', `[...d.querySelectorAll('.file-mention-item')].some(n => n.textContent.includes('util.go'))`), 'the u results', 5000);
        const rect = await ev(`(() => { const n = [...document.querySelectorAll('.file-mention-item')].find(x => x.textContent.includes('util.go')); const r = n.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);
        await Input.dispatchMouseEvent({ type: 'mousePressed', x: rect.x, y: rect.y, button: 'left', clickCount: 1 });
        await Input.dispatchMouseEvent({ type: 'mouseReleased', x: rect.x, y: rect.y, button: 'left', clickCount: 1 });
        await waitFor(`document.getElementById('command-input').value === 'src/util.go '`, 'a click to pick the item', 3000);
        console.log('   ✓ "mgo" -> src/main.go first with mgo highlighted, Tab inserts; clicking util.go inserts it');

        console.log('4. / opens the command list: best match first, Enter inserts the command and a space; a / in a word opens nothing');
        await ev(`window.T.state.settings = { ...(window.T.state.settings || {}), file_search_mode: 'directory' }; window.T.state.currentCommands = [
            { name: 'compact', command: '/compact', description: 'Compact the conversation' },
            { name: 'clear', command: '/clear', description: 'Clear the screen' },
            { name: 'review', command: '/review', description: 'Review code' }]; true`);
        await clear();
        await type('/');
        await waitFor(`!!document.querySelector('.command-mention-dropdown') && getComputedStyle(document.querySelector('.command-mention-dropdown')).display !== 'none'`, 'the command list', 4000);
        l = await list('command-mention');
        assert.deepEqual(l.items.map(i => i.text), ['/compact Compact the conversation', '/clear Clear the screen', '/review Review code'], 'every command, in order');
        await type('cl');
        await waitFor(shownWith('command-mention', `d.querySelector('.cmd-name').textContent === '/clear'`), '/clear to rank first for "cl"', 4000);
        l = await list('command-mention');
        assert.equal(l.items[0].text.startsWith('/clear'), true);
        assert.deepEqual(l.items[0].strong, ['cl'], 'the matched letters are highlighted');
        await key('Enter');
        await waitFor(`document.getElementById('command-input').value === '/clear '`, 'the command inserted', 3000);
        await closed('command-mention');
        await clear();
        await type('hello /re');
        await waitFor(shownWith('command-mention', `d.querySelector('.cmd-name').textContent === '/review'`), 'a / after a space', 4000);
        assert.equal((await list('command-mention')).items[0].text.startsWith('/review'), true, 'review matches "re"');
        await key('Escape');
        await closed('command-mention');
        await clear();
        await type('foo/bar');
        await new Promise(r => setTimeout(r, 400)); // sleep-ok: nothing may open for a / inside a word (no condition to wait for)
        assert.equal(await list('command-mention'), null, 'a / inside a word opens nothing');
        console.log('   ✓ all three commands in order; "cl" puts /clear first, Enter inserts "/clear "; "hello /re" finds /review; "foo/bar" opens nothing');

        console.log('5. A blur closes the list');
        await clear();
        await type('@');
        await waitFor(shownWith('file-mention'), 'the list', 4000);
        await ev(`document.getElementById('command-input').blur(); true`);
        await closed('file-mention');
        console.log('   ✓ closed on blur');

        console.log('\nAll mention-dropdown checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
